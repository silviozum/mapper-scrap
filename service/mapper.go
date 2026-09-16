package service

import (
	"context"
	"fmt"

	"mapper/model"
	"mapper/scraper"
)

type MapsScraper interface {
	Search(ctx context.Context, city, segment string, limit int) ([]model.Place, error)
}

type MapperService struct {
	scraper MapsScraper
}

func NewMapperService(mapsScraper MapsScraper) *MapperService {
	if mapsScraper == nil {
		mapsScraper = scraper.NewGoogleMaps()
	}
	return &MapperService{scraper: mapsScraper}
}

func (s *MapperService) Process(ctx context.Context, payload model.MapperRequest) (*model.MapperResponse, error) {
	fmt.Printf("POST /mapper/ scraping city=%q segment=%q limit=%d\n",
		payload.City, payload.Segment, payload.Limit)

	places, err := s.scraper.Search(ctx, payload.City, payload.Segment, payload.Limit)
	if err != nil {
		return nil, err
	}

	return &model.MapperResponse{
		City:   payload.City,
		Count:  len(places),
		Places: places,
	}, nil
}
