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
	FieldWarehouseStatus  = "status"
	FieldFitRulesVersion  = "fit_rules_version"
	FieldLiveAttributes   = "live.attributes"
	FieldLiveTotalAreaSqm = "live.total_area.sqm"
)

// Area is an area as entered plus its canonical sq m (Content.totalArea /
// availableArea).
type Area struct {
	Value float64 `bson:"value" json:"value"`
	Unit  string  `bson:"unit"  json:"unit"`
	Sqm   float64 `bson:"sqm"   json:"sqm"`
}

// LiveEvalDoc is what recompute reads from a `warehouses` doc: the live
// content's answers and total area. Content (03) must keep these bson names.
type LiveEvalDoc struct {
	ID   primitive.ObjectID `bson:"_id"`
	Live *struct {
		Attributes map[string]Answer `bson:"attributes"`
		TotalArea  *Area             `bson:"total_area"`
	} `bson:"live"`
}

// EvalInput builds the evaluator input from the live content.
func (d LiveEvalDoc) EvalInput() EvalInput {
	if d.Live == nil {
		return EvalInput{}
	}
	in := EvalInput{Attributes: d.Live.Attributes}
	if d.Live.TotalArea != nil {
		in.TotalAreaSqm = d.Live.TotalArea.Sqm
	}
	return in
}
