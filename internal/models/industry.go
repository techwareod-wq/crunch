package models

import (
	"context"
	"fmt"
	"slices"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Cmp is an industry-rule comparator (D-141; constants in domain).
type Cmp string

// Condition is one industry rule: `node is_yes` or `node.field <cmp> value`.
// Numeric values are in the field's canonical unit (sq m for area).
type Condition struct {
	Node  string `bson:"node"            json:"node"`
	Field string `bson:"field,omitempty" json:"field,omitempty"`
	Cmp   Cmp    `bson:"cmp"             json:"cmp"`
	Value any    `bson:"value,omitempty" json:"value,omitempty"`
}

// Path is the condition's target: "<node>" or "<node>.<field>".
func (c Condition) Path() string {
	if c.Field == "" {
		return c.Node
	}
	return c.Node + "." + c.Field
}

// Industry is one `industries` doc: an industry-fit rule set (D-030, D-037).
type Industry struct {
	ID        primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	Key       string             `bson:"key"           json:"key"`
	Name      string             `bson:"name"          json:"name"`
	Order     int                `bson:"order"         json:"order"`
	Required  []Condition        `bson:"required"      json:"required"`
	Preferred []Condition        `bson:"preferred"     json:"preferred"`
	Version   int                `bson:"version"       json:"version"`
	UpdatedBy string             `bson:"updated_by"    json:"updatedBy"`
	UpdatedAt time.Time          `bson:"updated_at"    json:"updatedAt"`
}

// Clone deep-copies ind.
func (ind Industry) Clone() Industry {
	ind.Required = slices.Clone(ind.Required)
	ind.Preferred = slices.Clone(ind.Preferred)
	return ind
}

// EnsureIndustryIndexes: unique key.
func EnsureIndustryIndexes(ctx context.Context) error {
	if _, err := Collection(industriesCollection).Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "key", Value: 1}}, Options: options.Index().SetUnique(true)},
	}); err != nil {
		return fmt.Errorf("ensure industries indexes: %w", err)
	}
	return nil
}

// ListIndustries reads every industry.
func ListIndustries(ctx context.Context) ([]Industry, error) {
	return findAllDocs[Industry](ctx, Collection(industriesCollection), bson.M{})
}

// InsertIndustry inserts ind; ErrDuplicateKey when the key exists.
func InsertIndustry(ctx context.Context, ind *Industry) error {
	return insertDoc(ctx, industriesCollection, ind, &ind.ID)
}

// ReplaceIndustry CAS-replaces on version; ErrVersionConflict on a mismatch.
func ReplaceIndustry(ctx context.Context, ind *Industry, expected int) error {
	return casReplaceByKey(ctx, industriesCollection, ind.Key, expected, ind)
}

// DeleteIndustry CAS-deletes on version; ErrVersionConflict on a mismatch.
func DeleteIndustry(ctx context.Context, key string, expected int) error {
	res, err := Collection(industriesCollection).DeleteOne(ctx, bson.M{"key": key, "version": expected})
	if err != nil {
		return err
	}
	if res.DeletedCount == 0 {
		return ErrVersionConflict
	}
	return nil
}
