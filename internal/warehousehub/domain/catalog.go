package domain

import (
	"context"
	"github.com/atharva-ng/crunch/internal/models"
	"math"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Catalog rules and DTOs shared across modules (spec 03). The stored shapes
// (Warehouse, Revision, Media, WarehouseRent, Content…) are in internal/models.

// Rent bases (the root `rent` field's Money.Basis).
const (
	BasisPerSqftMonth = "per_sqft_month"
	BasisPerSqmMonth  = "per_sqm_month"
	BasisFlatMonth    = "flat_month"
)

// ValidBasis reports whether b is a rent basis.
func ValidBasis(b string) bool {
	return b == BasisPerSqftMonth || b == BasisPerSqmMonth || b == BasisFlatMonth
}

// NormalizePrice converts the headline rent (D-058, D-131): per sq ft × 10.7639,
// per sq m as is, flat ÷ total area (approx). ok is false when there is no
// usable price (unknown basis, or flat rent without a total area).
func NormalizePrice(m models.Money, totalSqm float64) (models.Price, bool) {
	p := models.Price{Currency: m.Currency, Basis: m.Basis}
	if m.OnRequest {
		p.OnRequest = true
		return p, true
	}
	amt := float64(m.Amount)
	switch m.Basis {
	case BasisPerSqmMonth:
		p.PerSqmMonth = amt
	case BasisPerSqftMonth:
		p.PerSqmMonth = amt / SqmPerSqft
	case BasisFlatMonth:
		if totalSqm <= 0 {
			return models.Price{}, false
		}
		p.PerSqmMonth, p.Approx = amt/totalSqm, true
	default:
		return models.Price{}, false
	}
	p.PerSqmMonth = math.Round(p.PerSqmMonth*100) / 100
	return p, true
}

// NewGeoPoint builds a GeoJSON point.
func NewGeoPoint(lat, lng float64) *models.GeoPoint {
	return &models.GeoPoint{Type: "Point", Coordinates: [2]float64{lng, lat}}
}

// ListingCard is the compact public listing used by the 410 page's nearby
// list (03) and search results (04).
type ListingCard struct {
	ShortID  string      `json:"shortId"`
	Slug     string      `json:"slug"`
	Name     string      `json:"name"`
	City     string      `json:"city,omitempty"`
	Locality string      `json:"locality,omitempty"`
	CoverURL string      `json:"coverUrl,omitempty"`
	TotalSqm float64     `json:"totalSqm"`
	Rate     *PublicRate `json:"rate,omitempty"`
}

// PublicRate is the public view of the headline rent.
type PublicRate struct {
	Amount      int64   `json:"amount"`
	Currency    string  `json:"currency"`
	Basis       string  `json:"basis,omitempty"`
	PerSqmMonth float64 `json:"perSqmMonth,omitempty"`
	Approx      bool    `json:"approx"`
	OnRequest   bool    `json:"onRequest"`
}

// SearchEngine is what the catalog needs from search (04): the nearest live
// listings for an archived listing's 410 page. Wired in cmd/service once
// search lands; nil means "no nearby list".
type SearchEngine interface {
	Nearest(ctx context.Context, loc models.GeoPoint, limit int, exclude primitive.ObjectID) ([]ListingCard, error)
}
