package domain

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Tree-write validation (spec 02 "Tree rules"). Pure: the attributes module
// loads a fresh snapshot, applies the change to a copy and validates it here.

var (
	keyRe    = regexp.MustCompile(`^[a-z][a-z0-9_]{1,47}$`)
	optKeyRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,39}$`)
)

// ValidKey reports whether k is a valid attribute / industry key.
func ValidKey(k string) bool { return keyRe.MatchString(k) }

const (
	maxNameLen     = 120
	maxDescLen     = 2000
	maxSynonyms    = 30
	maxSynonymLen  = 60
	maxOptions     = 50
	maxFilterRowSz = 40
)

// ValidateDef checks d (as it would be stored) against the tree s, which must
// not yet contain d's change. It returns d normalized (canonical unit filled
// in, appliesWhen values coerced, strings trimmed).
func ValidateDef(s *Snapshot, d AttrDef) (AttrDef, error) {
	d.Name = strings.TrimSpace(d.Name)
	d.Description = strings.TrimSpace(d.Description)
	d.FilterRow = strings.TrimSpace(d.FilterRow)
	if !ValidKey(d.Key) {
		return d, fmt.Errorf("key must match %s", keyRe)
	}
	if d.Name == "" || utf8.RuneCountInString(d.Name) > maxNameLen {
		return d, fmt.Errorf("name must be 1–%d characters", maxNameLen)
	}
	if utf8.RuneCountInString(d.Description) > maxDescLen {
		return d, fmt.Errorf("description is longer than %d characters", maxDescLen)
	}
	syn, err := normalizeSynonyms(d.Synonyms)
	if err != nil {
		return d, err
	}
	d.Synonyms = syn

	switch d.Kind {
	case KindGroup:
		if d.Type != "" || d.Unit != nil || len(d.AllowedValues) > 0 || d.Calc != nil || d.AppliesWhen != nil || d.Filterable {
			return d, fmt.Errorf("a group has no type, unit, values, calc, applies-when or filter")
		}
		d.Role = ""
	case KindAttribute:
		if err := validateAttribute(&d); err != nil {
			return d, err
		}
	default:
		return d, fmt.Errorf("kind must be group or attribute")
	}

	if err := validateParent(s, d); err != nil {
		return d, err
	}
	if d.AppliesWhen != nil {
		n := *d.AppliesWhen
		if n.Op != OpAny && n.Op != OpAll {
			return d, fmt.Errorf("appliesWhen.op must be any or all")
		}
		if len(n.Conds) == 0 {
			return d, fmt.Errorf("appliesWhen needs at least one condition (or null)")
		}
		for _, c := range n.Conds {
			if c.Attr == d.Key {
				return d, fmt.Errorf("appliesWhen may not reference the attribute itself")
			}
		}
		conds, err := NormalizeConditions(s, n.Conds)
		if err != nil {
			return d, fmt.Errorf("appliesWhen: %w", err)
		}
		d.AppliesWhen = &CondNode{Op: n.Op, Conds: conds}
	}
	if cyc := dependencyCycle(s, d); cyc != "" {
		return d, fmt.Errorf("applicability cycle through %q", cyc)
	}
	return d, nil
}

func validateAttribute(d *AttrDef) error {
	if !KnownType(d.Type) {
		return fmt.Errorf("unknown type %q", d.Type)
	}
	switch d.Role {
	case "":
		d.Role = RoleParameter
	case RoleParameter, RoleProperty:
	default:
		return fmt.Errorf("role must be parameter or property")
	}

	// unit
	switch d.Type {
	case TypeNumber, TypeRange:
		if d.Unit == nil {
			return fmt.Errorf("%s needs a unit (dimension count for plain numbers)", d.Type)
		}
	case TypeCalculated:
	default:
		if d.Unit != nil {
			return fmt.Errorf("type %s takes no unit", d.Type)
		}
	}
	if d.Unit != nil {
		u := *d.Unit
		if !KnownDimension(u.Dimension) {
			return fmt.Errorf("unknown unit dimension %q", u.Dimension)
		}
		for _, in := range u.Input {
			if !AcceptsUnit(u.Dimension, in) {
				return fmt.Errorf("unit %q is not valid for %s", in, u.Dimension)
			}
		}
		u.Canonical = CanonicalUnit(u.Dimension)
		d.Unit = &u
	}

	// options
	if d.Type == TypePick || d.Type == TypeMulti {
		if len(d.AllowedValues) == 0 || len(d.AllowedValues) > maxOptions {
			return fmt.Errorf("%s needs 1–%d allowed values", d.Type, maxOptions)
		}
		seen := map[string]bool{}
		for i := range d.AllowedValues {
			av := &d.AllowedValues[i]
			av.Label = strings.TrimSpace(av.Label)
			if !optKeyRe.MatchString(av.Key) || seen[av.Key] {
				return fmt.Errorf("allowed value key %q is invalid or duplicated", av.Key)
			}
			if av.Label == "" {
				return fmt.Errorf("allowed value %q needs a label", av.Key)
			}
			seen[av.Key] = true
		}
	} else if len(d.AllowedValues) > 0 {
		return fmt.Errorf("type %s takes no allowed values", d.Type)
	}

	// calc
	if d.Type == TypeCalculated {
		if d.Calc == nil || CalcFns[d.Calc.Fn] == nil {
			return fmt.Errorf("calculated attribute needs a known calc fn")
		}
	} else if d.Calc != nil {
		return fmt.Errorf("only calculated attributes take a calc fn")
	}

	// filter
	if d.Filterable {
		if d.Type == TypeText || d.Type == TypeMoney {
			return fmt.Errorf("type %s cannot be filterable", d.Type)
		}
		if d.FilterRow == "" || len(d.FilterRow) > maxFilterRowSz {
			return fmt.Errorf("a filterable attribute needs a filterRow")
		}
	}
	return nil
}

// validateParent: the parent exists, is not retired, is not the node or one of
// its descendants, and is a group or a bool/pick attribute. A calculated
// attribute may also hang under a number (dock ratio under dock doors).
// Groups may only sit under groups.
func validateParent(s *Snapshot, d AttrDef) error {
	if d.ParentKey == "" {
		return nil
	}
	if d.ParentKey == d.Key || s.IsAncestor(d.Key, d.ParentKey) {
		return fmt.Errorf("a node cannot move under itself or its descendant")
	}
	p, ok := s.Def(d.ParentKey)
	if !ok {
		return fmt.Errorf("parent %q does not exist", d.ParentKey)
	}
	if p.Retired && !d.Retired {
		return fmt.Errorf("parent %q is retired", d.ParentKey)
	}
	if p.IsGroup() {
		return nil
	}
	if d.Kind == KindGroup {
		return fmt.Errorf("a group can only sit under another group")
	}
	switch {
	case p.Type == TypeBool || p.Type == TypePick:
		return nil
	case p.Type == TypeNumber && d.Type == TypeCalculated:
		return nil
	}
	return fmt.Errorf("parent %q must be a group or a bool/pick attribute", d.ParentKey)
}

// dependencyCycle reports a node through which d's applicability would depend
// on itself (parent edges + appliesWhen edges), or "".
func dependencyCycle(s *Snapshot, d AttrDef) string {
	deps := func(key string) []string {
		var n AttrDef
		if key == d.Key {
			n = d
		} else if x, ok := s.Def(key); ok {
			n = *x
		} else {
			return nil
		}
		var out []string
		if n.ParentKey != "" {
			out = append(out, n.ParentKey)
		}
		if n.AppliesWhen != nil {
			for _, c := range n.AppliesWhen.Conds {
				out = append(out, c.Attr)
			}
		}
		return out
	}
	seen := map[string]bool{}
	var hit string
	var walk func(k string) bool
	walk = func(k string) bool {
		for _, nk := range deps(k) {
			if nk == d.Key {
				hit = k
				return true
			}
			if seen[nk] {
				continue
			}
			seen[nk] = true
			if walk(nk) {
				return true
			}
		}
		return false
	}
	walk(d.Key)
	return hit
}

func normalizeSynonyms(in []string) ([]string, error) {
	if len(in) > maxSynonyms {
		return nil, fmt.Errorf("at most %d synonyms", maxSynonyms)
	}
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[strings.ToLower(s)] {
			continue
		}
		if utf8.RuneCountInString(s) > maxSynonymLen {
			return nil, fmt.Errorf("synonym %q is longer than %d characters", s, maxSynonymLen)
		}
		seen[strings.ToLower(s)] = true
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// CheckDefUpdate enforces what an update may not change: key, kind, type,
// parent (use move), the unit dimension (stored values are canonical in it),
// and existing allowed values (retire them instead of deleting).
func CheckDefUpdate(old, upd AttrDef) error {
	if upd.Key != old.Key || upd.Kind != old.Kind || upd.Type != old.Type {
		return fmt.Errorf("key, kind and type are immutable")
	}
	if upd.ParentKey != old.ParentKey {
		return fmt.Errorf("use move to change the parent")
	}
	if (old.Unit == nil) != (upd.Unit == nil) || (old.Unit != nil && old.Unit.Dimension != upd.Unit.Dimension) {
		return fmt.Errorf("the unit dimension is immutable")
	}
	for _, av := range old.AllowedValues {
		if !upd.HasAllowedValue(av.Key) {
			return fmt.Errorf("allowed value %q cannot be removed; retire it", av.Key)
		}
	}
	return nil
}

// ValidateIndustry checks an industry against the tree and returns it
// normalized.
func ValidateIndustry(s *Snapshot, ind Industry) (Industry, error) {
	ind.Name = strings.TrimSpace(ind.Name)
	if !ValidKey(ind.Key) {
		return ind, fmt.Errorf("key must match %s", keyRe)
	}
	if ind.Name == "" || utf8.RuneCountInString(ind.Name) > maxNameLen {
		return ind, fmt.Errorf("name must be 1–%d characters", maxNameLen)
	}
	req, err := NormalizeConditions(s, ind.Required)
	if err != nil {
		return ind, fmt.Errorf("required: %w", err)
	}
	pref, err := NormalizeConditions(s, ind.Preferred)
	if err != nil {
		return ind, fmt.Errorf("preferred: %w", err)
	}
	ind.Required, ind.Preferred = req, pref
	return ind, nil
}
