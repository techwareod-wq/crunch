package domain

import (
	"time"
)

// Tri is three-valued logic for conditions and applicability.
type Tri int8

const (
	TriFalse   Tri = -1
	TriUnknown Tri = 0
	TriTrue    Tri = 1
)

func and3(ts ...Tri) Tri {
	out := TriTrue
	for _, t := range ts {
		if t == TriFalse {
			return TriFalse
		}
		if t == TriUnknown {
			out = TriUnknown
		}
	}
	return out
}

func or3(ts ...Tri) Tri {
	out := TriFalse
	for _, t := range ts {
		if t == TriTrue {
			return TriTrue
		}
		if t == TriUnknown {
			out = TriUnknown
		}
	}
	return out
}

// EvalInput is what the evaluator reads from a warehouse's content.
type EvalInput struct {
	Attributes map[string]Answer
	// TotalAreaSqm is the fixed total-area field; 0 means unknown.
	TotalAreaSqm float64
}

// CalcFn fills a calculated attribute. ok=false means the result is unknown.
type CalcFn func(answers map[string]Answer, in EvalInput) (v float64, ok bool)

// CalcFns is the calculated-attribute registry (D-041), keyed by CalcSpec.Fn.
var CalcFns = map[string]CalcFn{
	"dock_ratio": dockRatio,
}

// dockRatio = dock doors per 10,000 sq ft of TOTAL area (D-041).
func dockRatio(answers map[string]Answer, in EvalInput) (float64, bool) {
	a, ok := answers["dock_doors"]
	if !ok || a.Status != StatusKnown {
		return 0, false
	}
	doors, ok := asFloat(a.V)
	if !ok || in.TotalAreaSqm <= 0 {
		return 0, false
	}
	return doors / (SqftFromSqm(in.TotalAreaSqm) / 10000), true
}

// NumFact is one known number in the search projection.
type NumFact struct {
	K string  `bson:"k" json:"k"`
	V float64 `bson:"v" json:"v"`
}

// Projection is the search projection written onto the live `warehouses`
// doc (spec 02 §6) and read by search (04). Slices are never nil so the
// stored arrays are always present.
type Projection struct {
	// Chips: "<key>:yes" for a known true bool, "<key>:<value>" for a pick
	// value and for each multi value. Filterable + public attributes only.
	Chips []string `bson:"chips" json:"chips"`
	// Unk: filterable + public attributes that may apply (applicability true
	// or unknown) and have no known answer — the "include unverified" set.
	Unk []string `bson:"unk" json:"unk"`
	// Nums: known numbers in canonical units; ranges emit <key>_min/<key>_max.
	Nums []NumFact `bson:"nums" json:"nums"`
	// Fit: "<industry>:<F|P|U|N>" per non-retired industry.
	Fit []string `bson:"fit" json:"fit"`
	// NeedsInfo: applicable, answerable attributes with no answer (D-039).
	NeedsInfo       []string  `bson:"needs_info"        json:"needsInfo"`
	NeedsInfoCount  int       `bson:"needs_info_count"  json:"needsInfoCount"`
	FitRulesVersion int64     `bson:"fit_rules_version" json:"fitRulesVersion"`
	EvaluatedAt     time.Time `bson:"evaluated_at"      json:"evaluatedAt"`
}

// Result is the full evaluation of one warehouse.
type Result struct {
	// Calc holds the known calculated values.
	Calc map[string]float64 `json:"calc"`
	// Applicable is the applicability of every attribute (groups excluded).
	Applicable map[string]Tri `json:"-"`
	// NeedsInfo lists, in tree order, the keys to ask about.
	NeedsInfo []string `json:"needsInfo"`
	// Fit is the verdict per non-retired industry.
	Fit        map[string]Verdict `json:"fit"`
	Projection Projection         `json:"-"`
}

// IsApplicable reports whether key definitely applies.
func (r *Result) IsApplicable(key string) bool { return r.Applicable[key] == TriTrue }

// Evaluate runs the attribute engine over one warehouse (spec 02 Evaluator).
// Pure: no IO, deterministic for (snapshot, input, now).
func Evaluate(s *Snapshot, in EvalInput, now time.Time) Result {
	e := &evaluator{
		s:        s,
		answers:  make(map[string]Answer, len(in.Attributes)+2),
		app:      map[string]Tri{},
		visiting: map[string]bool{},
	}
	for k, a := range in.Attributes {
		e.answers[k] = a
	}
	res := Result{
		Calc:       map[string]float64{},
		Applicable: map[string]Tri{},
		NeedsInfo:  []string{},
		Fit:        map[string]Verdict{},
	}

	// 1. Calculated values overwrite anything stored under their key.
	for _, d := range s.Defs {
		if d.Type != TypeCalculated || d.Retired || d.Calc == nil {
			continue
		}
		delete(e.answers, d.Key)
		fn, ok := CalcFns[d.Calc.Fn]
		if !ok {
			continue
		}
		if v, ok := fn(in.Attributes, in); ok {
			res.Calc[d.Key] = v
			e.answers[d.Key] = Answer{Status: StatusKnown, V: v}
		}
	}

	proj := Projection{
		Chips: []string{}, Unk: []string{}, Nums: []NumFact{}, Fit: []string{},
		FitRulesVersion: s.Version, EvaluatedAt: now,
	}

	// 2–3. Applicability, needsInfo and the chip/num projection, in tree order.
	for i := range s.Defs {
		d := &s.Defs[i]
		if d.IsGroup() {
			continue
		}
		app := e.applicable(d.Key)
		res.Applicable[d.Key] = app
		a, has := e.answers[d.Key]
		known := has && a.Status == StatusKnown
		unanswered := !has || a.Status == StatusUnknown || a.Status == ""

		if app == TriTrue && unanswered && d.Type != TypeCalculated && !d.Retired {
			res.NeedsInfo = append(res.NeedsInfo, d.Key)
		}
		if !d.Filterable || !d.Public || d.Retired || app == TriFalse {
			continue
		}
		if app == TriUnknown || unanswered {
			proj.Unk = append(proj.Unk, d.Key)
			continue
		}
		if known {
			proj.Chips, proj.Nums = project(d, a.V, proj.Chips, proj.Nums)
		}
	}

	// 5. Verdicts.
	for i := range s.Industries {
		ind := &s.Industries[i]
		if ind.Retired {
			continue
		}
		v := e.verdict(ind)
		res.Fit[ind.Key] = v
		proj.Fit = append(proj.Fit, ind.Key+":"+string(v))
	}

	proj.NeedsInfo = res.NeedsInfo
	proj.NeedsInfoCount = len(res.NeedsInfo)
	res.Projection = proj
	return res
}

// project appends a known answer's chips / numeric facts.
func project(d *AttrDef, v any, chips []string, nums []NumFact) ([]string, []NumFact) {
	switch d.Type {
	case TypeBool:
		if b, ok := asBool(v); ok && b {
			chips = append(chips, d.Key+":yes")
		}
	case TypePick:
		if s, ok := asString(v); ok && s != "" {
			chips = append(chips, d.Key+":"+s)
		}
	case TypeMulti:
		if ss, ok := asStrings(v); ok {
			for _, s := range ss {
				chips = append(chips, d.Key+":"+s)
			}
		}
	case TypeNumber, TypeCalculated:
		if f, ok := asFloat(v); ok {
			nums = append(nums, NumFact{K: d.Key, V: f})
		}
	case TypeRange:
		if r, ok := asRange(v); ok {
			nums = append(nums, NumFact{K: d.Key + "_min", V: r.Min}, NumFact{K: d.Key + "_max", V: r.Max})
		}
	}
	return chips, nums
}

type evaluator struct {
	s        *Snapshot
	answers  map[string]Answer // stored answers + calculated values
	app      map[string]Tri
	visiting map[string]bool
}

// applicable: not retired AND parent-rule AND/OR appliesWhen (spec 02 §2,
// D-032). An attribute parent contributes "parent applies and is truthy";
// appliesWhen op "all" ANDs its conditions in, op "any" ORs them in (so
// temperature tracking = cold storage OR GDP). A group parent only passes on
// its own applicability and always ANDs.
func (e *evaluator) applicable(key string) Tri {
	if t, ok := e.app[key]; ok {
		return t
	}
	d, ok := e.s.Def(key)
	if !ok || d.Retired {
		return TriFalse
	}
	if e.visiting[key] { // dependency cycle: refuse to decide
		return TriUnknown
	}
	e.visiting[key] = true
	defer delete(e.visiting, key)

	var parentTri Tri = TriTrue
	parentIsAttr := false
	if d.ParentKey != "" {
		if p, ok := e.s.Def(d.ParentKey); ok {
			if p.IsGroup() {
				parentTri = e.applicable(p.Key)
			} else {
				parentIsAttr = true
				parentTri = e.parentYes(p)
			}
		}
	}

	out := parentTri
	if n := d.AppliesWhen; n != nil && len(n.Conds) > 0 {
		terms := make([]Tri, 0, len(n.Conds)+1)
		for _, c := range n.Conds {
			terms = append(terms, e.cond(c))
		}
		switch {
		case n.Op == OpAny && parentIsAttr:
			out = or3(append(terms, parentTri)...)
		case n.Op == OpAny:
			out = and3(parentTri, or3(terms...))
		default:
			out = and3(append(terms, parentTri)...)
		}
	}
	e.app[key] = out
	return out
}

// parentYes: the parent applies and its answer is known and truthy.
func (e *evaluator) parentYes(p *AttrDef) Tri {
	pa := e.applicable(p.Key)
	if pa != TriTrue {
		return pa
	}
	a, has := e.answers[p.Key]
	switch {
	case !has || a.Status == StatusUnknown || a.Status == "":
		return TriUnknown
	case a.Status == StatusNA:
		return TriFalse
	}
	if truthy(p.Type, a.V) {
		return TriTrue
	}
	return TriFalse
}

func truthy(t AttrType, v any) bool {
	switch t {
	case TypeBool:
		b, _ := asBool(v)
		return b
	case TypePick, TypeText:
		s, _ := asString(v)
		return s != ""
	case TypeMulti:
		ss, _ := asStrings(v)
		return len(ss) > 0
	default: // number / range / money / calculated: answered = present
		return v != nil
	}
}

// cond evaluates one condition in three-valued logic (spec 02 §4).
func (e *evaluator) cond(c Condition) Tri {
	d, ok := e.s.Def(c.Attr)
	if !ok || d.Retired || d.IsGroup() {
		return TriFalse
	}
	switch e.applicable(c.Attr) {
	case TriFalse:
		return TriFalse
	case TriUnknown:
		return TriUnknown
	}
	a, has := e.answers[c.Attr]
	switch {
	case !has || a.Status == StatusUnknown || a.Status == "":
		return TriUnknown
	case a.Status == StatusNA: // D-038: N/A never meets a rule
		return TriFalse
	}
	if compare(d.Type, a.V, c) {
		return TriTrue
	}
	return TriFalse
}

// verdict applies D-033: Not fit if a required rule fails; Unverified if a
// required OR preferred rule is unknown; Fit if every preferred rule holds;
// else Partial.
func (e *evaluator) verdict(ind *Industry) Verdict {
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
