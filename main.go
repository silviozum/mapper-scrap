package main

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"mapper/handler"
	"mapper/scraper"
	"mapper/service"
)

func main() {
	mapsScraper := scraper.NewGoogleMaps()
	mapperService := service.NewMapperService(mapsScraper)
	mapperHandler := handler.NewMapperHandler(mapperService)

	mux := http.NewServeMux()
	mux.HandleFunc("/mapper/", mapperHandler.Create)

	server := &http.Server{
		Addr:              ":8080",
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		// Disable write deadline; scrape timeout is controlled by request context.
		WriteTimeout: 0,
	}

	fmt.Println("Server listening on :8080")
	log.Fatal(server.ListenAndServe())
}
