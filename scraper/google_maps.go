package scraper

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/chromedp"

	"mapper/model"
)

const (
	maxPlaces        = 100
	enrichWorkers    = 5
	scrollStaleTries = 12
)

var (
	reMapsCoords    = regexp.MustCompile(`@(-?\d+\.\d+),(-?\d+\.\d+)`)
	reMapsPlace3d4d = regexp.MustCompile(`!3d(-?\d+\.\d+)!4d(-?\d+\.\d+)`)
	reMapsPlace2d3d = regexp.MustCompile(`!2d(-?\d+\.\d+)!3d(-?\d+\.\d+)`)
	rePlaceIDCh     = regexp.MustCompile(`(ChIJ[\w-]+)`)
	rePlaceIDHex    = regexp.MustCompile(`!1s(0x[0-9a-fA-F]+:0x[0-9a-fA-F]+)`)
	rePlacePath     = regexp.MustCompile(`/maps/place/([^/@]+)/`)
)

type GoogleMaps struct{}

func NewGoogleMaps() *GoogleMaps {
	return &GoogleMaps{}
}

// TimeoutFor estimates scrape duration with parallel enrich workers.
func TimeoutFor(limit int) time.Duration {
	if limit <= 0 {
		limit = model.DefaultLimit
	}
	if limit > maxPlaces {
		limit = maxPlaces
	}

	rounds := (limit + enrichWorkers - 1) / enrichWorkers
	// scroll/collect can be slow; enrich ~10s per round of workers
	total := 4*time.Minute + time.Duration(rounds)*10*time.Second
	if total < 6*time.Minute {
		total = 6 * time.Minute
	}
	if total > 15*time.Minute {
		total = 15 * time.Minute
	}
	return total
}

// Search scrapes up to limit places and enriches them in parallel.
func (s *GoogleMaps) Search(ctx context.Context, city, segment string, limit int) ([]model.Place, error) {
	if limit <= 0 || limit > maxPlaces {
		limit = maxPlaces
	}

	query := fmt.Sprintf("%s em %s, Brasil", strings.TrimSpace(segment), strings.TrimSpace(city))
	searchURL := "https://www.google.com/maps/search/" + url.PathEscape(query) + "?hl=pt-BR"

	parent := context.WithoutCancel(ctx)
	timeout := TimeoutFor(limit)
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 && remaining < timeout {
			timeout = remaining
		}
	}

	rootCtx, rootCancel := context.WithTimeout(parent, timeout)
	defer rootCancel()

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.Flag("lang", "pt-BR"),
		chromedp.WindowSize(1440, 900),
		chromedp.UserAgent("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36"),
	)
	if chromePath := os.Getenv("CHROME_PATH"); chromePath != "" {
		opts = append(opts, chromedp.ExecPath(chromePath))
	}

	allocCtx, allocCancel := chromedp.NewExecAllocator(rootCtx, opts...)
	defer allocCancel()

	// First context owns the browser process — keep it alive for worker tabs.
	browserCtx, browserCancel := chromedp.NewContext(allocCtx)
	defer browserCancel()
	if err := chromedp.Run(browserCtx); err != nil {
		return nil, fmt.Errorf("start browser: %w", err)
	}

	feedCtx, feedCancel := chromedp.NewContext(browserCtx)
	defer feedCancel()

	fmt.Printf("scrape timeout=%s limit=%d workers=%d\n", timeout, limit, enrichWorkers)

	if err := chromedp.Run(feedCtx,
		chromedp.Navigate(searchURL),
		chromedp.WaitReady("body", chromedp.ByQuery),
	); err != nil {
		_ = saveDebugArtifacts(feedCtx, "navigate")
		return nil, fmt.Errorf("navigate google maps: %w", err)
	}

	if err := acceptConsent(feedCtx); err != nil {
		fmt.Printf("consent click skipped: %v\n", err)
	}

	if blocked, reason := isBlocked(feedCtx); blocked {
		paths := saveDebugArtifacts(feedCtx, "blocked")
		return nil, fmt.Errorf("google maps blocked scraping (%s); debug: %s", reason, paths)
	}

	mode, err := waitForResults(feedCtx)
	if err != nil {
		paths := saveDebugArtifacts(feedCtx, "no_results")
		return nil, fmt.Errorf("results not found for city %q (Brasil only): %w; debug: %s", city, err, paths)
	}

	if mode == "place" {
		place, err := extractPlaceDetails(feedCtx)
		if err != nil {
			paths := saveDebugArtifacts(feedCtx, "single_place")
			return nil, fmt.Errorf("extract single place: %w; debug: %s", err, paths)
		}
		if place == nil || place.Name == "" {
			return []model.Place{}, nil
		}
		return []model.Place{*place}, nil
	}

	links, err := collectPlaceLinks(feedCtx, limit)
	if err != nil {
		return nil, err
	}
	fmt.Printf("collected %d feed links (limit=%d)\n", len(links), limit)

	if len(links) == 0 {
		if place, ferr := extractPlaceDetails(feedCtx); ferr == nil && place != nil && place.Name != "" {
			return []model.Place{*place}, nil
		}
		paths := saveDebugArtifacts(feedCtx, "empty_feed")
		return nil, fmt.Errorf("no places extracted for city %q; debug: %s", city, paths)
	}

	// Keep browserCtx alive; worker tabs are children of it.
	places := enrichPlacesParallel(rootCtx, browserCtx, links)
	if len(places) == 0 {
		if rootCtx.Err() != nil {
			return nil, fmt.Errorf("timeout enriching city %q (links=%d): %w", city, len(links), rootCtx.Err())
		}
		return nil, fmt.Errorf("no places enriched for city %q (links=%d)", city, len(links))
	}
	if len(places) > limit {
		places = places[:limit]
	}
	fmt.Printf("enriched %d/%d places\n", len(places), len(links))
	return places, nil
}

func enrichPlacesParallel(ctx context.Context, browserCtx context.Context, links []string) []model.Place {
	workers := enrichWorkers
	if workers > len(links) {
		workers = len(links)
	}

	type job struct {
		index int
		url   string
	}
	type result struct {
		index int
		place *model.Place
	}

	jobs := make(chan job)
	results := make(chan result, len(links))

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			// Child tab of the same browser (do NOT cancel browserCtx).
			tabCtx, tabCancel := chromedp.NewContext(browserCtx)
			defer tabCancel()

			if err := chromedp.Run(tabCtx); err != nil {
				fmt.Printf("worker %d init failed: %v\n", workerID, err)
				return
			}

			for j := range jobs {
				if ctx.Err() != nil {
					return
				}
				place, err := openAndExtractPlace(tabCtx, j.url)
				if err != nil || place == nil || place.Name == "" {
					fmt.Printf("worker %d skip: %v\n", workerID, err)
					continue
				}
				select {
				case results <- result{index: j.index, place: place}:
				case <-ctx.Done():
					return
				}
			}
		}(w)
	}

	go func() {
		defer close(jobs)
		for i, link := range links {
			select {
			case <-ctx.Done():
				return
			case jobs <- job{index: i, url: link}:
			}
		}
	}()

	go func() {
		wg.Wait()
		close(results)
	}()

	ordered := make([]*model.Place, len(links))
	seen := make(map[string]struct{})

	for res := range results {
		if res.place == nil {
			continue
		}
		key := res.place.ID
		if key == "" {
			key = strings.ToLower(res.place.Name + "|" + res.place.Address)
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if res.index >= 0 && res.index < len(ordered) {
			ordered[res.index] = res.place
		}
	}

	places := make([]model.Place, 0, len(links))
	for _, p := range ordered {
		if p != nil {
			places = append(places, *p)
		}
	}
	return places
}

func collectPlaceLinks(ctx context.Context, limit int) ([]string, error) {
	seen := make(map[string]struct{})
	links := make([]string, 0, limit)
	stale := 0

	collectDeadline := time.Now().Add(3 * time.Minute)
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 {
			budget := remaining / 3
			if budget > 3*time.Minute {
				budget = 3 * time.Minute
			}
			if budget < 45*time.Second {
				budget = 45 * time.Second
			}
			collectDeadline = time.Now().Add(budget)
		}
	}

	for len(links) < limit {
		if ctx.Err() != nil || time.Now().After(collectDeadline) {
			break
		}

		batch, err := extractFeedLinks(ctx)
		if err != nil {
			return nil, err
		}

		added := 0
		for _, href := range batch {
			href = strings.TrimSpace(href)
			if href == "" {
				continue
			}
			key := placeLinkKey(href)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			links = append(links, href)
			added++
			if len(links) >= limit {
				break
			}
		}

		fmt.Printf("feed links=%d (+%d)\n", len(links), added)

		if len(links) >= limit {
			break
		}

		ended, _ := isFeedEnded(ctx)
		if ended && added == 0 {
			fmt.Println("feed reached end of list")
			break
		}

		if added == 0 {
			stale++
			if stale >= scrollStaleTries {
				break
			}
		} else {
			stale = 0
		}

		beforeCount := feedAnchorCount(ctx)
		_ = scrollFeed(ctx)
		_ = waitForNewFeedLinks(ctx, beforeCount, 2500*time.Millisecond)
	}

	return links, nil
}

func placeLinkKey(href string) string {
	if m := rePlaceIDCh.FindStringSubmatch(href); len(m) > 1 {
		return m[1]
	}
	if m := rePlaceIDHex.FindStringSubmatch(href); len(m) > 1 {
		return strings.ToLower(m[1])
	}
	if m := rePlacePath.FindStringSubmatch(href); len(m) > 1 {
		return strings.ToLower(m[1])
	}
	if u, err := url.Parse(href); err == nil {
		u.RawQuery = ""
		u.Fragment = ""
		return strings.ToLower(u.String())
	}
	return strings.ToLower(href)
}

func extractFeedLinks(ctx context.Context) ([]string, error) {
	var links []string
	err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
		const feed = document.querySelector('div[role="feed"]');
		if (!feed) return [];
		const hrefs = [];
		const seen = new Set();
		feed.querySelectorAll('a[href*="/maps/place/"]').forEach((a) => {
			const href = a.href || '';
			if (!href || seen.has(href)) return;
			seen.add(href);
			hrefs.push(href);
		});
		return hrefs;
	})()`, &links))
	if err != nil {
		return nil, fmt.Errorf("extract feed links: %w", err)
	}
	return links, nil
}

func isFeedEnded(ctx context.Context) (bool, error) {
	var ended bool
	err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
		const feed = document.querySelector('div[role="feed"]');
		if (!feed) return false;
		const text = (feed.innerText || '').toLowerCase();
		return text.includes('você chegou ao final da lista')
			|| text.includes('voce chegou ao final da lista')
			|| text.includes("you've reached the end of the list")
			|| text.includes('end of the list');
	})()`, &ended))
	return ended, err
}

func feedAnchorCount(ctx context.Context) int {
	var count int
	_ = chromedp.Run(ctx, chromedp.Evaluate(`(() => {
		const feed = document.querySelector('div[role="feed"]');
		if (!feed) return 0;
		return feed.querySelectorAll('a[href*="/maps/place/"]').length;
	})()`, &count))
	return count
}

func waitForNewFeedLinks(ctx context.Context, previousCount int, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if feedAnchorCount(ctx) > previousCount {
			return nil
		}
		_ = scrollFeed(ctx)
		time.Sleep(350 * time.Millisecond)
	}
	return nil
}

func openAndExtractPlace(ctx context.Context, placeURL string) (*model.Place, error) {
	if err := chromedp.Run(ctx,
		chromedp.Navigate(placeURL),
		chromedp.WaitReady("body", chromedp.ByQuery),
	); err != nil {
		return nil, err
	}

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var state map[string]any
		_ = chromedp.Run(ctx, chromedp.Evaluate(`(() => {
			const h1 = document.querySelector('h1');
			const href = location.href || '';
			const hasName = !!(h1 && (h1.textContent || '').trim());
			const hasCoords = /!3d-?\d+\.\d+!4d-?\d+\.\d+/.test(href)
				|| /!2d-?\d+\.\d+!3d-?\d+\.\d+/.test(href)
				|| /@-?\d+\.\d+,-?\d+\.\d+/.test(href);
			return { hasName, hasCoords, href };
		})()`, &state))
		if asBool(state["hasName"]) && asBool(state["hasCoords"]) {
			break
		}
		if asBool(state["hasName"]) && time.Now().After(deadline.Add(-2*time.Second)) {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}

	place, err := extractPlaceDetails(ctx)
	if err != nil || place == nil {
		return place, err
	}
	// Feed links often already contain !3dLAT!4dLNG even before redirect settles.
	enrichFromURL(place, placeURL)
	return place, nil
}

func asBool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	default:
		return false
	}
}

func extractPlaceDetails(ctx context.Context) (*model.Place, error) {
	var raw map[string]any
	err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
		const h1 = document.querySelector('h1');
		const name = (h1 && h1.textContent || '').trim();
		if (!name) return null;

		const textOf = (el) => {
			if (!el) return '';
			return (el.getAttribute('aria-label') || el.textContent || '').trim();
		};
		const cleanLabel = (value, prefixes) => {
			let v = (value || '').trim();
			for (const p of prefixes) {
				const re = new RegExp('^' + p + '\\s*', 'i');
				v = v.replace(re, '');
			}
			return v.trim();
		};

		let address = '';
		const addressBtn = document.querySelector('button[data-item-id="address"], button[data-item-id*="address"]');
		address = cleanLabel(textOf(addressBtn), ['Endereço:', 'Address:']);

		let phone = '';
		const phoneBtn = document.querySelector('button[data-item-id^="phone:"], button[data-item-id*="phone"]');
		phone = cleanLabel(textOf(phoneBtn), ['Telefone:', 'Phone:']);

		let website = '';
		const websiteLink = document.querySelector('a[data-item-id="authority"], a[data-item-id*="authority"]');
		if (websiteLink) website = websiteLink.href || '';

		let email = '';
		const mail = document.querySelector('a[href^="mailto:"]');
		if (mail) email = (mail.getAttribute('href') || '').replace(/^mailto:/i, '').split('?')[0].trim();

		let category = '';
		const categoryBtn = document.querySelector('button[jsaction*="pane.rating.category"], button[jsaction*="category"]');
		category = (categoryBtn && categoryBtn.textContent || '').trim();

		let rating = 0;
		const ratingEl = document.querySelector('[role="img"][aria-label*="estrela"], [role="img"][aria-label*="star"]');
		const ratingText = textOf(ratingEl);
		const ratingMatch = ratingText.match(/([0-9]+[.,][0-9]+)/);
		if (ratingMatch) rating = parseFloat(ratingMatch[1].replace(',', '.'));

		let photoUrl = '';
		const heroImg = document.querySelector('button[jsaction*="heroHeaderImage"] img, [role="main"] img');
		if (heroImg) photoUrl = heroImg.src || heroImg.getAttribute('src') || '';

		const href = location.href || '';
		let id = '';
		const chij = href.match(/(ChIJ[\w-]+)/);
		if (chij) id = chij[1];
		if (!id) {
			const hex = href.match(/!1s(0x[0-9a-fA-F]+:0x[0-9a-fA-F]+)/);
			if (hex) id = hex[1];
		}

		let lat = 0;
		let lng = 0;
		// Place coordinates are usually encoded as !3dLAT!4dLNG (preferred).
		let coords = href.match(/!3d(-?\d+\.\d+)!4d(-?\d+\.\d+)/);
		if (coords) {
			lat = parseFloat(coords[1]);
			lng = parseFloat(coords[2]);
		} else {
			// Sometimes appears as !2dLNG!3dLAT.
			coords = href.match(/!2d(-?\d+\.\d+)!3d(-?\d+\.\d+)/);
			if (coords) {
				lng = parseFloat(coords[1]);
				lat = parseFloat(coords[2]);
			} else {
				coords = href.match(/@(-?\d+\.\d+),(-?\d+\.\d+)/);
				if (coords) {
					lat = parseFloat(coords[1]);
					lng = parseFloat(coords[2]);
				}
			}
		}

		return {
			id,
			name,
			phone,
			email,
			address,
			lat,
			lng,
			website,
			category,
			rating,
			photoUrl,
			href,
		};
	})()`, &raw))
	if err != nil {
		return nil, fmt.Errorf("extract place details: %w", err)
	}
	if raw == nil {
		return nil, nil
	}

	place := mapRawPlace(raw)
	enrichFromURL(&place, fmt.Sprint(raw["href"]))
	return &place, nil
}

func mapRawPlace(raw map[string]any) model.Place {
	return model.Place{
		ID:       asString(raw["id"]),
		Name:     asString(raw["name"]),
		Phone:    asString(raw["phone"]),
		Email:    asString(raw["email"]),
		Address:  asString(raw["address"]),
		Website:  asString(raw["website"]),
		Category: asString(raw["category"]),
		PhotoURL: asString(raw["photoUrl"]),
		Lat:      asFloat(raw["lat"]),
		Lng:      asFloat(raw["lng"]),
		Rating:   asFloat(raw["rating"]),
	}
}

func enrichFromURL(place *model.Place, href string) {
	if place.ID == "" {
		if m := rePlaceIDCh.FindStringSubmatch(href); len(m) > 1 {
			place.ID = m[1]
		} else if m := rePlaceIDHex.FindStringSubmatch(href); len(m) > 1 {
			place.ID = m[1]
		}
	}
	if place.Lat == 0 && place.Lng == 0 {
		switch {
		case reMapsPlace3d4d.MatchString(href):
			m := reMapsPlace3d4d.FindStringSubmatch(href)
			place.Lat, _ = strconv.ParseFloat(m[1], 64)
			place.Lng, _ = strconv.ParseFloat(m[2], 64)
		case reMapsPlace2d3d.MatchString(href):
			m := reMapsPlace2d3d.FindStringSubmatch(href)
			place.Lng, _ = strconv.ParseFloat(m[1], 64)
			place.Lat, _ = strconv.ParseFloat(m[2], 64)
		case reMapsCoords.MatchString(href):
			m := reMapsCoords.FindStringSubmatch(href)
			place.Lat, _ = strconv.ParseFloat(m[1], 64)
			place.Lng, _ = strconv.ParseFloat(m[2], 64)
		}
	}
}

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case nil:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprint(t))
	}
}

func asFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case string:
		f, _ := strconv.ParseFloat(strings.Replace(t, ",", ".", 1), 64)
		return f
	default:
		return 0
	}
}

func acceptConsent(ctx context.Context) error {
	consentCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	var clicked bool
	err := chromedp.Run(consentCtx, chromedp.Evaluate(`(() => {
		const labels = [
			'Aceitar tudo',
			'Accept all',
			'Aceitar',
			'Accept',
			'Concordo',
			'I agree',
			'Reject all',
			'Rejeitar tudo',
		];
		const buttons = Array.from(document.querySelectorAll('button, [role="button"]'));
		for (const btn of buttons) {
			const text = (btn.getAttribute('aria-label') || btn.textContent || '').trim();
			if (!text) continue;
			if (labels.some((l) => text.toLowerCase().includes(l.toLowerCase()))) {
				btn.click();
				return true;
			}
		}
		for (const frame of Array.from(document.querySelectorAll('iframe'))) {
			try {
				const doc = frame.contentDocument;
				if (!doc) continue;
				const frameButtons = Array.from(doc.querySelectorAll('button, [role="button"]'));
				for (const btn of frameButtons) {
					const text = (btn.getAttribute('aria-label') || btn.textContent || '').trim();
					if (!text) continue;
					if (labels.some((l) => text.toLowerCase().includes(l.toLowerCase()))) {
						btn.click();
						return true;
					}
				}
			} catch (_) {}
		}
		return false;
	})()`, &clicked))
	if err != nil {
		return err
	}
	if clicked {
		time.Sleep(1200 * time.Millisecond)
	}
	return nil
}

func isBlocked(ctx context.Context) (bool, string) {
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var reason string
	_ = chromedp.Run(checkCtx, chromedp.Evaluate(`(() => {
		const bodyText = (document.body && document.body.innerText || '').toLowerCase();
		const html = (document.documentElement && document.documentElement.innerHTML || '').toLowerCase();
		if (document.querySelector('#captcha-form, #recaptcha, iframe[src*="recaptcha"], div.g-recaptcha')) {
			return 'captcha';
		}
		if (bodyText.includes('unusual traffic') || bodyText.includes('tráfego incomum') || bodyText.includes('not a robot')) {
			return 'captcha';
		}
		if (bodyText.includes('sorry') && bodyText.includes('automated')) {
			return 'bot_detection';
		}
		if (html.includes('/sorry/') || location.href.includes('/sorry/')) {
			return 'bot_detection';
		}
		return '';
	})()`, &reason))
	if reason == "" {
		return false, ""
	}
	return true, reason
}

func waitForResults(ctx context.Context) (string, error) {
	waitCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		if waitCtx.Err() != nil {
			return "", waitCtx.Err()
		}

		var mode string
		err := chromedp.Run(waitCtx, chromedp.Evaluate(`(() => {
			if (document.querySelector('div[role="feed"]')) return 'feed';
			if (document.querySelector('h1') && document.querySelector('button[data-item-id="address"], button[data-item-id*="address"], [data-item-id="address"]')) {
				return 'place';
			}
			if (document.querySelector('a[href*="/maps/place/"]') && !document.querySelector('div[role="feed"]')) {
				const h1 = document.querySelector('h1');
				if (h1 && (h1.textContent || '').trim().length > 0) return 'place';
			}
			return '';
		})()`, &mode))
		if err != nil {
			return "", err
		}
		if mode == "feed" || mode == "place" {
			return mode, nil
		}

		if blocked, reason := isBlocked(waitCtx); blocked {
			return "", fmt.Errorf("blocked: %s", reason)
		}

		time.Sleep(500 * time.Millisecond)
	}

	return "", context.DeadlineExceeded
}

func scrollFeed(ctx context.Context) error {
	return chromedp.Run(ctx, chromedp.Evaluate(`(() => {
		const feed = document.querySelector('div[role="feed"]');
		if (!feed) return false;

		const articles = feed.querySelectorAll(':scope > div > div, div[jsaction*="mouseover"]');
		const last = articles.length ? articles[articles.length - 1] : null;
		if (last) {
			last.scrollIntoView({ block: 'end', inline: 'nearest' });
		}

		feed.scrollTop = feed.scrollHeight;
		feed.dispatchEvent(new WheelEvent('wheel', {
			deltaY: 1200,
			bubbles: true,
			cancelable: true,
		}));
		feed.dispatchEvent(new Event('scroll', { bubbles: true }));
		return true;
	})()`, nil))
}

func saveDebugArtifacts(ctx context.Context, reason string) string {
	dir := filepath.Join("tmp", "debug")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Sprintf("mkdir failed: %v", err)
	}

	stamp := time.Now().Format("20060102_150405")
	base := fmt.Sprintf("maps_%s_%s", stamp, sanitizeFilePart(reason))
	pngPath := filepath.Join(dir, base+".png")
	htmlPath := filepath.Join(dir, base+".html")

	if ctx.Err() != nil {
		return fmt.Sprintf("capture skipped (context done: %v); intended paths %s , %s", ctx.Err(), pngPath, htmlPath)
	}

	captureCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	var buf []byte
	var html string
	if err := chromedp.Run(captureCtx,
		chromedp.CaptureScreenshot(&buf),
		chromedp.OuterHTML("html", &html, chromedp.ByQuery),
	); err != nil {
		return fmt.Sprintf("capture failed (%v); intended paths %s , %s", err, pngPath, htmlPath)
	}

	var parts []string
	if len(buf) > 0 {
		if err := os.WriteFile(pngPath, buf, 0o644); err != nil {
			parts = append(parts, fmt.Sprintf("png error: %v", err))
		} else {
			parts = append(parts, pngPath)
		}
	}
	if html != "" {
		if err := os.WriteFile(htmlPath, []byte(html), 0o644); err != nil {
			parts = append(parts, fmt.Sprintf("html error: %v", err))
		} else {
			parts = append(parts, htmlPath)
		}
	}
	if len(parts) == 0 {
		return "no artifacts saved"
	}
	return strings.Join(parts, ", ")
}

func sanitizeFilePart(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, s)
	if s == "" {
		return "debug"
	}
	return s
}
