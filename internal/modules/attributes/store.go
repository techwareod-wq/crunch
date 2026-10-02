package attributes

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// Module-local collection names (platform collections live in
// internal/models/collections.go).
const (
	collDefs       = "attribute_definitions"
	collIndustries = "industries"
)

var (
	errKeyExists       = errors.New("key already exists")
	errVersionConflict = errors.New("changed since last read")
)

// Store is the attribute tree + industries persistence. Mongo in production,
// memStore in tests.
type Store interface {
	// Load reads rulesVersion first, then every def and industry, so the
	// snapshot's version never claims more than its data holds.
	Load(ctx context.Context) (*domain.Snapshot, error)
	RulesVersion(ctx context.Context) (int64, error)
	BumpRulesVersion(ctx context.Context) (int64, error)
	// InsertDef fails with errKeyExists on a duplicate key.
	InsertDef(ctx context.Context, d *domain.AttrDef) error
	// ReplaceDef CAS-replaces the doc whose version is expected; a mismatch
	// is errVersionConflict. d.Version must already be expected+1.
	ReplaceDef(ctx context.Context, d *domain.AttrDef, expected int) error
	InsertIndustry(ctx context.Context, ind *domain.Industry) error
	ReplaceIndustry(ctx context.Context, ind *domain.Industry, expected int) error
}

// WarehouseStore is the recompute's view of the catalog's `warehouses`
// collection (contract in domain/warehouse.go).
type WarehouseStore interface {
	// StaleIDs pages (by _id) live/archived warehouses whose projection is
	// older than version.
	StaleIDs(ctx context.Context, version int64, after primitive.ObjectID, limit int) ([]primitive.ObjectID, error)
	LoadEvalDocs(ctx context.Context, ids []primitive.ObjectID) ([]domain.LiveEvalDoc, error)
	// WriteProjections sets each projection, guarded so a doc already at
	// (or past) the projection's rules version is left alone. Returns how
	// many docs were written.
	WriteProjections(ctx context.Context, ps map[primitive.ObjectID]domain.Projection) (int64, error)
}

// ensureIndexes: unique keys and the sibling-order index for the tree, plus
// the two `warehouses` indexes this module owns there: {needs_info} for the
// Needs-info queue (spec 02) and the recompute's stale-page scan.
func ensureIndexes(ctx context.Context) error {
	if _, err := models.Collection(collDefs).Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "key", Value: 1}}, Options: options.Index().SetUnique(true)},
		{Keys: bson.D{{Key: "parent_key", Value: 1}, {Key: "order", Value: 1}}},
	}); err != nil {
		return fmt.Errorf("ensure attribute_definitions indexes: %w", err)
	}
	if _, err := models.Collection(collIndustries).Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "key", Value: 1}}, Options: options.Index().SetUnique(true)},
	}); err != nil {
		return fmt.Errorf("ensure industries indexes: %w", err)
	}
	if _, err := models.Collection(domain.CollWarehouses).Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "needs_info", Value: 1}}},
		{Keys: bson.D{{Key: domain.FieldWarehouseStatus, Value: 1}, {Key: domain.FieldFitRulesVersion, Value: 1}, {Key: "_id", Value: 1}}},
	}); err != nil {
		return fmt.Errorf("ensure warehouses recompute indexes: %w", err)
	}
	return nil
}

type mongoStore struct{}

func (mongoStore) RulesVersion(ctx context.Context) (int64, error) {
	return models.GetCounter(ctx, models.CounterRulesVersion)
}

func (mongoStore) BumpRulesVersion(ctx context.Context) (int64, error) {
	return models.BumpCounter(ctx, models.CounterRulesVersion)
}

func (s mongoStore) Load(ctx context.Context) (*domain.Snapshot, error) {
	v, err := s.RulesVersion(ctx)
	if err != nil {
		return nil, fmt.Errorf("read rulesVersion: %w", err)
	}
	var defs []domain.AttrDef
	if err := findAll(ctx, collDefs, &defs); err != nil {
		return nil, fmt.Errorf("load attribute_definitions: %w", err)
	}
	var inds []domain.Industry
	if err := findAll(ctx, collIndustries, &inds); err != nil {
		return nil, fmt.Errorf("load industries: %w", err)
	}
	return domain.NewSnapshot(v, defs, inds), nil
}

func findAll(ctx context.Context, coll string, out any) error {
	cur, err := models.Collection(coll).Find(ctx, bson.M{})
	if err != nil {
		return err
	}
	return cur.All(ctx, out)
}

func (mongoStore) InsertDef(ctx context.Context, d *domain.AttrDef) error {
	id, err := models.InsertOne(ctx, collDefs, d)
	if mongo.IsDuplicateKeyError(err) {
		return errKeyExists
	}
	if err != nil {
		return err
	}
	d.ID = id
	return nil
}

func (mongoStore) ReplaceDef(ctx context.Context, d *domain.AttrDef, expected int) error {
	return casReplace(ctx, collDefs, d.Key, expected, d)
}

func (mongoStore) InsertIndustry(ctx context.Context, ind *domain.Industry) error {
	id, err := models.InsertOne(ctx, collIndustries, ind)
	if mongo.IsDuplicateKeyError(err) {
		return errKeyExists
	}
	if err != nil {
		return err
	}
	ind.ID = id
	return nil
}

func (mongoStore) ReplaceIndustry(ctx context.Context, ind *domain.Industry, expected int) error {
	return casReplace(ctx, collIndustries, ind.Key, expected, ind)
}

func casReplace(ctx context.Context, coll, key string, expected int, doc any) error {
	res, err := models.Collection(coll).ReplaceOne(ctx, bson.M{"key": key, "version": expected}, doc)
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return errVersionConflict
	}
	return nil
}

type mongoWarehouses struct{}

// staleFilter matches live/archived warehouses evaluated before version
// ($not $gte also matches a missing fit_rules_version).
func staleFilter(version int64) bson.M {
	return bson.M{
		domain.FieldWarehouseStatus: bson.M{"$in": bson.A{domain.WarehouseLive, domain.WarehouseArchived}},
		domain.FieldFitRulesVersion: bson.M{"$not": bson.M{"$gte": version}},
	}
}

func (mongoWarehouses) StaleIDs(ctx context.Context, version int64, after primitive.ObjectID, limit int) ([]primitive.ObjectID, error) {
	f := staleFilter(version)
	if !after.IsZero() {
		f["_id"] = bson.M{"$gt": after}
	}
	cur, err := models.Collection(domain.CollWarehouses).Find(ctx, f,
		options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}).SetLimit(int64(limit)).SetProjection(bson.M{"_id": 1}))
	if err != nil {
		return nil, err
	}
	var rows []struct {
		ID primitive.ObjectID `bson:"_id"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	ids := make([]primitive.ObjectID, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	return ids, nil
}

func (mongoWarehouses) LoadEvalDocs(ctx context.Context, ids []primitive.ObjectID) ([]domain.LiveEvalDoc, error) {
	cur, err := models.Collection(domain.CollWarehouses).Find(ctx, bson.M{"_id": bson.M{"$in": ids}},
		options.Find().SetProjection(bson.M{domain.FieldLiveAttributes: 1, domain.FieldLiveTotalAreaSqm: 1}))
	if err != nil {
		return nil, err
	}
	var docs []domain.LiveEvalDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	return docs, nil
}

func (mongoWarehouses) WriteProjections(ctx context.Context, ps map[primitive.ObjectID]domain.Projection) (int64, error) {
	if len(ps) == 0 {
		return 0, nil
	}
	writes := make([]mongo.WriteModel, 0, len(ps))
	for id, p := range ps {
		writes = append(writes, mongo.NewUpdateOneModel().
			SetFilter(bson.M{"_id": id, domain.FieldFitRulesVersion: bson.M{"$not": bson.M{"$gte": p.FitRulesVersion}}}).
			SetUpdate(bson.M{"$set": p}))
	}
	res, err := models.Collection(domain.CollWarehouses).BulkWrite(ctx, writes, options.BulkWrite().SetOrdered(false))
	if err != nil {
		return 0, err
	}
	return res.ModifiedCount, nil
}
