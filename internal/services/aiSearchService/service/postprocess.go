package service

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// Query rules applied in Go after the parse (model or basic search): the
// model's radius and arithmetic are not trusted for these.

const (
	// nearRadiusKm is the radius for "near <place>" when the parse set none
	// (the search default, 25 km, is narrower than "near" usually means).
	nearRadiusKm = 50
	// sqftPerPallet sizes an area from a pallet count. ASSUMPTION: one
	// standard pallet (1.2 m × 1.0 m) plus its share of aisle space, single
	// stacked: about 12 sq ft. Matches the Warehouse Finder.
	sqftPerPallet = 12
)

var (
	reNear = regexp.MustCompile(`(?i)\bnear\b`)
	// reAreaUnit: the query states an area itself (any area unit), so a
	// pallet count must not replace it.
	reAreaUnit = regexp.MustCompile(`(?i)(\bsq\.?\s*(ft|m|mtrs?|meters?|metres?)\b|\bsqft\b|\bsft\b|\bsqm\b|\bsquare\s+(feet|foot|met(er|re)s?)\b)`)
)

// applyQueryRules adjusts b for the query q (h is its pre-parse):
//   - "near <place>": a named place (or pincode) with no radius gets
//     nearRadiusKm.
//   - pallets: with no area stated in the query, the pallet count becomes
//     the area (count × sqftPerPallet sq ft, with its qualifier); with an area
//     stated, that area wins and the notes say pallets_ignored.
func applyQueryRules(b *built, q string, h hints) {
	f := &b.filters
	if f.RadiusKm == 0 && f.Location != nil && (f.Location.Place != "" || f.Location.PostalCode != "") && reNear.MatchString(q) {
		f.RadiusKm = nearRadiusKm
		b.notes = append(b.notes, "near_radius:"+strconv.Itoa(nearRadiusKm))
	}
	if p := h.Pallets; p != nil {
		if h.Area != nil || reAreaUnit.MatchString(q) {
			b.notes = append(b.notes, "pallets_ignored")
			return
		}
		if applyArea(f, p.Count*sqftPerPallet, domain.UnitSqft, p.Intent) {
			b.notes = slices.DeleteFunc(b.notes, func(n string) bool {
				return strings.HasPrefix(n, "unmapped:") && strings.Contains(strings.ToLower(n), "pallet")
			})
			b.notes = append(b.notes, "pallets_area")
			b.mapped = true
		}
	}
}
