package finder

import (
	"math"
	"slices"
	"strings"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// Pure matching and scoring for the experimental Warehouse Finder wizard
// (POST /v1/public/finder/match): no IO, no AI. The handler fetches
// candidates; everything here is deterministic. sqftPerSqm, minAreaRatio,
// typeChipPrefix, the hub radii and haversineKm are shared with the filter
// search (search.go, hubs.go).

const (
	maxArea     = 20
	maxType     = 30
	maxIndustry = 25
	maxPort     = 20
	maxRail     = 20
)

// Request values.
const (
	goodsGeneral = "general"
	goodsDG      = "dg"
	goodsCold    = "cold"
)

var (
	typeLabels  = map[string]string{"general": "General", "bonded": "Bonded", "ftwz": "FTWZ"}
	typePhrases = map[string]string{"general": "general type", "bonded": "bonded type", "ftwz": "FTWZ type"}
)

// industryLabels are the supported industries (others are ignored).
var industryLabels = map[string]string{"pharma": "Pharma", "ecommerce": "E-commerce", "chemicals": "Chemicals"}

// matchCandidate is one live listing as read for the wizard.
type matchCandidate struct {
	ShortID       string           `bson:"short_id"`
	Slug          string           `bson:"slug"`
	Name          string           `bson:"name"`
	City          string           `bson:"city"`
	Locality      string           `bson:"locality"`
	Loc           *models.GeoPoint `bson:"loc"`
	TotalSqm      float64          `bson:"total_sqm"`
	Price         *models.Price    `bson:"price"`
	CoverKey      string           `bson:"cover_key"`
	Chips         []string         `bson:"chips"`
	Fit           []string         `bson:"fit"`
	Completeness  float64          `bson:"completeness"`
	VerifiedRatio float64          `bson:"verified_ratio"`
	Live          *struct {
		Attributes struct {
			Warehouse struct {
				Fields struct {
					Rent *models.FieldValue `bson:"rent"`
				} `bson:"fields"`
			} `bson:"warehouse"`
		} `bson:"attributes"`
	} `bson:"live"`
}

// rent is the live headline rent field (nil when absent).
func (c matchCandidate) rent() *models.FieldValue {
	if c.Live == nil {
		return nil
	}
	return c.Live.Attributes.Warehouse.Fields.Rent
}

// criteria is a validated match request.
type criteria struct {
	City       string
	Type       string // "" = any
	NeededSqm  float64
	DG, Cold   bool
	Industries []string // supported, deduplicated, request order
	NearPort   bool
	NearRail   bool
}

// minSqm is the smallest total area that passes the hard filter.
func (c criteria) minSqm() float64 { return minAreaRatio * c.NeededSqm }

// sqftToSqm converts the asked area.
func sqftToSqm(sqft float64) float64 { return sqft / sqftPerSqm }

// listingType returns the listing's warehouse.type option ("" = unknown).
func listingType(chips []string) string {
	for _, c := range chips {
		if t, ok := strings.CutPrefix(c, typeChipPrefix); ok {
			return t
		}
	}
	return ""
}

// typeExcluded: with a wanted type, only a listing of a DIFFERENT known type
// is dropped; an unknown type is kept.
func typeExcluded(want string, chips []string) bool {
	if want == "" {
		return false
	}
	got := listingType(chips)
	return got != "" && got != want
}

// areaPoints scores total ÷ needed: 1.0–1.5 → 20, 0.8–1.0 → 12,
// 1.5–3.0 → 10, above 3.0 → 4 (below 0.8 is filtered out; 0 here).
func areaPoints(ratio float64) int {
	switch {
	case ratio >= 1.0 && ratio <= 1.5:
		return 20
	case ratio >= minAreaRatio && ratio < 1.0:
		return 12
	case ratio > 1.5 && ratio <= 3.0:
		return 10
	case ratio > 3.0:
		return 4
	}
	return 0
}

// industryPoints reads ind's verdict from fit ("pharma:F"): F 25, P 12,
// U 4, N or missing 0.
func industryPoints(fit []string, ind string) (int, domain.Verdict) {
	for _, f := range fit {
		k, v, ok := strings.Cut(f, ":")
		if !ok || k != ind {
			continue
		}
		switch domain.Verdict(v) {
		case domain.VerdictFit:
			return maxIndustry, domain.VerdictFit
		case domain.VerdictPartial:
			return 12, domain.VerdictPartial
		case domain.VerdictUnverified:
			return 4, domain.VerdictUnverified
		}
		return 0, domain.Verdict(v)
	}
	return 0, ""
}

// nearLoc reports whether loc ([lng, lat]) is within km of any hub.
func nearLoc(loc *models.GeoPoint, hubs []hub, km float64) bool {
	if loc == nil {
		return false
	}
	return nearAny(loc.Coordinates[1], loc.Coordinates[0], hubs, km)
}

// match is one matched criterion: a short label for the card and the phrase
// the reason line uses.
type match struct {
	Label  string
	Phrase string
	Near   bool // phrased "is near …"
}

// scored is one candidate's evaluation.
type scored struct {
	C           matchCandidate
	Score, Max  int
	Matched     []match
	TypeUnknown bool
}

// Percent is round(score / max × 100).
func (s scored) Percent() int {
	if s.Max <= 0 {
		return 0
	}
	return int(math.Round(float64(s.Score) / float64(s.Max) * 100))
}

// score evaluates c against q. Max counts only the criteria q selected.
// City and goods are hard filters (already applied), so they add matches
// but no points.
func score(q criteria, c matchCandidate) scored {
	s := scored{C: c, Max: maxArea}
	s.Matched = append(s.Matched, match{Label: "City", Phrase: "your city"})

	if q.Type != "" {
		s.Max += maxType
		switch listingType(c.Chips) {
		case q.Type:
			s.Score += maxType
			s.Matched = append(s.Matched, match{Label: typeLabels[q.Type], Phrase: typePhrases[q.Type]})
		case "":
			s.TypeUnknown = true
		}
	}

	if q.NeededSqm > 0 {
		pts := areaPoints(c.TotalSqm / q.NeededSqm)
		s.Score += pts
		if pts == maxArea {
			s.Matched = append(s.Matched, match{Label: "Right size", Phrase: "your size"})
		}
	}
	if q.DG {
		s.Matched = append(s.Matched, match{Label: "DG storage", Phrase: "DG storage"})
	}
	if q.Cold {
		s.Matched = append(s.Matched, match{Label: "Cold storage", Phrase: "cold storage"})
	}

	for _, ind := range q.Industries {
		s.Max += maxIndustry
		pts, v := industryPoints(c.Fit, ind)
		s.Score += pts
		switch v {
		case domain.VerdictFit:
			s.Matched = append(s.Matched, match{Label: industryLabels[ind] + " fit", Phrase: industryLabels[ind] + " fit"})
		case domain.VerdictPartial:
			s.Matched = append(s.Matched, match{Label: industryLabels[ind] + " partial fit", Phrase: "partial " + industryLabels[ind] + " fit"})
		}
	}

	if q.NearPort {
		s.Max += maxPort
		if nearLoc(c.Loc, ports, nearPortKm) {
			s.Score += maxPort
			s.Matched = append(s.Matched, match{Label: "Near port", Phrase: "a port", Near: true})
		}
	}
	if q.NearRail {
		s.Max += maxRail
		if nearLoc(c.Loc, rail, nearRailKm) {
			s.Score += maxRail
			s.Matched = append(s.Matched, match{Label: "Near rail", Phrase: "a rail terminal", Near: true})
		}
	}
	return s
}

// rank scores every candidate the type rule keeps and returns the best n:
// score, then completeness, then verified ratio (short id last, for a
// stable order).
func rank(q criteria, cs []matchCandidate, n int) []scored {
	out := make([]scored, 0, len(cs))
	for _, c := range cs {
		if typeExcluded(q.Type, c.Chips) {
			continue
		}
		out = append(out, score(q, c))
	}
	slices.SortStableFunc(out, func(a, b scored) int {
		switch {
		case a.Score != b.Score:
			return b.Score - a.Score
		case a.C.Completeness != b.C.Completeness:
			return cmpDesc(a.C.Completeness, b.C.Completeness)
		case a.C.VerifiedRatio != b.C.VerifiedRatio:
			return cmpDesc(a.C.VerifiedRatio, b.C.VerifiedRatio)
		}
		return strings.Compare(a.C.ShortID, b.C.ShortID)
	})
	return out[:min(n, len(out))]
}

func cmpDesc(a, b float64) int {
	if a > b {
		return -1
	}
	return 1
}

// reason is the one-line summary, e.g. "Matches your city, bonded type and
// is near a port".
func reason(ms []match) string {
	var matches, near []string
	for _, m := range ms {
		if m.Near {
			near = append(near, m.Phrase)
		} else {
			matches = append(matches, m.Phrase)
		}
	}
	if len(matches) == 0 {
		if len(near) == 0 {
			return ""
		}
		return "Is near " + joinList(near)
	}
	if len(near) > 0 {
		matches = append(matches, "is near "+joinList(near))
	}
	return "Matches " + joinList(matches)
}

// joinList joins "a", "a and b", "a, b and c".
func joinList(xs []string) string {
	if len(xs) <= 1 {
		return strings.Join(xs, "")
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}
