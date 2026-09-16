package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"mapper/model"
	"mapper/scraper"
	"mapper/service"
)

type MapperHandler struct {
	service *service.MapperService
}

func NewMapperHandler(svc *service.MapperService) *MapperHandler {
	return &MapperHandler{service: svc}
}

func (h *MapperHandler) Create(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var payload model.MapperRequest
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	if err := payload.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	timeout := scraper.TimeoutFor(payload.Limit)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), timeout)
	defer cancel()

	result, err := h.service.Process(ctx, payload)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(result)
}
