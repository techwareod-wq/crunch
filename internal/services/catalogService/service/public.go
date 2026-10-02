package service

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/catalogService"
	"github.com/atharva-ng/crunch/internal/services/catalogService/dto"
	"github.com/atharva-ng/crunch/internal/services/catalogService/store"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// Public listing DTOs (spec 03). Built from an allowlist, never by stripping
// an internal model (PRD §6). Never included: operator_company and any other
// non-public node/field, rentAdmin, staff media, unknown nodes, null fields,
// needsInfo.

// root fields the listing shows top-level (not repeated under attributes).
var hoistedRootFields = []string{fieldName, fieldDescription, fieldAddress, fieldLocation, fieldTotalArea, fieldRent}

const listingPathPrefix = "/warehouses/"

// Lookup resolves a public slug: 301 to the current slug, 410 + nearby for
// an archived listing, 404 for unpublished / unknown.
func (p *publicSite) Lookup(ctx context.Context, slug string) (catalogService.LookupResult, error) {
	sid, ok := shortIDFromSlug(slug)
	if !ok {
		return catalogService.LookupResult{}, catalogService.ErrNotFound
	}
	w, err := p.store.GetWarehouseByShortID(ctx, sid)
	if err != nil {
		return catalogService.LookupResult{}, err
	}
	switch w.Status {
	case models.WarehouseLive:
	case models.WarehouseArchived:
		res := catalogService.LookupResult{IsGone: true, Nearby: []domain.ListingCard{}}
		if eng := p.search(); eng != nil && w.Loc != nil {
			cards, err := eng.Nearest(ctx, *w.Loc, 6, w.ID)
			if err != nil {
				p.logWarn("catalog: nearest for 410 failed", err)
			} else {
				res.Nearby = cards
			}
		}
		return res, nil
	default:
		return catalogService.LookupResult{}, catalogService.ErrNotFound
	}
	if w.Live == nil {
		return catalogService.LookupResult{}, catalogService.ErrNotFound
	}
	if slug != w.Slug {
		return catalogService.LookupResult{RedirectTo: listingPathPrefix + w.Slug}, nil
	}
	media, err := p.store.ListMedia(ctx, w.ID)
	if err != nil {
		return catalogService.LookupResult{}, err
	}
	l := BuildPublicListing(p.rules.Snapshot(), w, media, p.mediaBase(), p.siteBase())
	return catalogService.LookupResult{Listing: &l}, nil
}

// BuildPublicListing assembles the allowlisted DTO from a live warehouse.
func BuildPublicListing(snap *domain.Snapshot, w *models.Warehouse, media []models.WarehouseMedia, mediaBase, siteBase string) dto.PublicListing {
	c := w.Live
	a := c.Attributes
	l := dto.PublicListing{
		ShortID: w.ShortID, Slug: w.Slug, UpdatedAt: w.UpdatedAt,
		Attributes: []dto.PublicNode{}, Industries: []string{}, Media: []dto.PublicMedia{},
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
	if addr, ok := rootValue[models.Address](a, fieldAddress); ok && public(fieldAddress) {
		l.Address = dto.PublicAddress{Locality: addr.Locality, City: addr.City, Region: addr.Region, PostalCode: addr.PostalCode, Country: addr.Country}
	}
	if loc, ok := rootValue[models.Location](a, fieldLocation); ok && public(fieldLocation) {
		l.Loc = &dto.PublicLoc{Lat: round(loc.Lat, 4), Lng: round(loc.Lng, 4)}
	}
	if area, ok := rootValue[models.Area](a, fieldTotalArea); ok && public(fieldTotalArea) {
		area.Sqm = round(area.Sqm, 2)
		l.TotalArea = &area
	}
	if rent, ok := rootValue[models.Money](a, fieldRent); ok && public(fieldRent) {
		l.Rate = publicRate(rent, w.Price)
	}

	// Attributes: public yes nodes, nested.
	res := domain.Evaluate(snap, a, w.UpdatedAt)
	var build func(key string) []dto.PublicNode
	build = func(parent string) []dto.PublicNode {
		out := []dto.PublicNode{}
		for _, k := range snap.Children(parent) {
			n, _ := snap.Node(k)
			if !n.Public || res.State[k] != domain.StatusYes {
				continue
			}
			pn := dto.PublicNode{Key: n.Key, Name: n.Name, Fields: publicFields(n, a, res), Children: build(k)}
			out = append(out, pn)
		}
		return out
	}
	if rootNode != nil {
		if extra := publicFields(rootNode, a, res); len(extra) > 0 {
			l.Attributes = append(l.Attributes, dto.PublicNode{Key: rootNode.Key, Name: rootNode.Name, Fields: extra, Children: []dto.PublicNode{}})
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
	byID := map[string]*models.WarehouseMedia{}
	for i := range media {
		byID[media[i].ID.Hex()] = &media[i]
	}
	for _, ref := range c.Media {
		m, ok := byID[ref.MediaID.Hex()]
		if !ok || m.Status != models.MediaReady || m.Visibility != models.VisibilityPublic {
			continue
		}
		l.Media = append(l.Media, dto.PublicMedia{Kind: m.Kind, DocType: m.DocType, URL: publicURL(mediaBase, m.Key), Caption: ref.Caption, IsCover: ref.IsCover})
	}
	slices.SortStableFunc(l.Media, func(x, y dto.PublicMedia) int {
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
func publicFields(n *models.AttributeNode, a models.Attributes, res domain.Result) []dto.PublicField {
	out := []dto.PublicField{}
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
			v = models.JSONValue(fv.V)
		}
		pf := dto.PublicField{Key: f.Key, Name: f.Name, Type: string(f.Type), Value: v}
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

func publicRate(m models.Money, price *models.Price) *domain.PublicRate {
	r := &domain.PublicRate{Amount: m.Amount, Currency: m.Currency, Basis: m.Basis, OnRequest: m.OnRequest}
	if m.OnRequest {
		r.Amount, r.Basis = 0, ""
	}
	if price != nil {
		r.PerSqmMonth, r.Approx = price.PerSqmMonth, price.Approx
	}
	return r
}

func buildSEO(l dto.PublicListing, siteBase string) dto.PublicSEO {
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
	seo := dto.PublicSEO{Title: title, MetaDescription: desc, CanonicalPath: path}
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
func (p *publicSite) Slugs(ctx context.Context, page int, withCover bool) ([]dto.SlugItem, error) {
	size := p.cfg().PublicPageSize
	if size <= 0 {
		size = 1000
	}
	ws, err := p.store.ListLive(ctx, page, size)
	if err != nil {
		return nil, err
	}
	out := make([]dto.SlugItem, 0, len(ws))
	for _, w := range ws {
		it := dto.SlugItem{Slug: w.Slug, UpdatedAt: w.UpdatedAt}
		if withCover {
			it.CoverURL = publicURL(p.mediaBase(), w.CoverKey)
		}
		out = append(out, it)
	}
	return out, nil
}

// Public serves the unauthenticated listing API.
type publicSite struct {
	store     store.Store
	rules     domain.Rules
	search    func() domain.SearchEngine
	mediaBase func() string
	siteBase  func() string
	cfg       func() config.CatalogValues
	logWarn   func(msg string, err error)
}
