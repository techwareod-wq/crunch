package service

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// Intents (D-081).
const (
	intentMin    = "min"
	intentMax    = "max"
	intentApprox = "approx"
)

// hints is the regex pre-parse (spec 05 step 1): passed to the model as
// hints, and the whole parse when the model fails (D-088).
type hints struct {
	Pincode string
	Area    *areaHint
	Price   *priceHint
}

type areaHint struct {
	Value  float64
	Unit   string // sqft | sqm
	Intent string
}

type priceHint struct {
	Amount float64 // major units (rupees)
	Basis  string  // per_sqft_month | per_sqm_month | flat_month
	Intent string
}

var (
	reArea = regexp.MustCompile(`(?i)(\d[\d,]*(?:\.\d+)?)\s*(k)?\s*(sq\.?\s*ft\.?|sqft|sft|square\s+f(?:ee|oo)t|sq\.?\s*m(?:trs?|eters?|etres?)?\.?|sqm|square\s+met(?:er|re)s?)`)
	// Money needs a currency or an Indian multiplier word, so a bare number
	// (a pincode, a count) is never read as a price.
	reMoneyCur  = regexp.MustCompile(`(?i)(?:₹|\brs\.?|\binr)\s*(\d[\d,]*(?:\.\d+)?)\s*(k|lakhs?|lacs?|l|crores?|cr)?\b`)
	reMoneyWord = regexp.MustCompile(`(?i)\b(\d[\d,]*(?:\.\d+)?)\s*(lakhs?|lacs?|crores?|cr)\b`)
	reBasis     = regexp.MustCompile(`(?i)^\s*(?:/|per\s+|a\s+)?\s*(sq\.?\s*ft\.?|sqft|sft|square\s+f(?:ee|oo)t|sq\.?\s*m\.?|sqm|square\s+met(?:er|re)s?|month|mo|pm)\b`)
	rePincode   = regexp.MustCompile(`\b[1-9]\d{5}\b`)

	reMin    = regexp.MustCompile(`(?i)(at\s*least|atleast|min(?:imum)?|above|more\s+than|over|\bfrom)\s*$`)
	reMax    = regexp.MustCompile(`(?i)(under|up\s*to|upto|max(?:imum)?|less\s+than|below|within|budget(?:\s+of)?)\s*$`)
	reApprox = regexp.MustCompile(`(?i)(about|around|approx(?:\.|imately)?|~|roughly|nearly|close\s+to)\s*$`)
)

// qualifierWindow is how far before a number a qualifier word is looked for.
const qualifierWindow = 24

// preparse extracts pincode, area and price. Matched spans are masked so a
// number is read once (an area's digits are never a pincode).
func preparse(q string) hints {
	var h hints
	masked := []byte(q)
	mask := func(lo, hi int) {
		for i := lo; i < hi; i++ {
			masked[i] = ' '
		}
	}

	if m := reArea.FindStringSubmatchIndex(q); m != nil {
		v, err := parseNumber(q[m[2]:m[3]])
		if err == nil {
			if m[4] >= 0 {
				v *= 1000
			}
			h.Area = &areaHint{Value: v, Unit: areaUnit(q[m[6]:m[7]]), Intent: qualifier(q[:m[0]], intentMin)}
			mask(m[0], m[1])
		}
	}

	text := string(masked)
	for _, re := range []*regexp.Regexp{reMoneyCur, reMoneyWord} {
		m := re.FindStringSubmatchIndex(text)
		if m == nil {
			continue
		}
		v, err := parseNumber(text[m[2]:m[3]])
		if err != nil {
			continue
		}
		if m[4] >= 0 {
			v *= multiplier(text[m[4]:m[5]])
		}
		basis, end := domain.BasisFlatMonth, m[1]
		if b := reBasis.FindStringSubmatchIndex(text[m[1]:]); b != nil {
			switch u := strings.ToLower(text[m[1]+b[2] : m[1]+b[3]]); {
			case strings.Contains(u, "f") || u == "sft":
				basis = domain.BasisPerSqftMonth
			case strings.Contains(u, "m") && !strings.HasPrefix(u, "mo") && u != "pm":
				basis = domain.BasisPerSqmMonth
			}
			end = m[1] + b[1]
		}
		h.Price = &priceHint{Amount: v, Basis: basis, Intent: qualifier(text[:m[0]], intentMax)}
		mask(m[0], end)
		text = string(masked)
		break
	}

	if p := rePincode.FindString(string(masked)); p != "" {
		h.Pincode = p
	}
	return h
}

// qualifier reads the intent from the words just before a number; def when
// there are none (area: min, D-081; price: max).
func qualifier(before, def string) string {
	if len(before) > qualifierWindow {
		before = before[len(before)-qualifierWindow:]
	}
	switch {
	case reApprox.MatchString(before):
		return intentApprox
	case reMax.MatchString(before):
		return intentMax
	case reMin.MatchString(before):
		return intentMin
	}
	return def
}

func parseNumber(s string) (float64, error) {
	return strconv.ParseFloat(strings.ReplaceAll(s, ",", ""), 64)
}

func multiplier(w string) float64 {
	switch w = strings.ToLower(w); {
	case w == "k":
		return 1e3
	case strings.HasPrefix(w, "cr"):
		return 1e7
	case strings.HasPrefix(w, "l"):
		return 1e5
	}
	return 1
}

func areaUnit(u string) string {
	u = strings.ToLower(u)
	if strings.Contains(u, "f") || u == "sft" {
		return domain.UnitSqft
	}
	return domain.UnitSqm
}

// String renders the hints for the prompt's second system block.
func (h hints) String() string {
	var parts []string
	if h.Pincode != "" {
		parts = append(parts, "pincode "+h.Pincode)
	}
	if a := h.Area; a != nil {
		parts = append(parts, fmt.Sprintf("area %g %s (%s)", a.Value, a.Unit, a.Intent))
	}
	if p := h.Price; p != nil {
		parts = append(parts, fmt.Sprintf("price %g INR %s (%s)", p.Amount, p.Basis, p.Intent))
	}
	return strings.Join(parts, "; ")
}
