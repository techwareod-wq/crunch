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

// Media kinds, doc types, visibility and status (D-062).
const (
	MediaPhoto = "photo"
	MediaDoc   = "doc"

	DocFloorPlan   = "floor_plan"
	DocAgreement   = "agreement"
	DocCertificate = "certificate"
	DocOther       = "other"

	VisibilityPublic = "public"
	VisibilityStaff  = "staff"

	MediaPending  = "pending"
	MediaReady    = "ready"
	MediaOrphaned = "orphaned"
)

// WarehouseMedia is a `warehouse_media` doc. Keys are immutable and shared
// across revisions; nothing is copied on approve.
type WarehouseMedia struct {
	ID          primitive.ObjectID `bson:"_id,omitempty"      json:"id"`
	WarehouseID primitive.ObjectID `bson:"warehouse_id"       json:"warehouseId"`
	Kind        string             `bson:"kind"               json:"kind"`
	DocType     string             `bson:"doc_type,omitempty" json:"docType,omitempty"`
	Visibility  string             `bson:"visibility"         json:"visibility"`
	Bucket      string             `bson:"bucket"             json:"-"`
	Key         string             `bson:"key"                json:"key"`
	Filename    string             `bson:"filename"           json:"filename"`
	ContentType string             `bson:"content_type"       json:"contentType"`
	Bytes       int64              `bson:"bytes"              json:"bytes"`
	Status      string             `bson:"status"             json:"status"`
	UploadedBy  string             `bson:"uploaded_by"        json:"uploadedBy"`
	CreatedAt   time.Time          `bson:"created_at"         json:"createdAt"`
}

func media() *mongo.Collection { return Collection(warehouseMediaCollection) }

// EnsureWarehouseMediaIndexes: per-warehouse listing, GC scan.
func EnsureWarehouseMediaIndexes(ctx context.Context) error {
	if _, err := media().Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "warehouse_id", Value: 1}, {Key: "kind", Value: 1}}},
		{Keys: bson.D{{Key: "status", Value: 1}, {Key: "created_at", Value: 1}}},
	}); err != nil {
		return fmt.Errorf("ensure warehouse_media indexes: %w", err)
	}
	return nil
}

// InsertWarehouseMedia inserts m (keeping a preset id).
func InsertWarehouseMedia(ctx context.Context, m *WarehouseMedia) error {
	if m.ID.IsZero() {
		m.ID = primitive.NewObjectID()
	}
	_, err := media().InsertOne(ctx, m)
	return err
}

// FindWarehouseMediaByID reads one media doc; ErrNotFound when missing.
func FindWarehouseMediaByID(ctx context.Context, id primitive.ObjectID) (*WarehouseMedia, error) {
	return findOneDoc[WarehouseMedia](ctx, media(), bson.M{"_id": id})
}

// ReplaceWarehouseMedia replaces m.
func ReplaceWarehouseMedia(ctx context.Context, m *WarehouseMedia) error {
	_, err := media().ReplaceOne(ctx, bson.M{"_id": m.ID}, m)
	return err
}

// DeleteWarehouseMedia deletes one media doc.
func DeleteWarehouseMedia(ctx context.Context, id primitive.ObjectID) error {
	_, err := media().DeleteOne(ctx, bson.M{"_id": id})
	return err
}

// ListWarehouseMedia lists a warehouse's pending + ready media, oldest first.
func ListWarehouseMedia(ctx context.Context, warehouseID primitive.ObjectID) ([]WarehouseMedia, error) {
	return findAllDocs[WarehouseMedia](ctx, media(), bson.M{"warehouse_id": warehouseID, "status": bson.M{"$ne": MediaOrphaned}},
		options.Find().SetSort(bson.D{{Key: "created_at", Value: 1}}))
}

// CountWarehousePhotos counts a warehouse's pending + ready photos.
func CountWarehousePhotos(ctx context.Context, warehouseID primitive.ObjectID) (int64, error) {
	return media().CountDocuments(ctx, bson.M{"warehouse_id": warehouseID, "kind": MediaPhoto,
		"status": bson.M{"$in": bson.A{MediaPending, MediaReady}}})
}

// ListWarehouseMediaForGC lists pending media created before pendingBefore,
// and ready/orphaned media created before staleBefore.
func ListWarehouseMediaForGC(ctx context.Context, pendingBefore, staleBefore time.Time) ([]WarehouseMedia, error) {
	return findAllDocs[WarehouseMedia](ctx, media(), bson.M{"$or": bson.A{
		bson.M{"status": MediaPending, "created_at": bson.M{"$lt": pendingBefore}},
		bson.M{"status": bson.M{"$in": bson.A{MediaReady, MediaOrphaned}}, "created_at": bson.M{"$lt": staleBefore}},
	}})
}
