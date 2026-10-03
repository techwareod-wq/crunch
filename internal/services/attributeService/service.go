// Package attributeService is the WarehouseHub attribute engine (spec 02):
// the admin-defined attribute tree (nodes + fields) and industry rules, the
// Warehouse root bootstrap, the in-memory rules snapshot other services
// evaluate with (domain.Rules), and the recompute jobs that keep every live
// warehouse's search projection in step with the rules. The evaluator itself
// is pure and lives in internal/warehousehub/domain.
package attributeService

import (
	"context"
	"fmt"
	"time"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/pipeline"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// NewNodeDefault is what a new node means for warehouses whose parent node is
// yes (D-126). There is no "yes for all".
type NewNodeDefault string

const (
	// DefaultUnknown writes {status: unknown} markers onto every such live
	// doc and open draft, with no review (D-128); they surface in Needs-info.
	DefaultUnknown NewNodeDefault = "unknown"
	// DefaultNo writes nothing: absent = no (D-133).
	DefaultNo NewNodeDefault = "no"
)

// WriteResult is returned by every tree / industry write.
type WriteResult struct {
	// RulesVersion after the write (0 when the bump failed).
	RulesVersion int64 `json:"rulesVersion"`
	// Changed lists the node / industry keys written.
	Changed []string `json:"changed"`
}

// NodePatch is a node update: nil fields are unchanged. Key and parent are
// not editable here (parent → MoveNode); fields have their own endpoints.
type NodePatch struct {
	Key             string    `json:"key"`
	ExpectedVersion int       `json:"expectedVersion"`
	Name            *string   `json:"name,omitempty"`
	Description     *string   `json:"description,omitempty"`
	Public          *bool     `json:"public,omitempty"`
	Filterable      *bool     `json:"filterable,omitempty"`
	FilterRow       *string   `json:"filterRow,omitempty"`
	FilterPos       *int      `json:"filterPos,omitempty"`
	Synonyms        *[]string `json:"synonyms,omitempty"`
}

// IndustryPatch is an industry update: nil fields are unchanged.
type IndustryPatch struct {
	Key             string              `json:"key"`
	ExpectedVersion int                 `json:"expectedVersion"`
	Name            *string             `json:"name,omitempty"`
	Order           *int                `json:"order,omitempty"`
	Required        *[]models.Condition `json:"required,omitempty"`
	Preferred       *[]models.Condition `json:"preferred,omitempty"`
}

// DeleteTarget names a hard delete (D-129, superuser): a node with its
// subtree (Field empty), a field (Option empty) or a pick/multi option.
// Confirm=false only previews.
type DeleteTarget struct {
	Node            string `json:"node"`
	Field           string `json:"field,omitempty"`
	Option          string `json:"option,omitempty"`
	ExpectedVersion int    `json:"expectedVersion"`
	Confirm         bool   `json:"confirm"`
}

// DeletePreview is the confirm screen: what goes, how many warehouses hold
// values, and what blocks the delete (empty = allowed).
type DeletePreview struct {
	// Nodes is the node and its descendants (node delete only).
	Nodes []string `json:"nodes"`
	// Warehouses holding values that the strip job will remove (for an
	// option: warehouses using it, which blocks the delete).
	Warehouses int64    `json:"warehouses"`
	BlockedBy  []string `json:"blockedBy"`
	// BatchID ties the strip job's change_log rows together (confirmed
	// deletes only).
	BatchID string `json:"batchId,omitempty"`
}

// Recompute (spec 02, D-040): every rules write dispatches recompute_all,
// which pages the stale live/archived warehouses and fans out
// recompute_batch messages; each batch evaluates and BulkWrites projections.
const (
	ProcessRecomputeAll   pipeline.ProcessType = "attributes.recompute_all"
	ProcessRecomputeBatch pipeline.ProcessType = "attributes.recompute_batch"

	// RunRules marks the fan-out dispatched by a rules write; safety-net runs
	// use "sn-YYYYMMDD" so their keys never collide with it.
	RunRules = "rules"
)

// ProcessStrip removes deleted nodes / fields from every live copy and open
// revision (D-129).
const ProcessStrip pipeline.ProcessType = "attributes.strip"

// StripPayload is the attributes.strip body. Paths are relative to
// `attributes` ("cold_storage", "cold_storage.fields.temperature").
type StripPayload struct {
	Paths   []string `json:"paths"`
	BatchID string   `json:"batchId"`
	// Target is the deleted node or "<node>.<field>" (change_log meta).
	Target string `json:"target"`
}

// StripKey is the stable message id of one strip run.
func StripKey(batchID string) string { return "strip:" + batchID }

// RecomputeAllPayload is the attributes.recompute_all body.
type RecomputeAllPayload struct {
	RulesVersion int64  `json:"rulesVersion"`
	Run          string `json:"run"`
}

// RecomputeBatchPayload is the attributes.recompute_batch body.
type RecomputeBatchPayload struct {
	RulesVersion int64    `json:"rulesVersion"`
	IDs          []string `json:"ids"`
}

// RecomputeAllKey is the stable message id of one recompute_all run. The key
// always carries the rulesVersion (a bare id would swallow later dispatches).
func RecomputeAllKey(rulesVersion int64, run string) string {
	return fmt.Sprintf("recompute_all:%d:%s", rulesVersion, run)
}

// AttributeService is the attribute engine.
type AttributeService interface {
	// Boot ensures the Warehouse root exists (D-142, idempotent; never
	// overwrites admin edits) and loads the rules snapshot. Called once in
	// main before the async handlers start.
	Boot(ctx context.Context) error
	// StartRefresh reloads the snapshot every interval until ctx ends, so an
	// instance that didn't serve a write catches up.
	StartRefresh(ctx context.Context, interval time.Duration)
	// Rules is the snapshot other services evaluate with.
	Rules() domain.Rules

	// Snapshot reads the tree + industries straight from the DB (admin reads
	// never edit against a stale cache).
	Snapshot(ctx context.Context) (*domain.Snapshot, error)

	// Tree writes. Each validates against a fresh snapshot, CAS-writes, logs
	// to change_log, bumps rulesVersion and dispatches the recompute.
	CreateNode(ctx context.Context, actor domain.Actor, n models.AttributeNode, def NewNodeDefault) (models.AttributeNode, WriteResult, error)
	UpdateNode(ctx context.Context, actor domain.Actor, p NodePatch) (models.AttributeNode, WriteResult, error)
	MoveNode(ctx context.Context, actor domain.Actor, key string, expected int, newParent string, order int) (models.AttributeNode, WriteResult, error)
	ReorderNodes(ctx context.Context, actor domain.Actor, parentKey string, keys []string) (WriteResult, error)
	CreateField(ctx context.Context, actor domain.Actor, nodeKey string, expected int, f models.AttributeField) (models.AttributeNode, WriteResult, error)
	UpdateField(ctx context.Context, actor domain.Actor, nodeKey string, expected int, f models.AttributeField) (models.AttributeNode, WriteResult, error)
	ReorderFields(ctx context.Context, actor domain.Actor, nodeKey string, expected int, keys []string) (models.AttributeNode, WriteResult, error)

	// DeleteDefinition hard-deletes a node (with its subtree), a field or an
	// option (D-129/D-138). Blocked while an industry rule or a ratio field
	// references it, or (option) while a warehouse uses it; the root and
	// locked fields are never deleted. Values are stripped by attributes.strip.
	DeleteDefinition(ctx context.Context, actor domain.Actor, t DeleteTarget) (DeletePreview, WriteResult, error)

	// Industry writes (delete is superuser-only at the route).
	CreateIndustry(ctx context.Context, actor domain.Actor, ind models.Industry) (models.Industry, WriteResult, error)
	UpdateIndustry(ctx context.Context, actor domain.Actor, p IndustryPatch) (models.Industry, WriteResult, error)
	DeleteIndustry(ctx context.Context, actor domain.Actor, key string, expected int) (WriteResult, error)

	// Async handlers.
	RecomputeAll(ctx context.Context, p RecomputeAllPayload) error
	RecomputeBatch(ctx context.Context, p RecomputeBatchPayload) error
	Strip(ctx context.Context, p StripPayload) error
}
