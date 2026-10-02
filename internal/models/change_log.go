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

// ChangeLogEntry is one row of the WarehouseHub change log (D-014): the full
// document before and after every change to a listing, revision, rent, media,
// attribute definition, industry or enquiry. Kept forever.
// Distinct from adminActions (the request-level security trail, which stores
// only a payload hash).
type ChangeLogEntry struct {
	ID          primitive.ObjectID `bson:"_id,omitempty"  json:"id"`
	Entity      string             `bson:"entity"         json:"entity"`
	EntityID    string             `bson:"entity_id"      json:"entityId"`
	Action      string             `bson:"action"         json:"action"`
	ActorUserID string             `bson:"actor_user_id"  json:"actorUserId"`
	ActorEmail  string             `bson:"actor_email"    json:"actorEmail"`
	At          time.Time          `bson:"at"             json:"at"`
	Before      bson.M             `bson:"before"         json:"before,omitempty"`
	After       bson.M             `bson:"after"          json:"after,omitempty"`
	Meta        bson.M             `bson:"meta,omitempty" json:"meta,omitempty"`
}

// ChangeLogFilter narrows ListChangeLog. Zero fields are ignored.
type ChangeLogFilter struct {
	Entity      string
	EntityID    string
	ActorUserID string
	ActorEmail  string
	From        *time.Time
	To          *time.Time
}

// EnsureChangeLogIndexes backs the history-of-one-entity view, the by-actor
// view and the global newest-first feed. No TTL: kept forever (D-014).
func EnsureChangeLogIndexes(ctx context.Context) error {
	_, err := Collection(changeLogCollection).Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "entity", Value: 1}, {Key: "entity_id", Value: 1}, {Key: "at", Value: -1}}},
		{Keys: bson.D{{Key: "actor_user_id", Value: 1}, {Key: "at", Value: -1}}},
		{Keys: bson.D{{Key: "actor_email", Value: 1}, {Key: "at", Value: -1}}},
		{Keys: bson.D{{Key: "at", Value: -1}}},
	})
	if err != nil {
		return fmt.Errorf("ensure change log indexes: %w", err)
	}
	return nil
}

// InsertChangeLogEntry appends one row. Append-only: no update or delete
// helpers exist for this collection.
func InsertChangeLogEntry(ctx context.Context, e *ChangeLogEntry) error {
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	id, err := InsertOne(ctx, changeLogCollection, e)
	if err != nil {
		return err
	}
	e.ID = id
	return nil
}

// ListChangeLog pages entries newest-first. before/after are projected out —
// the list is a feed; FindChangeLogEntry returns the full docs. page is
// 1-based.
func ListChangeLog(ctx context.Context, f ChangeLogFilter, page, limit int) ([]ChangeLogEntry, int64, error) {
	filter := bson.M{}
	if f.Entity != "" {
		filter["entity"] = f.Entity
	}
	if f.EntityID != "" {
		filter["entity_id"] = f.EntityID
	}
	if f.ActorUserID != "" {
		filter["actor_user_id"] = f.ActorUserID
	}
	if f.ActorEmail != "" {
		filter["actor_email"] = f.ActorEmail
	}
	if f.From != nil || f.To != nil {
		at := bson.M{}
		if f.From != nil {
			at["$gte"] = f.From.UTC()
		}
		if f.To != nil {
			at["$lt"] = f.To.UTC()
		}
		filter["at"] = at
	}

	total, err := Collection(changeLogCollection).CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, err
	}
	opts := options.Find().
		SetSort(bson.D{{Key: "at", Value: -1}, {Key: fieldID, Value: -1}}).
		SetSkip(int64(page-1) * int64(limit)).
		SetLimit(int64(limit)).
		SetProjection(bson.M{"before": 0, "after": 0})
	cur, err := Collection(changeLogCollection).Find(ctx, filter, opts)
	if err != nil {
		return nil, 0, err
	}
	entries := make([]ChangeLogEntry, 0, limit)
	if err := cur.All(ctx, &entries); err != nil {
		return nil, 0, err
	}
	return entries, total, nil
}

// FindChangeLogEntry loads one entry with its before/after documents.
func FindChangeLogEntry(ctx context.Context, id primitive.ObjectID) (bool, *ChangeLogEntry, error) {
	var e ChangeLogEntry
	found, err := FindOne(ctx, changeLogCollection, bson.M{fieldID: id}, &e)
	if err != nil || !found {
		return false, nil, err
	}
	return true, &e, nil
}
