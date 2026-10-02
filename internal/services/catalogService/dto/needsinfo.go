package dto

import "github.com/atharva-ng/crunch/internal/models"

// Needs-info kinds (spec 02 Needs-info queue).
const (
	NeedsInfoNode  = "node"  // an unknown node
	NeedsInfoField = "field" // a required field missing on a yes node (D-127)
)

// NeedsInfoKey is one GET /v1/admin/needs-info/summary row.
type NeedsInfoKey struct {
	Key   string `json:"key"`
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

// NeedsInfoWarehouse is one GET /v1/admin/needs-info/list row.
type NeedsInfoWarehouse struct {
	ID           string        `json:"id"`
	ShortID      string        `json:"shortId"`
	Name         string        `json:"name"`
	City         string        `json:"city,omitempty"`
	NeedsInfo    []string      `json:"needsInfo"`
	OpenRevision *OpenRevision `json:"openRevision"`
}

// OpenRevision is a warehouse's open draft / in-review revision.
type OpenRevision struct {
	ID    string               `json:"id"`
	State models.RevisionState `json:"state"`
}
