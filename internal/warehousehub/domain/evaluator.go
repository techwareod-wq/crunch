package domain

import (
	"github.com/atharva-ng/crunch/internal/models"
	"time"
)

// Tri is three-valued logic for conditions.
type Tri int8

const (
	TriFalse   Tri = -1
	TriUnknown Tri = 0
	TriTrue    Tri = 1
)

// Result is the full evaluation of one warehouse.
type Result struct {
	// State is every node's effective state.
	State map[string]models.NodeStatus `json:"state"`
	// Ratios holds the computable ratio values by field path.
	Ratios map[string]float64 `json:"ratios"`
	// UnknownNodes and MissingRequired, in tree order.
	UnknownNodes    []string `json:"unknownNodes"`
	MissingRequired []string `json:"missingRequired"`
	// NeedsInfo = UnknownNodes + MissingRequired.
	NeedsInfo []string `json:"needsInfo"`
	// Fit is the verdict per industry.
	Fit        map[string]Verdict `json:"fit"`
	Projection models.Projection  `json:"-"`
}

// Evaluate runs the attribute engine over one warehouse (spec 02 Evaluator).
// Pure: no IO, deterministic for (snapshot, attributes, now).
func Evaluate(s *Snapshot, a models.Attributes, now time.Time) Result {
	e := &evaluator{s: s, a: a, eff: effectiveStates(s, a), ratios: map[string]float64{}}
	res := Result{
		State:           e.eff,
		Ratios:          e.ratios,
		UnknownNodes:    []string{},
		MissingRequired: []string{},
		Fit:             map[string]Verdict{},
	}

	// 2. Ratios on yes nodes (D-134).
	for i := range s.Nodes {
		n := &s.Nodes[i]
		if e.eff[n.Key] != StatusYes {
			continue
		}
		for j := range n.Fields {
			f := &n.Fields[j]
			if f.Type == TypeRatio && f.Ratio != nil {
				if v, ok := e.ratio(f.Ratio); ok {
					e.ratios[n.Key+"."+f.Key] = v
				}
			}
		}
	}

	proj := models.Projection{
		Chips: []string{}, Unk: []string{}, Nums: []models.NumFact{}, Fit: []string{},
		FitRulesVersion: s.Version, EvaluatedAt: now,
	}

	// 3 + 6. Needs-info and the chip/num projection, in tree order. Every
	// node and field is projected: public / filterable are applied when
	// searching, so admin search can query staff-only attributes.
	for i := range s.Nodes {
		n := &s.Nodes[i]
		switch e.eff[n.Key] {
		case StatusUnknown:
			res.UnknownNodes = append(res.UnknownNodes, n.Key)
			proj.Unk = append(proj.Unk, n.Key)
		case StatusYes:
			if n.Key != RootKey {
				proj.Chips = append(proj.Chips, n.Key)
			}
			for j := range n.Fields {
				f := &n.Fields[j]
				path := n.Key + "." + f.Key
				v, present := e.value(n.Key, f)
				missing := !present && (f.Required || f.Type == TypeRatio)
				if missing && f.Required {
					res.MissingRequired = append(res.MissingRequired, path)
				}
				if !Projectable(f.Type) {
					continue
				}
				switch {
				case present:
					proj.Chips, proj.Nums = project(f, path, v, proj.Chips, proj.Nums)
				case missing:
					proj.Unk = append(proj.Unk, path)
				}
			}
		}
	}
	res.NeedsInfo = append(append([]string{}, res.UnknownNodes...), res.MissingRequired...)

	// 5. Verdicts.
	for i := range s.Industries {
		ind := &s.Industries[i]
		v := e.verdict(ind)
		res.Fit[ind.Key] = v
		proj.Fit = append(proj.Fit, ind.Key+":"+string(v))
	}

	proj.NeedsInfo = res.NeedsInfo
	proj.NeedsInfoCount = len(res.NeedsInfo)
	res.Projection = proj
	return res
}

// Projectable reports whether a field of type t lands in the search
// projection (chips or nums).
func Projectable(t models.FieldType) bool {
	switch t {
	case TypeBool, TypePick, TypeMulti, TypeNumber, TypeArea, TypeRatio, TypeRange:
		return true
	}
	return false
}

// project appends a present value's chips / numeric facts.
func project(f *models.AttributeField, path string, v any, chips []string, nums []models.NumFact) ([]string, []models.NumFact) {
	switch f.Type {
	case TypeBool:
		if b, ok := asBool(v); ok && b {
			chips = append(chips, path)
		}
	case TypePick:
		if s, ok := asString(v); ok && s != "" {
			chips = append(chips, path+":"+s)
		}
	case TypeMulti:
		if ss, ok := asStrings(v); ok {
			for _, s := range ss {
				chips = append(chips, path+":"+s)
			}
		}
	case TypeNumber, TypeArea, TypeRatio:
		if x, ok := numberOf(f.Type, v); ok {
			nums = append(nums, models.NumFact{K: path, V: x})
		}
	case TypeRange:
		if r, ok := asRange(v); ok {
			nums = append(nums, models.NumFact{K: path + "_min", V: r.Min}, models.NumFact{K: path + "_max", V: r.Max})
		}
	}
	return chips, nums
}

type evaluator struct {
	s      *Snapshot
	a      models.Attributes
	eff    map[string]models.NodeStatus
	ratios map[string]float64
}

// value returns node.field's value: the computed ratio, or the stored value
// (nil/absent = not present).
func (e *evaluator) value(node string, f *models.AttributeField) (any, bool) {
	if f.Type == TypeRatio {
		v, ok := e.ratios[node+"."+f.Key]
		return v, ok
	}
	fv := e.a.Value(node, f.Key)
	if fv == nil || fv.V == nil {
		return nil, false
	}
	return fv.V, true
}

// ratio computes top ÷ (bottom ÷ per). Unknown when either input's node isn't
// yes, a value is missing, or the bottom is zero.
func (e *evaluator) ratio(r *models.RatioSpec) (float64, bool) {
	top, ok1 := e.input(r.Top, "")
	bottom, ok2 := e.input(r.Bottom, r.BottomUnit)
	if !ok1 || !ok2 || bottom <= 0 || r.Per <= 0 {
		return 0, false
	}
	return top / (bottom / r.Per), true
}

func (e *evaluator) input(path, unit string) (float64, bool) {
	n, f, ok := e.s.Field(path)
	if !ok || f.Type == TypeRatio || e.eff[n.Key] != StatusYes {
		return 0, false
	}
	fv := e.a.Value(n.Key, f.Key)
	if fv == nil {
		return 0, false
	}
	x, ok := numberOf(f.Type, fv.V)
	if !ok {
		return 0, false
	}
	if unit != "" {
		var err error
		if x, err = FromCanonical(fieldFamily(f), x, unit); err != nil {
			return 0, false
		}
	}
	return x, true
}

// cond evaluates one condition (spec 02 Evaluator §4):
//   - is_yes: yes → T, no → F, unknown → U;
//   - field cmp: node no → F; node unknown → U; required field missing
//     (D-127) or ratio not computable → U; optional null → F (D-125);
//     otherwise compare.
func (e *evaluator) cond(c models.Condition) Tri {
	n, ok := e.s.Node(c.Node)
	if !ok {
		return TriFalse
	}
	switch e.eff[n.Key] {
	case StatusNo:
		return TriFalse
	case StatusUnknown:
		return TriUnknown
	}
	if c.Cmp == CmpIsYes {
		return TriTrue
	}
	f, ok := n.Field(c.Field)
	if !ok {
		return TriFalse
	}
	v, present := e.value(n.Key, f)
	if !present {
		if f.Required || f.Type == TypeRatio {
			return TriUnknown
		}
		return TriFalse
	}
	if compare(f.Type, v, c) {
		return TriTrue
	}
	return TriFalse
}

// verdict applies D-033: Not fit if a required rule fails; Unverified if a
// required OR preferred rule is unknown; Fit if every preferred rule holds;
// else Partial.
func (e *evaluator) verdict(ind *models.Industry) Verdict {
	req := make([]Tri, 0, len(ind.Required))
	for _, c := range ind.Required {
		req = append(req, e.cond(c))
	}
	pref := make([]Tri, 0, len(ind.Preferred))
	for _, c := range ind.Preferred {
		pref = append(pref, e.cond(c))
	}
	for _, t := range req {
		if t == TriFalse {
			return VerdictNotFit
		}
	}
	for _, t := range append(req, pref...) {
		if t == TriUnknown {
			return VerdictUnverified
		}
	}
	for _, t := range pref {
		if t == TriFalse {
			return VerdictPartial
		}
	}
	return VerdictFit
}

// Completeness scores a listing for relevance (04): completeness is the share
// of fields on yes nodes that hold a value (each unknown node counts as one
// missing item; ratios count when computable), verified is the share of
// stored values carrying a verifiedAt (D-031).
func Completeness(s *Snapshot, a models.Attributes, r Result) (completeness, verified float64) {
	var total, filled, stored, checked int
	for i := range s.Nodes {
		n := &s.Nodes[i]
		switch r.State[n.Key] {
		case StatusUnknown:
			total++
		case StatusYes:
			for j := range n.Fields {
				f := &n.Fields[j]
				total++
				if f.Type == TypeRatio {
					if _, ok := r.Ratios[n.Key+"."+f.Key]; ok {
						filled++
					}
					continue
				}
				fv := a.Value(n.Key, f.Key)
				if fv == nil || fv.V == nil {
					continue
				}
				filled++
				stored++
				if fv.VerifiedAt != nil {
					checked++
				}
			}
		}
	}
	if total > 0 {
		completeness = float64(filled) / float64(total)
	}
	if stored > 0 {
		verified = float64(checked) / float64(stored)
	}
	return completeness, verified
}
