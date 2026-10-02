package domain

import "github.com/atharva-ng/crunch/internal/models"

// The attribute tree (spec 02, D-120…D-126): admin-defined nodes, each owning
// typed fields. A node is something a warehouse has (Cold storage); its
// fields describe it (temperature). Nothing about the structure is hard-coded
// except the Warehouse root and its locked fields (D-140, D-142). The stored
// shapes are in internal/models (attribute_node.go, industry.go).

// RootKey is the Warehouse root node. It is always "yes" on every warehouse.
const RootKey = "warehouse"

// Field types (D-137). Immutable after creation (D-130).
const (
	TypeBool     models.FieldType = "bool"
	TypeNumber   models.FieldType = "number"
	TypeRange    models.FieldType = "range"
	TypePick     models.FieldType = "pick"
	TypeMulti    models.FieldType = "multi"
	TypeText     models.FieldType = "text"
	TypeLongtext models.FieldType = "longtext"
	TypeMoney    models.FieldType = "money"
	TypeDate     models.FieldType = "date"
	TypeAddress  models.FieldType = "address"
	TypeLocation models.FieldType = "location"
	TypeArea     models.FieldType = "area"
	// TypeRatio is computed (top ÷ (bottom ÷ per), D-134) and read-only.
	TypeRatio models.FieldType = "ratio"
)

// KnownFieldType reports whether t is a FieldType constant.
func KnownFieldType(t models.FieldType) bool {
	switch t {
	case TypeBool, TypeNumber, TypeRange, TypePick, TypeMulti, TypeText, TypeLongtext,
		TypeMoney, TypeDate, TypeAddress, TypeLocation, TypeArea, TypeRatio:
		return true
	}
	return false
}

// numericType reports whether a field of type t has one comparable number
// (number canonical, area sq m, ratio).
func numericType(t models.FieldType) bool {
	return t == TypeNumber || t == TypeArea || t == TypeRatio
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
