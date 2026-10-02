package store

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// Store is the attribute service's data access: the tree + industries, the
// rulesVersion counter and the recompute's view of `warehouses`.
type Store interface {
	// Load reads rulesVersion first, then every node and industry, so the
	// snapshot's version never claims more than its data holds.
	Load(ctx context.Context) (*domain.Snapshot, error)
	RulesVersion(ctx context.Context) (int64, error)
	BumpRulesVersion(ctx context.Context) (int64, error)
	// InsertNode fails with models.ErrDuplicateKey on a duplicate key.
	InsertNode(ctx context.Context, n *models.AttributeNode) error
	// ReplaceNode CAS-replaces the doc whose version is expected; a mismatch
	// is models.ErrVersionConflict. n.Version must already be expected+1.
	ReplaceNode(ctx context.Context, n *models.AttributeNode, expected int) error
	InsertIndustry(ctx context.Context, ind *models.Industry) error
	ReplaceIndustry(ctx context.Context, ind *models.Industry, expected int) error
	DeleteIndustry(ctx context.Context, key string, expected int) error
	// DeleteNode CAS-deletes a node on version.
	DeleteNode(ctx context.Context, key string, expected int) error

	// StaleWarehouseIDs pages (by _id) live/archived warehouses whose
	// projection is older than version.
	StaleWarehouseIDs(ctx context.Context, version int64, after primitive.ObjectID, limit int) ([]primitive.ObjectID, error)
	WarehouseEvalDocs(ctx context.Context, ids []primitive.ObjectID) ([]models.WarehouseEvalDoc, error)
	// WriteProjections sets each projection, guarded so a doc already at (or
	// past) the projection's rules version is left alone.
	WriteProjections(ctx context.Context, ps map[primitive.ObjectID]models.Projection) (int64, error)

	// MarkNodeUnknown writes {status: unknown} for a new node onto every live
	// copy and open revision whose parent node is yes (D-128). Idempotent;
	// returns the warehouse ids touched.
	MarkNodeUnknown(ctx context.Context, key, parent string, at time.Time) ([]primitive.ObjectID, error)
	// CountHolding counts warehouses whose live copy or open revision holds
	// any of paths (relative to `attributes`).
	CountHolding(ctx context.Context, paths []string) (int64, error)
	// CountUsingOption counts warehouses whose live copy or open revision
	// uses option on node.field.
	CountUsingOption(ctx context.Context, node, field, option string) (int64, error)
	// Strip removes paths from every live copy and open revision; returns
	// the warehouse ids touched.
	Strip(ctx context.Context, paths []string, at time.Time) ([]primitive.ObjectID, error)
}

type store struct{}

func NewStore() Store {
	return &store{}
}

func (s *store) RulesVersion(ctx context.Context) (int64, error) {
	return models.GetCounter(ctx, models.CounterRulesVersion)
}

func (s *store) BumpRulesVersion(ctx context.Context) (int64, error) {
	return models.BumpCounter(ctx, models.CounterRulesVersion)
}

func (s *store) Load(ctx context.Context) (*domain.Snapshot, error) {
	v, err := s.RulesVersion(ctx)
	if err != nil {
		return nil, fmt.Errorf("read rulesVersion: %w", err)
	}
	nodes, err := models.ListAttributeNodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("load attribute_nodes: %w", err)
	}
	inds, err := models.ListIndustries(ctx)
	if err != nil {
		return nil, fmt.Errorf("load industries: %w", err)
	}
	return domain.NewSnapshot(v, nodes, inds), nil
}

func (s *store) InsertNode(ctx context.Context, n *models.AttributeNode) error {
	return models.InsertAttributeNode(ctx, n)
}

func (s *store) ReplaceNode(ctx context.Context, n *models.AttributeNode, expected int) error {
	return models.ReplaceAttributeNode(ctx, n, expected)
}

func (s *store) InsertIndustry(ctx context.Context, ind *models.Industry) error {
	return models.InsertIndustry(ctx, ind)
}

func (s *store) ReplaceIndustry(ctx context.Context, ind *models.Industry, expected int) error {
	return models.ReplaceIndustry(ctx, ind, expected)
}

func (s *store) DeleteIndustry(ctx context.Context, key string, expected int) error {
	return models.DeleteIndustry(ctx, key, expected)
}

func (s *store) StaleWarehouseIDs(ctx context.Context, version int64, after primitive.ObjectID, limit int) ([]primitive.ObjectID, error) {
	return models.StaleWarehouseIDs(ctx, version, after, limit)
}

func (s *store) WarehouseEvalDocs(ctx context.Context, ids []primitive.ObjectID) ([]models.WarehouseEvalDoc, error) {
	return models.FindWarehouseEvalDocs(ctx, ids)
}

func (s *store) WriteProjections(ctx context.Context, ps map[primitive.ObjectID]models.Projection) (int64, error) {
	return models.WriteWarehouseProjections(ctx, ps)
}

func (s *store) MarkNodeUnknown(ctx context.Context, key, parent string, at time.Time) ([]primitive.ObjectID, error) {
	return models.MarkNodeUnknown(ctx, key, parent, at)
}

func (s *store) DeleteNode(ctx context.Context, key string, expected int) error {
	return models.DeleteAttributeNode(ctx, key, expected)
}

func (s *store) CountHolding(ctx context.Context, paths []string) (int64, error) {
	return models.CountWarehousesHolding(ctx, paths)
}

func (s *store) CountUsingOption(ctx context.Context, node, field, option string) (int64, error) {
	return models.CountWarehousesUsingOption(ctx, node, field, option)
}

func (s *store) Strip(ctx context.Context, paths []string, at time.Time) ([]primitive.ObjectID, error) {
	return models.StripAttributes(ctx, paths, at)
}
