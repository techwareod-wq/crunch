package dto

import "encoding/json"

const (
	PatchOpAdd     = "add"
	PatchOpRemove  = "remove"
	PatchOpReplace = "replace"
)

// PatchOp is one mutation against a WebEntity field.
//
// Field is the BSON dot-path of the target (e.g. "context.business_name",
// "context.key_features", "competitors").
//
// Index is required for index-based array ops (replace at index, remove at
// index). When omitted on an array op, the op applies to the whole array
// (replace = overwrite the array; remove = drop matching values).
//
// Value carries the new payload — its expected JSON shape depends on the
// target field (string, []string, Competitor, []Competitor, etc.).
type PatchOp struct {
	Op    string          `json:"op" validate:"required,oneof=add remove replace"`
	Field string          `json:"field" validate:"required"`
	Index *int            `json:"index,omitempty"`
	Value json.RawMessage `json:"value,omitempty"`
}

type PatchWebEntityRequest struct {
	WebEntityID string    `json:"webEntityId" validate:"required"`
	Ops         []PatchOp `json:"ops" validate:"dive"`
	Finalise    bool      `json:"finalise,omitempty"`
}

type PatchWebEntityResponse struct {
	WebEntityID string `json:"webEntityId"`
	Applied     int    `json:"applied"`
	Finalised   bool   `json:"finalised"`
}
