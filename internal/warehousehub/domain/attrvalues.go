package domain

import "time"

// Warehouse-side attribute data (spec 02 "Warehouse document shape"):
//
//	attributes: {
//	  warehouse:    { status: "yes", fields: { name: {v: "…"}, total_area: {v: {…}} } },
//	  cold_storage: { status: "yes", fields: { temperature: {v: -18, raw: {…}}, humidity: null } },
//	  temp_control: { status: "unknown" },
//	  // hazmat_storage absent → "no" (D-133)
//	}

// NodeStatus is a node's state on one warehouse (D-121). Only yes and
// unknown are stored; absent means no (D-133).
type NodeStatus string

const (
	StatusYes     NodeStatus = "yes"
	StatusNo      NodeStatus = "no"
	StatusUnknown NodeStatus = "unknown"
)

// ValueSource records where a value came from (D-031).
type ValueSource string

const (
	SourceSiteVisit ValueSource = "site_visit"
	SourceOwner     ValueSource = "owner"
	SourceDocument  ValueSource = "document"
	SourceOther     ValueSource = "other"
)

// FieldValue is one field's value. V holds the canonical value; its Go type
// depends on the field type (see CanonicalizeValue). Values read back from
// Mongo arrive BSON-generic (int32, primitive.A, primitive.D…); the accessors
// in values.go accept every shape.
type FieldValue struct {
	V   any       `bson:"v"             json:"v"`
	Raw *RawValue `bson:"raw,omitempty" json:"raw,omitempty"`
	// Optional provenance (D-031).
	Source     ValueSource `bson:"source,omitempty"      json:"source,omitempty"`
	VerifiedAt *time.Time  `bson:"verified_at,omitempty" json:"verifiedAt,omitempty"`
	By         string      `bson:"by,omitempty"          json:"by,omitempty"`
	At         *time.Time  `bson:"at,omitempty"          json:"at,omitempty"`
}

// RawValue is a number/range value as entered, before unit conversion.
type RawValue struct {
	Value any    `bson:"value"          json:"value"`
	Unit  string `bson:"unit,omitempty" json:"unit,omitempty"`
}

// NodeState is one node's entry on a warehouse. In Fields a nil value is a
// stored null: "not provided" on an optional field (D-125). An absent key on
// an optional field reads the same; on a required field both are "missing".
type NodeState struct {
	Status NodeStatus             `bson:"status"           json:"status"`
	Fields map[string]*FieldValue `bson:"fields,omitempty" json:"fields,omitempty"`
}

// Attributes is a warehouse's attribute data keyed by node key.
type Attributes map[string]NodeState

// Value returns the stored value of node.field (nil when absent or null).
func (a Attributes) Value(node, field string) *FieldValue {
	return a[node].Fields[field]
}

// Range is a min–max value (e.g. a temperature range).
type Range struct {
	Min float64 `bson:"min" json:"min"`
	Max float64 `bson:"max" json:"max"`
}

// Money is an amount in minor units of an ISO 4217 currency. No FX (D-058).
// OnRequest marks "price on request": Amount is then 0 and ignored.
type Money struct {
	Amount    int64  `bson:"amount"               json:"amount"`
	Currency  string `bson:"currency"             json:"currency"`
	Basis     string `bson:"basis,omitempty"      json:"basis,omitempty"`
	Period    string `bson:"period,omitempty"     json:"period,omitempty"`
	OnRequest bool   `bson:"on_request,omitempty" json:"onRequest,omitempty"`
}

// Address is a postal address. Country is ISO 3166-1 alpha-2.
type Address struct {
	Line1      string `bson:"line1"                 json:"line1"`
	Line2      string `bson:"line2,omitempty"       json:"line2,omitempty"`
	Locality   string `bson:"locality,omitempty"    json:"locality,omitempty"`
	City       string `bson:"city"                  json:"city"`
	Region     string `bson:"region,omitempty"      json:"region,omitempty"`
	PostalCode string `bson:"postal_code,omitempty" json:"postalCode,omitempty"`
	Country    string `bson:"country"               json:"country"`
}

// Location sources (D-061).
const (
	LocationGeocoded = "geocoded"
	LocationManual   = "manual"
)

// Location is a map pin.
type Location struct {
	Lat         float64 `bson:"lat"                    json:"lat"`
	Lng         float64 `bson:"lng"                    json:"lng"`
	Accuracy    string  `bson:"accuracy,omitempty"     json:"accuracy,omitempty"`
	Source      string  `bson:"source"                 json:"source"`
	PlaceID     string  `bson:"place_id,omitempty"     json:"placeId,omitempty"`
	AddressHash string  `bson:"address_hash,omitempty" json:"addressHash,omitempty"`
}
