package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"

	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/services/aiSearchService"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// extraction is the set_filters tool input.
type extraction struct {
	Location *struct {
		Text string `json:"text"`
		Kind string `json:"kind"`
	} `json:"location"`
	RadiusKm float64 `json:"radiusKm"`
	Area     *struct {
		Value  float64 `json:"value"`
		Unit   string  `json:"unit"`
		Intent string  `json:"intent"`
	} `json:"area"`
	Price *struct {
		Amount   float64 `json:"amount"`
		Currency string  `json:"currency"`
		Basis    string  `json:"basis"`
		Intent   string  `json:"intent"`
	} `json:"price"`
	Industries []string `json:"industries"`
	Chips      []string `json:"chips"`
	Ranges     []struct {
		Key  string   `json:"key"`
		Min  *float64 `json:"min"`
		Max  *float64 `json:"max"`
		Unit string   `json:"unit"`
	} `json:"ranges"`
	Sort       string   `json:"sort"`
	Unmapped   []string `json:"unmapped"`
	Confidence string   `json:"confidence"`
}

// extract asks the model for set_filters under the hard deadline. Any
// failure (timeout, API error, truncated or malformed tool call) is an
// error: the caller runs the basic search (D-088).
func (s *svc) extract(ctx context.Context, p prompt, q string, h hints) (*extraction, error) {
	if s.llm == nil {
		return nil, aiSearchService.ErrNoLLM
	}
	cfg := s.cfg()
	ctx, cancel := context.WithTimeout(ctx, millis(cfg.LLMDeadlineMillis, 2200))
	defer cancel()
	msgs := []dto.Message{{Role: dto.RoleSystem, Content: p.system, Cache: true}}
	if hs := h.String(); hs != "" {
		msgs = append(msgs, dto.Message{Role: dto.RoleSystem, Content: "Pre-parsed hints (check them against the query): " + hs})
	}
	msgs = append(msgs, dto.Message{Role: dto.RoleUser, Content: q})
	resp, err := s.llm.Prompt(ctx, dto.PromptRequest{
		Model: cfg.Model, MaxTokens: max(cfg.MaxTokens, 256), Messages: msgs, DisableThinking: true,
		Tools:     []dto.ToolDef{{Name: toolName, Description: "Set the warehouse search filters for the user's query.", InputSchema: p.schema}},
		ForceTool: toolName,
	})
	if err != nil {
		return nil, err
	}
	if resp.StopReason == dto.StopReasonMaxTokens {
		return nil, aiSearchService.ErrToolCallTruncated
	}
	if resp.ToolUse == nil || resp.ToolUse.Name != toolName {
		return nil, aiSearchService.ErrNoSetFilters
	}
	var ext extraction
	if err := json.Unmarshal(resp.ToolUse.Input, &ext); err != nil {
		return nil, fmt.Errorf("bad set_filters input: %w", err)
	}
	return &ext, nil
}

// built is the filters from a parse plus what the AI response reports.
type built struct {
	filters domain.SearchFilters
	notes   []string
	// mapped: the query produced at least one filter of its own (a
	// location from the request doesn't count, D-084).
	mapped bool
}

// fromExtraction validates the model's output and applies the D-081 intent
// rules in Go (the model's arithmetic is never trusted). Unknown chip /
// range / industry keys are dropped later by the search normalizer.
func fromExtraction(snap *domain.Snapshot, ext *extraction, req locationReq) built {
	b := built{notes: []string{}}
	f := &b.filters
	if l := ext.Location; l != nil {
		text := strings.TrimSpace(l.Text)
		switch {
		case l.Kind == "pincode" && rePincode.MatchString(text):
			f.Location = &domain.SearchLocation{PostalCode: rePincode.FindString(text)}
		case l.Kind == "place" && text != "":
			f.Location = &domain.SearchLocation{Place: text}
		}
	}
	b.mapped = f.Location != nil
	if ext.RadiusKm > 0 {
		f.RadiusKm = int(math.Round(ext.RadiusKm))
	}
	if a := ext.Area; a != nil {
		if ok := applyArea(f, a.Value, a.Unit, a.Intent); !ok {
			b.notes = append(b.notes, "area_dropped")
		}
	}
	if p := ext.Price; p != nil {
		if note := applyPrice(f, p.Amount, p.Currency, p.Basis, p.Intent); note != "" {
			b.notes = append(b.notes, note)
		}
	}
	f.Industries = ext.Industries
	f.Chips = ext.Chips
	for _, r := range ext.Ranges {
		mm, ok := rangeOf(snap, r.Key, r.Min, r.Max, r.Unit)
		if !ok {
			b.notes = append(b.notes, "range_dropped:"+r.Key)
			continue
		}
		if f.Ranges == nil {
			f.Ranges = map[string]domain.MinMax{}
		}
		f.Ranges[r.Key] = mm
	}
	if slices.Contains([]string{domain.SortRelevance, domain.SortDistance, domain.SortPriceAsc, domain.SortPriceDesc, domain.SortAreaDesc}, ext.Sort) {
		f.Sort = ext.Sort
	}
	for _, u := range ext.Unmapped {
		if u = strings.TrimSpace(u); u != "" {
			b.notes = append(b.notes, "unmapped:"+u)
		}
	}
	b.mapped = b.mapped || f.AreaSqm != nil || f.Price != nil || len(f.Industries) > 0 || len(f.Chips) > 0 || len(f.Ranges) > 0
	req.apply(f)
	return b
}

// fromHints is the basic search (LLM failed, D-088): the regex pre-parse
// plus chips found by a synonym scan of the query.
func fromHints(snap *domain.Snapshot, q string, h hints, req locationReq) built {
	b := built{notes: []string{"basic_search"}}
	f := &b.filters
	if h.Pincode != "" {
		f.Location = &domain.SearchLocation{PostalCode: h.Pincode}
	}
	if a := h.Area; a != nil {
		applyArea(f, a.Value, a.Unit, a.Intent)
	}
	if p := h.Price; p != nil {
		applyPrice(f, p.Amount, "", p.Basis, p.Intent)
	}
	f.Chips = scanChips(snap, q)
	b.mapped = f.Location != nil || f.AreaSqm != nil || f.Price != nil || len(f.Chips) > 0
	req.apply(f)
	return b
}

// locationReq is the request's own location (map centre / IP): used when
// the query names no place; its country always applies.
type locationReq struct {
	point   *domain.LatLng
	country string
}

func (r locationReq) apply(f *domain.SearchFilters) {
	if f.Location == nil && r.point != nil {
		p := *r.point
		f.Location = &domain.SearchLocation{Point: &p}
	}
	if f.Location != nil {
		f.Location.Country = r.country
	} else {
		f.Location = &domain.SearchLocation{Country: r.country} // no place: country only
	}
}

// applyArea sets AreaSqm per D-081: min / max bound, approx = ±20%.
func applyArea(f *domain.SearchFilters, value float64, unit, intent string) bool {
	if value <= 0 {
		return false
	}
	sqm, err := domain.ToCanonical(domain.DimArea, value, unit)
	if err != nil {
		return false
	}
	f.AreaInput = &domain.AreaInput{Value: value, Unit: unit, Intent: intent}
	switch intent {
	case intentMax:
		f.AreaSqm = &domain.MinMax{Max: ptr(sqm)}
	case intentApprox:
		f.AreaSqm = &domain.MinMax{Min: ptr(sqm * 0.8), Max: ptr(sqm * 1.2)}
	default:
		f.AreaInput.Intent = intentMin
		f.AreaSqm = &domain.MinMax{Min: ptr(sqm)}
	}
	return true
}

// applyPrice converts a rupee amount to the normalized per-sq-m-per-month
// minor units search filters on (D-058). A flat monthly total needs the
// area to divide by; without one the price is dropped. Returns a note when
// it drops the price.
func applyPrice(f *domain.SearchFilters, amount float64, currency, basis, intent string) string {
	if amount <= 0 {
		return "price_dropped"
	}
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if currency == "" {
		currency = "INR"
	}
	minor := amount * 100
	var perSqm float64
	switch basis {
	case domain.BasisPerSqmMonth:
		perSqm = minor
	case domain.BasisPerSqftMonth:
		perSqm = minor / domain.SqmPerSqft
	case domain.BasisFlatMonth:
		area := areaCentre(f.AreaSqm)
		if area <= 0 {
			return "price_needs_area"
		}
		perSqm = minor / area
	default:
		return "price_dropped"
	}
	perSqm = math.Round(perSqm*100) / 100
	pf := &domain.PriceFilter{Currency: currency}
	switch intent {
	case intentMin:
		pf.PerSqmMonthMin = ptr(perSqm)
	case intentApprox:
		pf.PerSqmMonthMin, pf.PerSqmMonthMax = ptr(perSqm*0.8), ptr(perSqm*1.2)
	default:
		pf.PerSqmMonthMax = ptr(perSqm)
	}
	f.Price = pf
	f.PriceInput = map[string]any{"amount": amount, "currency": currency, "basis": basis, "intent": intent}
	return ""
}

func areaCentre(a *domain.MinMax) float64 {
	switch {
	case a == nil:
		return 0
	case a.Min != nil && a.Max != nil:
		return (*a.Min + *a.Max) / 2
	case a.Min != nil:
		return *a.Min
	case a.Max != nil:
		return *a.Max
	}
	return 0
}

// rangeOf converts a range filter into the field's canonical unit and
// orders the bounds. Unknown keys pass through (the normalizer drops them).
func rangeOf(snap *domain.Snapshot, key string, lo, hi *float64, unit string) (domain.MinMax, bool) {
	if lo == nil && hi == nil {
		return domain.MinMax{}, false
	}
	_, field, ok := snap.Field(key)
	if ok && field.Unit != nil && unit != "" {
		conv := func(v *float64) (*float64, error) {
			if v == nil {
				return nil, nil
			}
			c, err := domain.ToCanonical(field.Unit.Family, *v, unit)
			return &c, err
		}
		var err1, err2 error
		lo, err1 = conv(lo)
		hi, err2 = conv(hi)
		if err1 != nil || err2 != nil {
			return domain.MinMax{}, false
		}
	}
	if lo != nil && hi != nil && *lo > *hi {
		lo, hi = hi, lo
	}
	return domain.MinMax{Min: lo, Max: hi}, true
}

// scanChips finds node chips whose name or synonyms appear as words in q
// (case-insensitive), plus pick/multi options named by label.
func scanChips(snap *domain.Snapshot, q string) []string {
	lq := " " + strings.ToLower(nonWord.ReplaceAllString(q, " ")) + " "
	has := func(phrase string) bool {
		phrase = strings.TrimSpace(strings.ToLower(nonWord.ReplaceAllString(phrase, " ")))
		return phrase != "" && strings.Contains(lq, " "+phrase+" ")
	}
	var out []string
	for i := range snap.Nodes {
		n := &snap.Nodes[i]
		if n.Key == domain.RootKey || !n.Public {
			continue
		}
		if n.Filterable && (has(n.Name) || slices.ContainsFunc(n.Synonyms, has)) {
			out = append(out, n.Key)
		}
		for _, f := range n.Fields {
			if !f.Public || !f.Filterable || (f.Type != domain.TypePick && f.Type != domain.TypeMulti) {
				continue
			}
			for _, o := range f.Options {
				if has(o.Label) {
					out = append(out, n.Key+"."+f.Key+":"+o.Key)
				}
			}
		}
	}
	return out
}

var nonWord = regexp.MustCompile(`[^\p{L}\p{N}]+`)

func ptr[T any](v T) *T { return &v }

// keywordText strips numbers, units and filler words from q, leaving the
// words worth matching with $text.
func keywordText(q string) string {
	var keep []string
	for _, w := range strings.Fields(strings.ToLower(nonWord.ReplaceAllString(q, " "))) {
		if stopwords[w] || strings.IndexFunc(w, func(r rune) bool { return r < '0' || r > '9' }) < 0 {
			continue
		}
		keep = append(keep, w)
	}
	return strings.Join(keep, " ")
}

var stopwords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a an the in at on near for with and or of to from by under upto up within around about approx
		sq ft sqft sft m sqm square feet foot meter metre meters metres k km per month rs inr lakh lac crore cr rent price
		need want looking find show me i we warehouse warehouses godown godowns space storage minimum min max maximum least than more less`) {
		stopwords[w] = true
	}
}
