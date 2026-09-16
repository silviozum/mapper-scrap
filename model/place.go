package model

type Place struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Phone    string  `json:"phone,omitempty"`
	Email    string  `json:"email,omitempty"`
	Address  string  `json:"address,omitempty"`
	Lat      float64 `json:"lat,omitempty"`
	Lng      float64 `json:"lng,omitempty"`
	Website  string  `json:"website,omitempty"`
	Category string  `json:"category,omitempty"`
	Rating   float64 `json:"rating,omitempty"`
	PhotoURL string  `json:"photoUrl,omitempty"`
}

type MapperResponse struct {
	City   string  `json:"city"`
	Count  int     `json:"count"`
	Places []Place `json:"places"`
}
