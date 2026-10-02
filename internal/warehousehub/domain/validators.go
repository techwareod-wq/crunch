package domain

import (
	"fmt"
	"github.com/atharva-ng/crunch/internal/models"
	"math"
	"regexp"
	"slices"
	"strings"
	"sync"
)

// Field validations (D-137): an extensible registry. A field stores an
// ordered list of Validation{kind, value}; each kind is one entry here.
// Adding a kind = one registry entry + one admin form control, no migration.

// MaxRegexLen caps an admin-entered pattern.
const MaxRegexLen = 500

// MaxValidations bounds one field's validation list.
const MaxValidations = 10

// Validator is one registry entry.
type Validator struct {
	// AppliesTo lists the field types the kind may be set on.
	AppliesTo []models.FieldType
	// CheckParams validates the parameter when the admin saves the field and
	// returns it in canonical form.
	CheckParams func(f *models.AttributeField, p any) (any, error)
	// Check validates a canonical value (see CanonicalizeValue). It returns
	// the default message on failure.
	Check func(f *models.AttributeField, fv *models.FieldValue, p any) error
}

// Validation kinds (v1).
const (
	ValidMin        = "min"
	ValidMax        = "max"
	ValidMaxLength  = "maxLength"
	ValidRegex      = "regex"
	ValidUnits      = "units"
	ValidCurrencies = "currencies"
)

var validators = map[string]Validator{
	ValidMin: {
		AppliesTo:   []models.FieldType{TypeNumber, TypeRange, TypeArea, TypeMoney},
		CheckParams: numberParam,
		Check: func(f *models.AttributeField, fv *models.FieldValue, p any) error {
			lim, _ := asFloat(p)
			if lo, _, ok := boundsOf(f.Type, fv.V); ok && lo < lim {
				return fmt.Errorf("must be at least %v", lim)
			}
			return nil
		},
	},
	ValidMax: {
		AppliesTo:   []models.FieldType{TypeNumber, TypeRange, TypeArea, TypeMoney},
		CheckParams: numberParam,
		Check: func(f *models.AttributeField, fv *models.FieldValue, p any) error {
			lim, _ := asFloat(p)
			if _, hi, ok := boundsOf(f.Type, fv.V); ok && hi > lim {
				return fmt.Errorf("must be at most %v", lim)
			}
			return nil
		},
	},
	ValidMaxLength: {
		AppliesTo: []models.FieldType{TypeText, TypeLongtext},
		CheckParams: func(f *models.AttributeField, p any) (any, error) {
			n, ok := asFloat(p)
			if !ok || n != math.Trunc(n) || n < 1 || n > float64(maxTextLen(f.Type)) {
				return nil, fmt.Errorf("expects a whole number 1–%d", maxTextLen(f.Type))
			}
			return int(n), nil
		},
		Check: func(_ *models.AttributeField, fv *models.FieldValue, p any) error {
			n, _ := asFloat(p)
			if s, _ := asString(fv.V); float64(len([]rune(s))) > n {
				return fmt.Errorf("must be at most %d characters", int(n))
			}
			return nil
		},
	},
	ValidRegex: {
		AppliesTo: []models.FieldType{TypeText},
		CheckParams: func(_ *models.AttributeField, p any) (any, error) {
			s, ok := asString(p)
			if !ok || s == "" || len(s) > MaxRegexLen {
				return nil, fmt.Errorf("expects a pattern of 1–%d characters", MaxRegexLen)
			}
			if _, err := compileRegex(s); err != nil {
				return nil, fmt.Errorf("invalid pattern: %v", err)
			}
			return s, nil
		},
		Check: func(_ *models.AttributeField, fv *models.FieldValue, p any) error {
			pat, _ := asString(p)
			re, err := compileRegex(pat)
			if err != nil {
				return fmt.Errorf("pattern is invalid")
			}
			if s, _ := asString(fv.V); !re.MatchString(s) {
				return fmt.Errorf("has the wrong format")
			}
			return nil
		},
	},
	ValidUnits: {
		AppliesTo: []models.FieldType{TypeNumber, TypeRange, TypeArea},
		CheckParams: func(f *models.AttributeField, p any) (any, error) {
			us, ok := asStrings(p)
			if !ok || len(us) == 0 {
				return nil, fmt.Errorf("expects a non-empty list of units")
			}
			fam := fieldFamily(f)
			if fam == "" {
				return nil, fmt.Errorf("the field has no unit family")
			}
			for _, u := range us {
				if !AcceptsUnit(fam, u) {
					return nil, fmt.Errorf("unit %q is not a %s unit", u, fam)
				}
			}
			return dedupe(us), nil
		},
		Check: func(f *models.AttributeField, fv *models.FieldValue, p any) error {
			us, _ := asStrings(p)
			unit := ""
			if f.Type == TypeArea {
				if a, ok := asArea(fv.V); ok {
					unit = a.Unit
				}
			} else if fv.Raw != nil {
				unit = fv.Raw.Unit
			}
			if unit == "" {
				unit = CanonicalUnit(fieldFamily(f))
			}
			if !slices.Contains(us, unit) {
				return fmt.Errorf("unit must be one of %s", strings.Join(us, ", "))
			}
			return nil
		},
	},
	ValidCurrencies: {
		AppliesTo: []models.FieldType{TypeMoney},
		CheckParams: func(_ *models.AttributeField, p any) (any, error) {
			cs, ok := asStrings(p)
			if !ok || len(cs) == 0 {
				return nil, fmt.Errorf("expects a non-empty list of ISO 4217 codes")
			}
			out := make([]string, 0, len(cs))
			for _, c := range cs {
				c = strings.ToUpper(strings.TrimSpace(c))
				if !isAlpha(c, 3) {
					return nil, fmt.Errorf("%q is not an ISO 4217 code", c)
				}
				out = append(out, c)
			}
			return dedupe(out), nil
		},
		Check: func(_ *models.AttributeField, fv *models.FieldValue, p any) error {
			cs, _ := asStrings(p)
			if m, ok := decodeDoc[models.Money](fv.V); ok && !slices.Contains(cs, m.Currency) {
				return fmt.Errorf("currency must be one of %s", strings.Join(cs, ", "))
			}
			return nil
		},
	},
}

// ValidationKinds lists the registered kinds (sorted), for the admin form.
func ValidationKinds() map[string][]models.FieldType {
	out := make(map[string][]models.FieldType, len(validators))
	for k, v := range validators {
		out[k] = slices.Clone(v.AppliesTo)
	}
	return out
}

// NormalizeValidations checks a field's validation list at save time and
// returns it with canonical parameters.
func NormalizeValidations(f *models.AttributeField) ([]models.FieldValidation, error) {
	if len(f.Validations) > MaxValidations {
		return nil, fmt.Errorf("at most %d validations", MaxValidations)
	}
	out := make([]models.FieldValidation, 0, len(f.Validations))
	for _, v := range f.Validations {
		reg, ok := validators[v.Kind]
		if !ok {
			return nil, fmt.Errorf("unknown validation %q", v.Kind)
		}
		if !slices.Contains(reg.AppliesTo, f.Type) {
			return nil, fmt.Errorf("validation %q does not apply to a %s field", v.Kind, f.Type)
		}
		p, err := reg.CheckParams(f, v.Value)
		if err != nil {
			return nil, fmt.Errorf("validation %q: %w", v.Kind, err)
		}
		v.Value = p
		v.Message = strings.TrimSpace(v.Message)
		out = append(out, v)
	}
	return out, nil
}

// RunValidations checks a canonical value against f's validations, in order,
// and returns the first failure.
func RunValidations(f *models.AttributeField, fv *models.FieldValue) error {
	if fv == nil {
		return nil
	}
	for _, v := range f.Validations {
		reg, ok := validators[v.Kind]
		if !ok {
			continue // a kind removed from the registry no longer applies
		}
		if err := reg.Check(f, fv, v.Value); err != nil {
			if v.Message != "" {
				return fmt.Errorf("%s", v.Message)
			}
			return err
		}
	}
	return nil
}

func numberParam(_ *models.AttributeField, p any) (any, error) {
	f, ok := asFloat(p)
	if !ok {
		return nil, fmt.Errorf("expects a number")
	}
	return f, nil
}

// boundsOf returns the low/high comparable numbers of a value: canonical for
// number/range, sq m for area, minor units for money (skipped when the price
// is on request).
func boundsOf(t models.FieldType, v any) (lo, hi float64, ok bool) {
	switch t {
	case TypeNumber:
		f, ok := asFloat(v)
		return f, f, ok
	case TypeRange:
		r, ok := asRange(v)
		return r.Min, r.Max, ok
	case TypeArea:
		a, ok := asArea(v)
		return a.Sqm, a.Sqm, ok
	case TypeMoney:
		m, ok := decodeDoc[models.Money](v)
		if !ok || m.OnRequest {
			return 0, 0, false
		}
		return float64(m.Amount), float64(m.Amount), true
	}
	return 0, 0, false
}

// fieldFamily is the unit family of a number/range/area field ("" if none).
func fieldFamily(f *models.AttributeField) models.Dimension {
	if f.Type == TypeArea {
		return DimArea
	}
	if f.Unit != nil {
		return f.Unit.Family
	}
	return ""
}

var regexCache sync.Map // pattern → *regexp.Regexp

func compileRegex(pat string) (*regexp.Regexp, error) {
	if re, ok := regexCache.Load(pat); ok {
		return re.(*regexp.Regexp), nil
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		return nil, err
	}
	regexCache.Store(pat, re)
	return re, nil
}

func dedupe(ss []string) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		if !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

func isAlpha(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, r := range s {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}
