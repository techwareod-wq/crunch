package domain

import (
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// Definition validation for nodes, fields and industries (spec 02). Pure:
// the attributes service calls these against a fresh snapshot before every
// write.

// Limits on definitions.
const (
	MaxNameLen        = 100
	MaxDescriptionLen = 1000
	MaxFieldsPerNode  = 50
	MaxOptions        = 100
	MaxSynonyms       = 30
)

var keyRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,47}$`)

// ValidKey reports whether k is a valid node / field / option / industry key.
func ValidKey(k string) bool { return keyRe.MatchString(k) }

// ValidateNode checks a node (and every field) against the tree and returns
// it normalized. The parent must exist and must not be the node or one of its
// descendants (a move cycle). Only the root has no parent.
func ValidateNode(s *Snapshot, n Node) (Node, error) {
	n = n.Clone()
	if !ValidKey(n.Key) {
		return n, fmt.Errorf("key %q must be lowercase letters, digits and _ (start with a letter, ≤ 48)", n.Key)
	}
	var err error
	if n.Name, n.Description, err = checkText(n.Name, n.Description); err != nil {
		return n, err
	}
	if n.Key == RootKey {
		if n.ParentKey != "" {
			return n, fmt.Errorf("the root has no parent")
		}
	} else {
		if n.ParentKey == "" {
			return n, fmt.Errorf("parentKey is required")
		}
		if _, ok := s.Node(n.ParentKey); !ok {
			return n, fmt.Errorf("parent %q does not exist", n.ParentKey)
		}
		if n.ParentKey == n.Key || s.IsAncestor(n.Key, n.ParentKey) {
			return n, fmt.Errorf("%q can't move under itself or its descendant %q", n.Key, n.ParentKey)
		}
	}
	n.FilterRow = strings.TrimSpace(n.FilterRow)
	if n.Synonyms, err = cleanSynonyms(n.Synonyms); err != nil {
		return n, err
	}
	if len(n.Fields) > MaxFieldsPerNode {
		return n, fmt.Errorf("at most %d fields per node", MaxFieldsPerNode)
	}
	seen := map[string]bool{}
	for i := range n.Fields {
		if seen[n.Fields[i].Key] {
			return n, fmt.Errorf("duplicate field key %q", n.Fields[i].Key)
		}
		seen[n.Fields[i].Key] = true
		if n.Fields[i], err = ValidateField(s, &n, n.Fields[i]); err != nil {
			return n, err
		}
	}
	if n.Fields == nil {
		n.Fields = []Field{}
	}
	return n, nil
}

// ValidateField checks one field of owner (which may not be in s yet) and
// returns it normalized.
func ValidateField(s *Snapshot, owner *Node, f Field) (Field, error) {
	f = f.Clone()
	path := owner.Key + "." + f.Key
	if !ValidKey(f.Key) {
		return f, fmt.Errorf("field key %q must be lowercase letters, digits and _ (start with a letter, ≤ 48)", f.Key)
	}
	var err error
	if f.Name, f.Description, err = checkText(f.Name, f.Description); err != nil {
		return f, fmt.Errorf("%s: %w", path, err)
	}
	bad := func(format string, a ...any) (Field, error) {
		return f, fmt.Errorf("%s: %s", path, fmt.Sprintf(format, a...))
	}
	if !KnownFieldType(f.Type) {
		return bad("unknown type %q", f.Type)
	}
	f.FilterRow = strings.TrimSpace(f.FilterRow)

	// Unit: number/range only.
	if f.Unit != nil {
		if f.Type != TypeNumber && f.Type != TypeRange {
			return bad("only number and range fields take a unit")
		}
		if !KnownDimension(f.Unit.Family) {
			return bad("unknown unit family %q", f.Unit.Family)
		}
		for _, u := range f.Unit.Input {
			if !AcceptsUnit(f.Unit.Family, u) {
				return bad("unit %q is not a %s unit", u, f.Unit.Family)
			}
		}
		f.Unit.Input = dedupe(f.Unit.Input)
	}

	// Options: pick/multi only.
	if f.Type == TypePick || f.Type == TypeMulti {
		if len(f.Options) == 0 || len(f.Options) > MaxOptions {
			return bad("needs 1–%d options", MaxOptions)
		}
		keys := map[string]bool{}
		for i := range f.Options {
			o := &f.Options[i]
			o.Label = strings.TrimSpace(o.Label)
			if !ValidKey(o.Key) || keys[o.Key] {
				return bad("option key %q is invalid or repeated", o.Key)
			}
			if o.Label == "" || utf8.RuneCountInString(o.Label) > MaxNameLen {
				return bad("option %q needs a label of 1–%d characters", o.Key, MaxNameLen)
			}
			keys[o.Key] = true
			if o.Order <= 0 {
				o.Order = i + 1
			}
		}
	} else if len(f.Options) > 0 {
		return bad("only pick and multi fields take options")
	}

	// Ratio (D-134).
	if f.Type == TypeRatio {
		if f.Required {
			return bad("a ratio is calculated and can't be required")
		}
		if f.Ratio == nil {
			return bad("a ratio needs {top, bottom, per}")
		}
		if f.Ratio.Per <= 0 {
			return bad("ratio per must be > 0")
		}
		if err := checkRatioInput(s, owner, f.Ratio.Top, ""); err != nil {
			return bad("ratio top: %v", err)
		}
		if err := checkRatioInput(s, owner, f.Ratio.Bottom, f.Ratio.BottomUnit); err != nil {
			return bad("ratio bottom: %v", err)
		}
	} else if f.Ratio != nil {
		return bad("only ratio fields take a ratio spec")
	}

	if f.Validations, err = NormalizeValidations(&f); err != nil {
		return bad("%v", err)
	}
	return f, nil
}

// checkRatioInput: path names a numeric, non-ratio field (owner's own fields
// are resolved from owner, which may be unsaved); unit must be of its family.
func checkRatioInput(s *Snapshot, owner *Node, path, unit string) error {
	nk, fk, ok := strings.Cut(path, ".")
	if !ok {
		return fmt.Errorf("%q must be <node>.<field>", path)
	}
	var f *Field
	if nk == owner.Key {
		f, ok = owner.Field(fk)
	} else {
		_, f, ok = s.Field(path)
	}
	if !ok {
		return fmt.Errorf("field %q does not exist", path)
	}
	if f.Type != TypeNumber && f.Type != TypeArea {
		return fmt.Errorf("%q must be a number or area field", path)
	}
	if unit != "" {
		fam := fieldFamily(f)
		if fam == "" || !AcceptsUnit(fam, unit) {
			return fmt.Errorf("unit %q does not fit %q", unit, path)
		}
	}
	return nil
}

// CheckFieldUpdate enforces what an update may not change: the key, the type
// (D-130), the locked flag, and any option (options are removed only through
// the superuser delete, D-138). A locked field changes name, description and
// order only (D-140).
func CheckFieldUpdate(old, upd Field) error {
	if old.Key != upd.Key {
		return fmt.Errorf("a field key is immutable")
	}
	if old.Type != upd.Type {
		return fmt.Errorf("a field's type is immutable — delete and recreate it (D-130)")
	}
	if old.Locked != upd.Locked {
		return fmt.Errorf("locked is set by the system")
	}
	for _, o := range old.Options {
		if !upd.HasOption(o.Key) {
			return fmt.Errorf("option %q can only be removed with options/delete", o.Key)
		}
	}
	if old.Locked {
		a, b := old.Clone(), upd.Clone()
		a.Name, a.Description, a.Order = "", "", 0
		b.Name, b.Description, b.Order = "", "", 0
		if !reflect.DeepEqual(a, b) {
			return fmt.Errorf("%s is a locked system field: only name, description and order can change (D-140)", old.Key)
		}
	}
	return nil
}

// ValidateIndustry checks an industry against the tree and returns it with
// normalized conditions.
func ValidateIndustry(s *Snapshot, ind Industry) (Industry, error) {
	ind = ind.Clone()
	if !ValidKey(ind.Key) {
		return ind, fmt.Errorf("key %q must be lowercase letters, digits and _ (start with a letter, ≤ 48)", ind.Key)
	}
	var err error
	if ind.Name, _, err = checkText(ind.Name, ""); err != nil {
		return ind, err
	}
	if ind.Required, err = NormalizeConditions(s, ind.Required); err != nil {
		return ind, fmt.Errorf("required: %w", err)
	}
	if ind.Preferred, err = NormalizeConditions(s, ind.Preferred); err != nil {
		return ind, fmt.Errorf("preferred: %w", err)
	}
	if len(ind.Required)+len(ind.Preferred) == 0 {
		return ind, fmt.Errorf("an industry needs at least one rule")
	}
	return ind, nil
}

func checkText(name, desc string) (string, string, error) {
	name, desc = strings.TrimSpace(name), strings.TrimSpace(desc)
	if name == "" || utf8.RuneCountInString(name) > MaxNameLen {
		return name, desc, fmt.Errorf("name must be 1–%d characters", MaxNameLen)
	}
	if utf8.RuneCountInString(desc) > MaxDescriptionLen {
		return name, desc, fmt.Errorf("description must be at most %d characters", MaxDescriptionLen)
	}
	return name, desc, nil
}

func cleanSynonyms(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || slices.Contains(out, s) {
			continue
		}
		if utf8.RuneCountInString(s) > MaxNameLen {
			return nil, fmt.Errorf("synonym %q is longer than %d characters", s, MaxNameLen)
		}
		out = append(out, s)
	}
	if len(out) > MaxSynonyms {
		return nil, fmt.Errorf("at most %d synonyms", MaxSynonyms)
	}
	return out, nil
}
