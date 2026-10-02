// Package dto holds the catalog service's response shapes: the public
// listing DTOs (built from an allowlist, never by stripping an internal
// model — PRD §6) and the admin detail view.
package dto

import (
	"time"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// PublicListing is GET /v1/public/listing.
type PublicListing struct {
	ShortID     string             `json:"shortId"`
	Slug        string             `json:"slug"`
	Name        string             `json:"name"`
	Description string             `json:"description,omitempty"`
	Address     PublicAddress      `json:"address"`
	Loc         *PublicLoc         `json:"loc,omitempty"`
	TotalArea   *models.Area       `json:"totalArea,omitempty"`
	Rate        *domain.PublicRate `json:"rate,omitempty"`
	Attributes  []PublicNode       `json:"attributes"`
	Industries  []string           `json:"industries"`
	Media       []PublicMedia      `json:"media"`
	SEO         PublicSEO          `json:"seo"`
	UpdatedAt   time.Time          `json:"updatedAt"`
}

// PublicAddress omits the street lines.
type PublicAddress struct {
	Locality   string `json:"locality,omitempty"`
	City       string `json:"city,omitempty"`
	Region     string `json:"region,omitempty"`
	PostalCode string `json:"postalCode,omitempty"`
	Country    string `json:"country,omitempty"`
}

// PublicLoc is the pin rounded to 4 dp (~11 m).
type PublicLoc struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

// PublicNode is a public yes node with its public, non-null field values,
// nested by tree.
type PublicNode struct {
	Key      string        `json:"key"`
	Name     string        `json:"name"`
	Fields   []PublicField `json:"fields"`
	Children []PublicNode  `json:"children"`
}

// PublicField is one public value. Labels resolve pick/multi option keys.
type PublicField struct {
	Key    string   `json:"key"`
	Name   string   `json:"name"`
	Type   string   `json:"type"`
	Value  any      `json:"value"`
	Unit   string   `json:"unit,omitempty"`
	Labels []string `json:"labels,omitempty"`
}

// PublicMedia is a public photo or doc.
type PublicMedia struct {
	Kind    string `json:"kind"`
	DocType string `json:"docType,omitempty"`
	URL     string `json:"url"`
	Caption string `json:"caption,omitempty"`
	IsCover bool   `json:"isCover"`
}

// PublicSEO feeds the page head.
type PublicSEO struct {
	Title           string         `json:"title"`
	MetaDescription string         `json:"metaDescription"`
	CanonicalPath   string         `json:"canonicalPath"`
	OGImage         string         `json:"ogImage,omitempty"`
	JSONLD          map[string]any `json:"jsonLd"`
}

// SlugItem is one entry of the public slugs / sitemap feeds.
type SlugItem struct {
	Slug      string    `json:"slug"`
	UpdatedAt time.Time `json:"updatedAt"`
	CoverURL  string    `json:"coverUrl,omitempty"`
}

// WarehouseDetail is the admin detail view: the warehouse, its open revision
// (with the evaluator preview), its media and its revision history.
type WarehouseDetail struct {
	Warehouse    *models.Warehouse          `json:"warehouse"`
	OpenRevision *models.WarehouseRevision  `json:"openRevision"`
	Preview      *Preview                   `json:"preview,omitempty"`
	Media        []models.WarehouseMedia    `json:"media"`
	History      []models.WarehouseRevision `json:"history"`
}

// Preview is the evaluator output returned with a draft.
type Preview struct {
	State     map[string]models.NodeStatus `json:"state"`
	Ratios    map[string]float64           `json:"ratios"`
	NeedsInfo []string                     `json:"needsInfo"`
	Fit       map[string]domain.Verdict    `json:"fit"`
	// SubmitProblems is what a submit would reject right now.
	SubmitProblems []string `json:"submitProblems"`
}
