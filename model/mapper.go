package model

import (
	"errors"
	"strings"
)

const (
	DefaultLimit = 100
	MaxLimit     = 100
)

type MapperRequest struct {
	City    string `json:"city"`
	Segment string `json:"segment"`
	Limit   int    `json:"limit"`
}

func (r *MapperRequest) Validate() error {
	r.City = strings.TrimSpace(r.City)
	r.Segment = strings.TrimSpace(r.Segment)

	if r.City == "" {
		return errors.New("city is required")
	}
	if r.Segment == "" {
		return errors.New("segment is required")
	}

	if r.Limit <= 0 {
		r.Limit = DefaultLimit
	}
	if r.Limit > MaxLimit {
		return errors.New("limit must be <= 100")
	}

	return nil
}
