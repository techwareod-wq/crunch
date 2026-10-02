package catalog

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// Public listing DTOs (spec 03). Built from an allowlist, never by stripping
// an internal model (PRD §6). Never included: operator_company and any other
// non-public node/field, rentAdmin, staff media, unknown nodes, null fields,
// needsInfo.

// PublicListing is GET /v1/public/listing.
type PublicListing struct {
	ShortID     string             `json:"shortId"`
	Slug        string             `json:"slug"`
	Name        string             `json:"name"`
	Description string             `json:"description,omitempty"`
	Address     PublicAddress      `json:"address"`
	Loc         *PublicLoc         `json:"loc,omitempty"`
	TotalArea   *domain.Area       `json:"totalArea,omitempty"`
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

// root fields the listing shows top-level (not repeated under attributes).
var hoistedRootFields = []string{fieldName, fieldDescription, fieldAddress, fieldLocation, fieldTotalArea, fieldRent}

const listingPathPrefix = "/warehouses/"

// lookupResult is the public lookup outcome (spec 03 Slugs).
type lookupResult struct {
	Listing    *PublicListing
	RedirectTo string               // 301
	Gone       []domain.ListingCard // 410 (nearby)
	IsGone     bool
}

// Lookup resolves a public slug: 301 to the current slug, 410 + nearby for
// an archived listing, 404 for unpublished / unknown.
func (p *Public) Lookup(ctx context.Context, slug string) (lookupResult, error) {
	sid, ok := shortIDFromSlug(slug)
	if !ok {
		return lookupResult{}, errNotFound
	}
	w, err := p.store.GetWarehouseByShortID(ctx, sid)
	if err != nil {
		return lookupResult{}, err
	}
	switch w.Status {
	case domain.WarehouseLive:
	case domain.WarehouseArchived:
		res := lookupResult{IsGone: true, Gone: []domain.ListingCard{}}
		if eng := p.search(); eng != nil && w.Loc != nil {
			cards, err := eng.Nearest(ctx, *w.Loc, 6, w.ID)
			if err != nil {
				p.logWarn("catalog: nearest for 410 failed", err)
			} else {
				res.Gone = cards
			}
		}
		return res, nil
	default:
		return lookupResult{}, errNotFound
	}
	if w.Live == nil {
		return lookupResult{}, errNotFound
	}
	if slug != w.Slug {
		return lookupResult{RedirectTo: listingPathPrefix + w.Slug}, nil
	}
	media, err := p.store.ListMedia(ctx, w.ID)
	if err != nil {
		return lookupResult{}, err
	}
	l := BuildPublicListing(p.rules.Snapshot(), w, media, p.mediaBase(), p.siteBase())
	return lookupResult{Listing: &l}, nil
}

// BuildPublicListing assembles the allowlisted DTO from a live warehouse.
func BuildPublicListing(snap *domain.Snapshot, w *domain.Warehouse, media []domain.Media, mediaBase, siteBase string) PublicListing {
	c := w.Live
	a := c.Attributes
	l := PublicListing{
		ShortID: w.ShortID, Slug: w.Slug, UpdatedAt: w.UpdatedAt,
		Attributes: []PublicNode{}, Industries: []string{}, Media: []PublicMedia{},
	}
	rootNode, _ := snap.Node(domain.RootKey)
	public := func(field string) bool {
		if rootNode == nil {
			return false
		}
		f, ok := rootNode.Field(field)
		return ok && f.Public
	}
	if public(fieldName) {
		l.Name, _ = rootValue[string](a, fieldName)
	}
	if public(fieldDescription) {
		l.Description, _ = rootValue[string](a, fieldDescription)
	}
	if addr, ok := rootValue[domain.Address](a, fieldAddress); ok && public(fieldAddress) {
		l.Address = PublicAddress{Locality: addr.Locality, City: addr.City, Region: addr.Region, PostalCode: addr.PostalCode, Country: addr.Country}
	}
	if loc, ok := rootValue[domain.Location](a, fieldLocation); ok && public(fieldLocation) {
		l.Loc = &PublicLoc{Lat: round(loc.Lat, 4), Lng: round(loc.Lng, 4)}
	}
	if area, ok := rootValue[domain.Area](a, fieldTotalArea); ok && public(fieldTotalArea) {
		area.Sqm = round(area.Sqm, 2)
		l.TotalArea = &area
	}
	if rent, ok := rootValue[domain.Money](a, fieldRent); ok && public(fieldRent) {
		l.Rate = publicRate(rent, w.Price)
	}

	// Attributes: public yes nodes, nested.
	res := domain.Evaluate(snap, a, w.UpdatedAt)
	var build func(key string) []PublicNode
	build = func(parent string) []PublicNode {
		out := []PublicNode{}
		for _, k := range snap.Children(parent) {
			n, _ := snap.Node(k)
			if !n.Public || res.State[k] != domain.StatusYes {
				continue
			}
			pn := PublicNode{Key: n.Key, Name: n.Name, Fields: publicFields(n, a, res), Children: build(k)}
			out = append(out, pn)
		}
		return out
	}
	if rootNode != nil {
		if extra := publicFields(rootNode, a, res); len(extra) > 0 {
			l.Attributes = append(l.Attributes, PublicNode{Key: rootNode.Key, Name: rootNode.Name, Fields: extra, Children: []PublicNode{}})
		}
		l.Attributes = append(l.Attributes, build(domain.RootKey)...)
	}

	// Industries: Fit, or Partial shown as fit (D-034).
	for _, ind := range snap.Industries {
		if v := res.Fit[ind.Key]; v == domain.VerdictFit || v == domain.VerdictPartial {
			l.Industries = append(l.Industries, ind.Key)
		}
	}

	// Media: public only, cover first.
	byID := map[string]*domain.Media{}
	for i := range media {
		byID[media[i].ID.Hex()] = &media[i]
	}
	for _, ref := range c.Media {
		m, ok := byID[ref.MediaID.Hex()]
		if !ok || m.Status != domain.MediaReady || m.Visibility != domain.VisibilityPublic {
			continue
		}
		l.Media = append(l.Media, PublicMedia{Kind: m.Kind, DocType: m.DocType, URL: publicURL(mediaBase, m.Key), Caption: ref.Caption, IsCover: ref.IsCover})
	}
	slices.SortStableFunc(l.Media, func(x, y PublicMedia) int {
		switch {
		case x.IsCover == y.IsCover:
			return 0
		case x.IsCover:
			return -1
		}
		return 1
	})

	l.SEO = buildSEO(l, siteBase)
	return l
}

// publicFields lists a yes node's public, non-null values (root: minus the
// hoisted ones).
func publicFields(n *domain.Node, a domain.Attributes, res domain.Result) []PublicField {
	out := []PublicField{}
	for i := range n.Fields {
		f := &n.Fields[i]
		if !f.Public || (n.Key == domain.RootKey && slices.Contains(hoistedRootFields, f.Key)) {
			continue
		}
		var v any
		if f.Type == domain.TypeRatio {
			x, ok := res.Ratios[n.Key+"."+f.Key]
			if !ok {
				continue
			}
			v = round(x, 2)
		} else {
			fv := a.Value(n.Key, f.Key)
			if fv == nil || fv.V == nil {
				continue
			}
			v = domain.JSONValue(fv.V)
		}
		pf := PublicField{Key: f.Key, Name: f.Name, Type: string(f.Type), Value: v}
		if f.Unit != nil {
			pf.Unit = domain.CanonicalUnit(f.Unit.Family)
		}
		if f.Type == domain.TypePick || f.Type == domain.TypeMulti {
			keys, _ := domain.AsStrings(v)
			if s, ok := v.(string); ok {
				keys = []string{s}
			}
			for _, k := range keys {
				for _, o := range f.Options {
					if o.Key == k {
						pf.Labels = append(pf.Labels, o.Label)
					}
				}
			}
		}
		out = append(out, pf)
	}
	return out
}

func publicRate(m domain.Money, price *domain.Price) *domain.PublicRate {
	r := &domain.PublicRate{Amount: m.Amount, Currency: m.Currency, Basis: m.Basis, OnRequest: m.OnRequest}
	if m.OnRequest {
		r.Amount, r.Basis = 0, ""
	}
	if price != nil {
		r.PerSqmMonth, r.Approx = price.PerSqmMonth, price.Approx
	}
	return r
}

func buildSEO(l PublicListing, siteBase string) PublicSEO {
	place := strings.Join(nonEmpty(l.Address.Locality, l.Address.City), ", ")
	title := l.Name
	if place != "" {
		title = fmt.Sprintf("%s — warehouse in %s", l.Name, place)
	}
	desc := l.Description
	if desc == "" {
		desc = title
		if l.TotalArea != nil {
			desc = fmt.Sprintf("%s. %s sq m of warehouse space.", title, trimFloat(l.TotalArea.Sqm))
		}
	}
	desc = truncateRunes(strings.Join(strings.Fields(desc), " "), 155)
	path := listingPathPrefix + l.Slug
	seo := PublicSEO{Title: title, MetaDescription: desc, CanonicalPath: path}
	for _, m := range l.Media {
		if m.IsCover {
			seo.OGImage = m.URL
		}
	}
	ld := map[string]any{
		"@context":    "https://schema.org",
		"@type":       "Place",
		"name":        l.Name,
		"description": desc,
		"address": withoutEmpty(map[string]any{
			"@type": "PostalAddress", "addressLocality": l.Address.City, "addressRegion": l.Address.Region,
			"postalCode": l.Address.PostalCode, "addressCountry": l.Address.Country,
		}),
	}
	if l.Loc != nil {
		ld["geo"] = map[string]any{"@type": "GeoCoordinates", "latitude": l.Loc.Lat, "longitude": l.Loc.Lng}
	}
	if seo.OGImage != "" {
		ld["image"] = seo.OGImage
	}
	if siteBase != "" {
		ld["url"] = strings.TrimRight(siteBase, "/") + path
	}
	seo.JSONLD = ld
	return seo
}

func withoutEmpty(m map[string]any) map[string]any {
	for k, v := range m {
		if v == "" {
			delete(m, k)
		}
	}
	return m
}

func nonEmpty(ss ...string) []string {
	return slices.DeleteFunc(ss, func(s string) bool { return s == "" })
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n-1])) + "…"
}

func trimFloat(v float64) string {
	if v == math.Trunc(v) {
		return fmt.Sprintf("%.0f", v)
	}
	return fmt.Sprintf("%.1f", v)
}

// Slugs pages the live listings for SSG / ISR and the sitemap.
func (p *Public) Slugs(ctx context.Context, page int, withCover bool) ([]SlugItem, error) {
	size := p.cfg().PublicPageSize
	if size <= 0 {
		size = 1000
	}
	ws, err := p.store.ListLive(ctx, page, size)
	if err != nil {
		return nil, err
	}
	out := make([]SlugItem, 0, len(ws))
	for _, w := range ws {
		it := SlugItem{Slug: w.Slug, UpdatedAt: w.UpdatedAt}
		if withCover {
			it.CoverURL = publicURL(p.mediaBase(), w.CoverKey)
		}
		out = append(out, it)
	}
	return out, nil
}

// Public serves the unauthenticated listing API.
type Public struct {
	store     Store
	rules     domain.Rules
	search    func() domain.SearchEngine
	mediaBase func() string
	siteBase  func() string
	cfg       func() config.CatalogValues
	logWarn   func(msg string, err error)
}
