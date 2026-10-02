package domain

import (
	"slices"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// The attribute tree (spec 02, D-120…D-126): admin-defined nodes, each owning
// typed fields. A node is something a warehouse has (Cold storage); its
// fields describe it (temperature). Nothing about the structure is hard-coded
// except the Warehouse root and its locked fields (D-140, D-142).

// RootKey is the Warehouse root node. It is always "yes" on every warehouse.
const RootKey = "warehouse"

// FieldType is a field's value type (D-137). Immutable after creation (D-130).
type FieldType string

const (
	TypeBool     FieldType = "bool"
	TypeNumber   FieldType = "number"
	TypeRange    FieldType = "range"
	TypePick     FieldType = "pick"
	TypeMulti    FieldType = "multi"
	TypeText     FieldType = "text"
	TypeLongtext FieldType = "longtext"
	TypeMoney    FieldType = "money"
	TypeDate     FieldType = "date"
	TypeAddress  FieldType = "address"
	TypeLocation FieldType = "location"
	TypeArea     FieldType = "area"
	// TypeRatio is computed (top ÷ (bottom ÷ per), D-134) and read-only.
	TypeRatio FieldType = "ratio"
)

// KnownFieldType reports whether t is a FieldType constant.
func KnownFieldType(t FieldType) bool {
	switch t {
	case TypeBool, TypeNumber, TypeRange, TypePick, TypeMulti, TypeText, TypeLongtext,
		TypeMoney, TypeDate, TypeAddress, TypeLocation, TypeArea, TypeRatio:
		return true
	}
	return false
}

// numericType reports whether a field of type t has one comparable number
// (number canonical, area sq m, ratio).
func numericType(t FieldType) bool {
	return t == TypeNumber || t == TypeArea || t == TypeRatio
}

// UnitSpec pins a number/range field to a unit family. Stored values are in
// the family's canonical unit; Input lists the units the admin form offers.
type UnitSpec struct {
	Family Dimension `bson:"family"          json:"family"`
	Input  []string  `bson:"input,omitempty" json:"input,omitempty"`
}

// Option is one choice of a pick/multi field. Values link by Key; an option
// in use can't be deleted (D-138).
type Option struct {
	Key   string `bson:"key"   json:"key"`
	Label string `bson:"label" json:"label"`
	Order int    `bson:"order" json:"order"`
}

// RatioSpec defines a ratio field: Top ÷ (Bottom ÷ Per) (D-134). Top and
// Bottom are full field paths "<node>.<field>" of numeric fields.
// BottomUnit expresses Bottom in that unit of its family before dividing
// (dock ratio = doors per 10,000 sq ft: bottomUnit "sqft"); empty = canonical.
type RatioSpec struct {
	Top        string  `bson:"top"                   json:"top"`
	Bottom     string  `bson:"bottom"                json:"bottom"`
	Per        float64 `bson:"per"                   json:"per"`
	BottomUnit string  `bson:"bottom_unit,omitempty" json:"bottomUnit,omitempty"`
}

// Validation is one entry of a field's validation list (D-137). Kind names a
// registry entry (validators.go); Value holds its parameter. Message, when
// set, replaces the default error text shown to editors.
type Validation struct {
	Kind    string `bson:"kind"              json:"kind"`
	Value   any    `bson:"value"             json:"value"`
	Message string `bson:"message,omitempty" json:"message,omitempty"`
}

// Field is one typed value a node carries. Its full path is
// "<node>.<field>".
type Field struct {
	Key         string    `bson:"key"                   json:"key"`
	Name        string    `bson:"name"                  json:"name"`
	Description string    `bson:"description,omitempty" json:"description,omitempty"`
	Order       int       `bson:"order"                 json:"order"`
	Type        FieldType `bson:"type"                  json:"type"`
	// Required: the node can be "yes" on a submitted revision only with a
	// real value here (D-124). Never on a ratio.
	Required bool `bson:"required" json:"required"`
	// Locked: a root system field. Rename, re-describe and reorder only
	// (D-140). Set by the boot bootstrap, never through the API.
	Locked      bool         `bson:"locked"                json:"locked"`
	Unit        *UnitSpec    `bson:"unit,omitempty"        json:"unit,omitempty"`
	Options     []Option     `bson:"options,omitempty"     json:"options,omitempty"`
	Ratio       *RatioSpec   `bson:"ratio,omitempty"       json:"ratio,omitempty"`
	Validations []Validation `bson:"validations,omitempty" json:"validations,omitempty"`
	Public      bool         `bson:"public"                json:"public"`
	Filterable  bool         `bson:"filterable"            json:"filterable"`
	FilterRow   string       `bson:"filter_row,omitempty"  json:"filterRow,omitempty"`
	FilterPos   int          `bson:"filter_pos"            json:"filterPos"`
}

// HasOption reports whether key is one of f's options.
func (f *Field) HasOption(key string) bool {
	return slices.ContainsFunc(f.Options, func(o Option) bool { return o.Key == key })
}

// Node is one `attribute_nodes` doc: a node and its fields, read and written
// together under one CAS version.
type Node struct {
	ID  primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	Key string             `bson:"key"           json:"key"`
	// ParentKey is "" only on the root.
	ParentKey   string `bson:"parent_key"            json:"parentKey"`
	Name        string `bson:"name"                  json:"name"`
	Description string `bson:"description,omitempty" json:"description,omitempty"`
	Order       int    `bson:"order"                 json:"order"`
	// System marks the root: undeletable, unmovable.
	System     bool   `bson:"system"               json:"system"`
	Public     bool   `bson:"public"               json:"public"`
	Filterable bool   `bson:"filterable"           json:"filterable"`
	FilterRow  string `bson:"filter_row,omitempty" json:"filterRow,omitempty"`
	FilterPos  int    `bson:"filter_pos"           json:"filterPos"`
	// Synonyms feed the AI search vocabulary (spec 05).
	Synonyms []string `bson:"synonyms,omitempty" json:"synonyms,omitempty"`
	Fields   []Field  `bson:"fields"             json:"fields"`
	// Version is the per-doc CAS counter (expectedVersion on update).
	Version   int       `bson:"version"    json:"version"`
	UpdatedBy string    `bson:"updated_by" json:"updatedBy"`
	UpdatedAt time.Time `bson:"updated_at" json:"updatedAt"`
}

// Field looks up one of n's fields.
func (n *Node) Field(key string) (*Field, bool) {
	for i := range n.Fields {
		if n.Fields[i].Key == key {
			return &n.Fields[i], true
		}
	}
	return nil, false
}

// Clone deep-copies n so a snapshot's node can be edited safely.
func (n Node) Clone() Node {
	n.Synonyms = slices.Clone(n.Synonyms)
	n.Fields = slices.Clone(n.Fields)
	for i := range n.Fields {
		n.Fields[i] = n.Fields[i].Clone()
	}
	return n
}

// Clone deep-copies f.
func (f Field) Clone() Field {
	f.Options = slices.Clone(f.Options)
	f.Validations = slices.Clone(f.Validations)
	if f.Unit != nil {
		u := *f.Unit
		u.Input = slices.Clone(u.Input)
		f.Unit = &u
	}
	if f.Ratio != nil {
		r := *f.Ratio
		f.Ratio = &r
	}
	return f
}

// Industry is one industry-fit rule set (D-030, D-037, D-141).
type Industry struct {
	ID        primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	Key       string             `bson:"key"           json:"key"`
	Name      string             `bson:"name"          json:"name"`
	Order     int                `bson:"order"         json:"order"`
	Required  []Condition        `bson:"required"      json:"required"`
	Preferred []Condition        `bson:"preferred"     json:"preferred"`
	Version   int                `bson:"version"       json:"version"`
	UpdatedBy string             `bson:"updated_by"    json:"updatedBy"`
	UpdatedAt time.Time          `bson:"updated_at"    json:"updatedAt"`
}

// Clone deep-copies ind.
func (ind Industry) Clone() Industry {
	ind.Required = slices.Clone(ind.Required)
	ind.Preferred = slices.Clone(ind.Preferred)
	return ind
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
