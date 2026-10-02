package domain

import "go.mongodb.org/mongo-driver/bson/primitive"

// The slice of the `warehouses` collection contract that more than one module
// relies on. The catalog module (03) owns the collection and its full model;
// the attributes module's recompute reads LiveEvalDoc and writes Projection
// through these names, so they must not drift.

// CollWarehouses is the live-listing collection (catalog-owned).
const CollWarehouses = "warehouses"

// Warehouse statuses (`warehouses.status`).
const (
	WarehouseUnpublished = "unpublished"
	WarehouseLive        = "live"
	WarehouseArchived    = "archived"
)

// Field paths on a `warehouses` doc.
const (
	FieldWarehouseStatus = "status"
	FieldFitRulesVersion = "fit_rules_version"
	FieldLiveAttributes  = "live.attributes"
)

// Area is an area value as entered plus its canonical sq m (field type
// "area", e.g. warehouse.total_area).
type Area struct {
	Value float64 `bson:"value" json:"value"`
	Unit  string  `bson:"unit"  json:"unit"`
	Sqm   float64 `bson:"sqm"   json:"sqm"`
}

// LiveEvalDoc is what recompute reads from a `warehouses` doc: the live
// content's attribute data. Content (03) must keep this bson name.
type LiveEvalDoc struct {
	ID   primitive.ObjectID `bson:"_id"`
	Live *struct {
		Attributes Attributes `bson:"attributes"`
	} `bson:"live"`
}
