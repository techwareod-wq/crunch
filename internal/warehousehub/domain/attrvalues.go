package domain

import "github.com/atharva-ng/crunch/internal/models"

// Warehouse-side attribute data (spec 02 "Warehouse document shape"); the
// stored shapes (Attributes, NodeState, FieldValue, Money, Address, …) are in
// internal/models/warehouse_values.go.

// Node states (D-121). Only yes and unknown are stored; absent means no
// (D-133).
const (
	StatusYes     models.NodeStatus = "yes"
	StatusNo      models.NodeStatus = "no"
	StatusUnknown models.NodeStatus = "unknown"
)

// Value sources (D-031).
const (
	SourceSiteVisit models.ValueSource = "site_visit"
	SourceOwner     models.ValueSource = "owner"
	SourceDocument  models.ValueSource = "document"
	SourceOther     models.ValueSource = "other"
)

// Location sources (D-061).
const (
	LocationGeocoded = "geocoded"
	LocationManual   = "manual"
)
