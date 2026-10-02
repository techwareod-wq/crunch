package domain

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// NodeKind separates layout groups from answerable attributes.
type NodeKind string

const (
	KindGroup     NodeKind = "group"     // layout only; never answered
	KindAttribute NodeKind = "attribute" // a fact a warehouse can have an answer for
)

// AttrRole labels an attribute (no behaviour attached).
type AttrRole string

const (
	RoleParameter AttrRole = "parameter"
	RoleProperty  AttrRole = "property"
)

// AttrType is an attribute's answer type. There is no attestation type
// (D-031): certificates are plain bool.
type AttrType string

const (
	TypeBool       AttrType = "bool"
	TypeNumber     AttrType = "number"
	TypeRange      AttrType = "range"
	TypePick       AttrType = "pick"
	TypeMulti      AttrType = "multi"
	TypeText       AttrType = "text"
	TypeMoney      AttrType = "money"
	TypeCalculated AttrType = "calculated"
)

// KnownType reports whether t is an AttrType constant.
func KnownType(t AttrType) bool {
	switch t {
	case TypeBool, TypeNumber, TypeRange, TypePick, TypeMulti, TypeText, TypeMoney, TypeCalculated:
		return true
	}
	return false
}

// UnitSpec pins a number/range attribute to a dimension. Stored values are
// canonical; Input lists the units the admin form offers.
type UnitSpec struct {
	Dimension Dimension `bson:"dimension"           json:"dimension"`
	Input     []string  `bson:"input,omitempty"     json:"input,omitempty"`
	Canonical string    `bson:"canonical,omitempty" json:"canonical,omitempty"`
}

// AllowedValue is one option of a pick/multi attribute. Options are never
// deleted (answers link by key); they are retired.
type AllowedValue struct {
	Key     string `bson:"key"     json:"key"`
	Label   string `bson:"label"   json:"label"`
	Order   int    `bson:"order"   json:"order"`
	Retired bool   `bson:"retired" json:"retired"`
}

// CalcSpec names the Go function that fills a calculated attribute (D-041).
type CalcSpec struct {
	Fn string `bson:"fn" json:"fn"`
}

// AttrDef is one node of the attribute tree (attribute_definitions).
// Key is stable and immutable: answers and rules link by key.
type AttrDef struct {
	ID          primitive.ObjectID `bson:"_id,omitempty"            json:"id"`
	Key         string             `bson:"key"                      json:"key"`
	Kind        NodeKind           `bson:"kind"                     json:"kind"`
	Role        AttrRole           `bson:"role,omitempty"           json:"role,omitempty"`
	Type        AttrType           `bson:"type,omitempty"           json:"type,omitempty"`
	Name        string             `bson:"name"                     json:"name"`
	Description string             `bson:"description,omitempty"    json:"description,omitempty"`
	// ParentKey is "" for a root node.
	ParentKey     string         `bson:"parent_key"               json:"parentKey"`
	Order         int            `bson:"order"                    json:"order"`
	Unit          *UnitSpec      `bson:"unit,omitempty"           json:"unit,omitempty"`
	AllowedValues []AllowedValue `bson:"allowed_values,omitempty" json:"allowedValues,omitempty"`
	Calc          *CalcSpec      `bson:"calc,omitempty"           json:"calc,omitempty"`
	// AppliesWhen is combined with the implicit "parent is yes" condition
	// (D-032): op "all" ANDs it in, op "any" ORs it in (see Evaluate).
	AppliesWhen *CondNode `bson:"applies_when,omitempty" json:"appliesWhen,omitempty"`
	Filterable  bool      `bson:"filterable"             json:"filterable"`
	FilterRow   string    `bson:"filter_row,omitempty"   json:"filterRow,omitempty"`
	FilterPos   int       `bson:"filter_pos"             json:"filterPos"`
	Public      bool      `bson:"public"                 json:"public"`
	// Synonyms feed the AI search vocabulary (spec 05).
	Synonyms  []string   `bson:"synonyms,omitempty"   json:"synonyms,omitempty"`
	Retired   bool       `bson:"retired"              json:"retired"`
	RetiredAt *time.Time `bson:"retired_at,omitempty" json:"retiredAt,omitempty"`
	// Version is the per-doc CAS counter (expectedVersion on update).
	Version   int       `bson:"version"    json:"version"`
	UpdatedBy string    `bson:"updated_by" json:"updatedBy"`
	UpdatedAt time.Time `bson:"updated_at" json:"updatedAt"`
}

// IsGroup reports whether d is a layout group.
func (d *AttrDef) IsGroup() bool { return d.Kind == KindGroup }

// HasAllowedValue reports whether key is one of d's options (retired or not).
func (d *AttrDef) HasAllowedValue(key string) bool {
	for _, av := range d.AllowedValues {
		if av.Key == key {
			return true
		}
	}
	return false
}

// Industry is one industry-fit rule set (D-030, D-037). Bonded is an ordinary
// industry.
type Industry struct {
	ID        primitive.ObjectID `bson:"_id,omitempty"        json:"id"`
	Key       string             `bson:"key"                  json:"key"`
	Name      string             `bson:"name"                 json:"name"`
	Order     int                `bson:"order"                json:"order"`
	Required  []Condition        `bson:"required"             json:"required"`
	Preferred []Condition        `bson:"preferred"            json:"preferred"`
	Retired   bool               `bson:"retired"              json:"retired"`
	RetiredAt *time.Time         `bson:"retired_at,omitempty" json:"retiredAt,omitempty"`
	Version   int                `bson:"version"              json:"version"`
	UpdatedBy string             `bson:"updated_by"           json:"updatedBy"`
	UpdatedAt time.Time          `bson:"updated_at"           json:"updatedAt"`
}

// References reports whether any of ind's rules names one of keys.
func (ind *Industry) References(keys map[string]bool) bool {
	for _, set := range [][]Condition{ind.Required, ind.Preferred} {
		for _, c := range set {
			if keys[c.Attr] {
				return true
			}
		}
	}
	return false
}

// Verdict is a warehouse's fit for one industry (D-033). Stored in the
// search projection as "<industry>:<verdict>".
type Verdict string

const (
	VerdictFit        Verdict = "F"
	VerdictPartial    Verdict = "P"
	VerdictUnverified Verdict = "U"
	VerdictNotFit     Verdict = "N"
)

// AnswerStatus separates "no" from "nobody checked" (Attribute Tree §4).
type AnswerStatus string

const (
	StatusKnown   AnswerStatus = "known"
	StatusUnknown AnswerStatus = "unknown"
	StatusNA      AnswerStatus = "na"
)

// AnswerSource records where a known answer came from (D-031).
type AnswerSource string

const (
	SourceSiteVisit AnswerSource = "site_visit"
	SourceOwner     AnswerSource = "owner"
	SourceDocument  AnswerSource = "document"
	SourceOther     AnswerSource = "other"
)

// Answer is one attribute answer inside a revision's / live doc's content
// (`attributes.<key>`). An absent key counts as unknown.
//
// V holds the canonical value: bool | float64 | string | []string | Range |
// Money. Numbers are canonical units (sq m, °C, t/m², MT, m); Raw keeps what
// the editor typed. Values read back from Mongo arrive as BSON-generic types
// (int32, primitive.A, primitive.D…); the evaluator's accessors accept both.
type Answer struct {
	Status     AnswerStatus `bson:"status"                json:"status"`
	V          any          `bson:"v,omitempty"           json:"v,omitempty"`
	Raw        *RawValue    `bson:"raw,omitempty"         json:"raw,omitempty"`
	Source     AnswerSource `bson:"source,omitempty"      json:"source,omitempty"`
	VerifiedAt *time.Time   `bson:"verified_at,omitempty" json:"verifiedAt,omitempty"`
	By         string       `bson:"by,omitempty"          json:"by,omitempty"`
	At         *time.Time   `bson:"at,omitempty"          json:"at,omitempty"`
}

// RawValue is a number/range answer as entered, before unit conversion.
type RawValue struct {
	Value any    `bson:"value"          json:"value"`
	Unit  string `bson:"unit,omitempty" json:"unit,omitempty"`
}

// Range is a min–max answer (e.g. temperature range).
type Range struct {
	Min float64 `bson:"min" json:"min"`
	Max float64 `bson:"max" json:"max"`
}

// Money is an amount in minor units of an ISO 4217 currency. No FX (D-058).
type Money struct {
	Amount   int64  `bson:"amount"           json:"amount"`
	Currency string `bson:"currency"         json:"currency"`
	Basis    string `bson:"basis,omitempty"  json:"basis,omitempty"`
	Period   string `bson:"period,omitempty" json:"period,omitempty"`
}
