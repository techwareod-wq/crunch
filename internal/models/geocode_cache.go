package models

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// GeocodeCacheEntry is a `geocode_cache` doc: one resolved search location
// (D-078), keyed "country|type|normalized query", dropped at ExpiresAt by
// the TTL index.
type GeocodeCacheEntry struct {
	Key       string    `bson:"_id"`
	Lat       float64   `bson:"lat"`
	Lng       float64   `bson:"lng"`
	Label     string    `bson:"label"`
	Source    string    `bson:"source"`
	ExpiresAt time.Time `bson:"expires_at"`
}

func geocodeCache() *mongo.Collection { return Collection(geocodeCacheCollection) }

// EnsureGeocodeCacheIndexes: TTL on expires_at.
func EnsureGeocodeCacheIndexes(ctx context.Context) error {
	if _, err := geocodeCache().Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{{Key: "expires_at", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(0),
	}); err != nil {
		return fmt.Errorf("ensure geocode_cache indexes: %w", err)
	}
	return nil
}

// FindGeocodeCache reads an unexpired entry; ErrNotFound when missing.
func FindGeocodeCache(ctx context.Context, key string, now time.Time) (*GeocodeCacheEntry, error) {
	return findOneDoc[GeocodeCacheEntry](ctx, geocodeCache(), bson.M{"_id": key, "expires_at": bson.M{"$gt": now}})
}

// PutGeocodeCache upserts e.
func PutGeocodeCache(ctx context.Context, e GeocodeCacheEntry) error {
	_, err := geocodeCache().ReplaceOne(ctx, bson.M{"_id": e.Key}, e, options.Replace().SetUpsert(true))
	return err
}
