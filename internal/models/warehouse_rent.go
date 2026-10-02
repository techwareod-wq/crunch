package models

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// WarehouseRent is a `warehouse_rents` doc: the live rent terms, copied on
// approve. Payments later reference warehouseId + termsVersion (PRD §4.4).
type WarehouseRent struct {
	ID            primitive.ObjectID `bson:"_id,omitempty"   json:"id"`
	WarehouseID   primitive.ObjectID `bson:"warehouse_id"    json:"warehouseId"`
	TermsVersion  int                `bson:"terms_version"   json:"termsVersion"`
	Headline      Money              `bson:"headline"        json:"headline"`
	Admin         *RentAdmin         `bson:"admin,omitempty" json:"admin,omitempty"`
	EffectiveFrom time.Time          `bson:"effective_from"  json:"effectiveFrom"`
}

// EnsureWarehouseRentIndexes: one rent doc per warehouse.
func EnsureWarehouseRentIndexes(ctx context.Context) error {
	if _, err := Collection(warehouseRentsCollection).Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "warehouse_id", Value: 1}}, Options: options.Index().SetUnique(true)},
	}); err != nil {
		return fmt.Errorf("ensure warehouse_rents indexes: %w", err)
	}
	return nil
}

// UpsertWarehouseRent writes the live terms for r.WarehouseID.
func UpsertWarehouseRent(ctx context.Context, r WarehouseRent) error {
	_, err := Collection(warehouseRentsCollection).UpdateOne(ctx, bson.M{"warehouse_id": r.WarehouseID},
		bson.M{"$set": bson.M{"terms_version": r.TermsVersion, "headline": r.Headline, "admin": r.Admin, "effective_from": r.EffectiveFrom}},
		options.Update().SetUpsert(true))
	return err
}
