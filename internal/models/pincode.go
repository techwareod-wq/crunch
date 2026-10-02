package models

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Pincode is a `pincodes` doc: one Indian postal code's centroid from the
// GeoNames dump (D-078 last-resort geocoder), loaded by cmd/pincodes.
type Pincode struct {
	Code     string  `bson:"_id"`
	Lat      float64 `bson:"lat"`
	Lng      float64 `bson:"lng"`
	Place    string  `bson:"place"`
	District string  `bson:"district"`
	State    string  `bson:"state"`
	// Places / DistrictLC are lower-cased for place-name lookups.
	Places     []string `bson:"places"`
	DistrictLC string   `bson:"district_lc"`
}

func pincodes() *mongo.Collection { return Collection(pincodesCollection) }

// EnsurePincodeIndexes: place and district name lookups.
func EnsurePincodeIndexes(ctx context.Context) error {
	if _, err := pincodes().Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "places", Value: 1}}},
		{Keys: bson.D{{Key: "district_lc", Value: 1}}},
	}); err != nil {
		return fmt.Errorf("ensure pincodes indexes: %w", err)
	}
	return nil
}

// FindPincode reads one postal code; ErrNotFound when missing.
func FindPincode(ctx context.Context, code string) (*Pincode, error) {
	return findOneDoc[Pincode](ctx, pincodes(), bson.M{"_id": code})
}

// FindPincodesByPlace lists pincodes whose place names include name
// (lower-cased), at most limit.
func FindPincodesByPlace(ctx context.Context, name string, limit int) ([]Pincode, error) {
	return findAllDocs[Pincode](ctx, pincodes(), bson.M{"places": name}, options.Find().SetLimit(int64(limit)))
}

// FindPincodesByDistrict lists a district's pincodes (lower-cased name), at
// most limit.
func FindPincodesByDistrict(ctx context.Context, name string, limit int) ([]Pincode, error) {
	return findAllDocs[Pincode](ctx, pincodes(), bson.M{"district_lc": name}, options.Find().SetLimit(int64(limit)))
}

// UpsertPincodes bulk-upserts ps (loader). Returns upserted + modified.
func UpsertPincodes(ctx context.Context, ps []Pincode) (int64, error) {
	if len(ps) == 0 {
		return 0, nil
	}
	writes := make([]mongo.WriteModel, 0, len(ps))
	for _, p := range ps {
		writes = append(writes, mongo.NewReplaceOneModel().SetFilter(bson.M{"_id": p.Code}).SetReplacement(p).SetUpsert(true))
	}
	res, err := pincodes().BulkWrite(ctx, writes, options.BulkWrite().SetOrdered(false))
	if err != nil {
		return 0, err
	}
	return res.UpsertedCount + res.ModifiedCount, nil
}
