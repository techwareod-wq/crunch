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

// RevisionState is a revision's lifecycle state (spec 03).
type RevisionState string

const (
	RevDraft      RevisionState = "draft"
	RevInReview   RevisionState = "in_review"
	RevApproved   RevisionState = "approved"
	RevSuperseded RevisionState = "superseded"
	RevDiscarded  RevisionState = "discarded"
)

// IsOpen reports whether s is draft or in review (at most one per warehouse).
func (s RevisionState) IsOpen() bool { return s == RevDraft || s == RevInReview }

// ReviewEntry is one append-only review-history line (D-054).
type ReviewEntry struct {
	Action  string    `bson:"action"            json:"action"`
	By      string    `bson:"by"                json:"by"`
	At      time.Time `bson:"at"                json:"at"`
	Comment string    `bson:"comment,omitempty" json:"comment,omitempty"`
}

// WarehouseRevision is a `warehouse_revisions` doc: one version of a
// listing (draft, in review, approved history).
type WarehouseRevision struct {
	ID          primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	WarehouseID primitive.ObjectID `bson:"warehouse_id"  json:"warehouseId"`
	Version     int                `bson:"version"       json:"version"`
	// BaseVersion is the live version this revision was cloned from (0 for
	// a never-published warehouse); approve CASes on it.
	BaseVersion int           `bson:"base_version" json:"baseVersion"`
	State       RevisionState `bson:"state"        json:"state"`
	// Open mirrors State.IsOpen() for the unique partial index.
	Open bool `bson:"open" json:"-"`
	// Rev is the save CAS counter (D-052).
	Rev         int            `bson:"rev"                    json:"rev"`
	BatchID     string         `bson:"batch_id,omitempty"     json:"batchId,omitempty"`
	Content     ListingContent `bson:"content"                json:"content"`
	Review      []ReviewEntry  `bson:"review"                 json:"review"`
	CreatedBy   string         `bson:"created_by"             json:"createdBy"`
	UpdatedBy   string         `bson:"updated_by"             json:"updatedBy"`
	CreatedAt   time.Time      `bson:"created_at"             json:"createdAt"`
	UpdatedAt   time.Time      `bson:"updated_at"             json:"updatedAt"`
	SubmittedBy string         `bson:"submitted_by,omitempty" json:"submittedBy,omitempty"`
	// SubmittedByID is the submitter's user id.
	SubmittedByID string     `bson:"submitted_by_id,omitempty" json:"-"`
	SubmittedAt   *time.Time `bson:"submitted_at,omitempty"    json:"submittedAt,omitempty"`
	ApprovedBy    string     `bson:"approved_by,omitempty"     json:"approvedBy,omitempty"`
	ApprovedAt    *time.Time `bson:"approved_at,omitempty"     json:"approvedAt,omitempty"`
}

func revisions() *mongo.Collection { return Collection(warehouseRevisionsCollection) }

// EnsureWarehouseRevisionIndexes: at most one open revision per warehouse
// (partial on the mirrored `open` flag), the review queue, history.
func EnsureWarehouseRevisionIndexes(ctx context.Context) error {
	if _, err := revisions().Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "warehouse_id", Value: 1}}, Options: options.Index().SetUnique(true).
			SetName("one_open_revision").SetPartialFilterExpression(bson.M{"open": true})},
		{Keys: bson.D{{Key: "state", Value: 1}, {Key: "submitted_at", Value: 1}}},
		{Keys: bson.D{{Key: "warehouse_id", Value: 1}, {Key: "version", Value: -1}}},
	}); err != nil {
		return fmt.Errorf("ensure warehouse_revisions indexes: %w", err)
	}
	return nil
}

// InsertWarehouseRevision inserts r; ErrDuplicateKey when the warehouse
// already has an open revision.
func InsertWarehouseRevision(ctx context.Context, r *WarehouseRevision) error {
	return insertDoc(ctx, warehouseRevisionsCollection, r, &r.ID)
}

// FindWarehouseRevisionByID reads one revision; ErrNotFound when missing.
func FindWarehouseRevisionByID(ctx context.Context, id primitive.ObjectID) (*WarehouseRevision, error) {
	return findOneDoc[WarehouseRevision](ctx, revisions(), bson.M{"_id": id})
}

// ReplaceWarehouseRevision CAS-replaces r over the doc in (state, rev);
// ErrVersionConflict on a miss, ErrDuplicateKey when reopening would make a
// second open revision.
func ReplaceWarehouseRevision(ctx context.Context, r *WarehouseRevision, state RevisionState, rev int) error {
	res, err := revisions().ReplaceOne(ctx, bson.M{"_id": r.ID, "state": state, "rev": rev}, r)
	if mongo.IsDuplicateKeyError(err) {
		return ErrDuplicateKey
	}
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return ErrVersionConflict
	}
	return nil
}

// ListWarehouseRevisions lists a warehouse's revisions, newest first,
// without content.
func ListWarehouseRevisions(ctx context.Context, warehouseID primitive.ObjectID) ([]WarehouseRevision, error) {
	return findAllDocs[WarehouseRevision](ctx, revisions(), bson.M{"warehouse_id": warehouseID},
		options.Find().SetSort(bson.D{{Key: "version", Value: -1}}).SetProjection(bson.M{"content": 0}))
}

// ListApprovedWarehouseRevisions lists a warehouse's approved revisions.
func ListApprovedWarehouseRevisions(ctx context.Context, warehouseID primitive.ObjectID) ([]WarehouseRevision, error) {
	return findAllDocs[WarehouseRevision](ctx, revisions(), bson.M{"warehouse_id": warehouseID, "state": RevApproved})
}

// ListReviewQueue pages in-review revisions, oldest submission first (no
// content).
func ListReviewQueue(ctx context.Context, page, limit int) ([]WarehouseRevision, int64, error) {
	q := bson.M{"state": RevInReview}
	total, err := revisions().CountDocuments(ctx, q)
	if err != nil {
		return nil, 0, err
	}
	items, err := findAllDocs[WarehouseRevision](ctx, revisions(), q, options.Find().
		SetSort(bson.D{{Key: "submitted_at", Value: 1}, {Key: "_id", Value: 1}}).
		SetSkip(skipFor(page, limit)).SetLimit(int64(limit)).SetProjection(bson.M{"content": 0}))
	return items, total, err
}

// DeleteWarehouseRevisions deletes every revision of a warehouse.
func DeleteWarehouseRevisions(ctx context.Context, warehouseID primitive.ObjectID) error {
	_, err := revisions().DeleteMany(ctx, bson.M{"warehouse_id": warehouseID})
	return err
}

// ReferencedMediaIDs returns every media id a non-discarded revision
// references (content.media or the agreement).
func ReferencedMediaIDs(ctx context.Context) (map[primitive.ObjectID]bool, error) {
	cur, err := revisions().Find(ctx, bson.M{"state": bson.M{"$ne": RevDiscarded}},
		options.Find().SetProjection(bson.M{"content.media.media_id": 1, "content.rent_admin.agreement_media_id": 1}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	out := map[primitive.ObjectID]bool{}
	for cur.Next(ctx) {
		var r WarehouseRevision
		if err := cur.Decode(&r); err != nil {
			return nil, err
		}
		for _, id := range r.Content.MediaIDs() {
			out[id] = true
		}
	}
	return out, cur.Err()
}

// MediaIDs lists the media ids a content references.
func (c ListingContent) MediaIDs() []primitive.ObjectID {
	out := make([]primitive.ObjectID, 0, len(c.Media)+1)
	for _, m := range c.Media {
		out = append(out, m.MediaID)
	}
	if c.RentAdmin != nil && c.RentAdmin.AgreementMediaID != nil {
		out = append(out, *c.RentAdmin.AgreementMediaID)
	}
	return out
}

// WarehouseRevisionStates reads the state of each revision in ids.
func WarehouseRevisionStates(ctx context.Context, ids []primitive.ObjectID) (map[primitive.ObjectID]RevisionState, error) {
	out := map[primitive.ObjectID]RevisionState{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := findAllDocs[WarehouseRevision](ctx, revisions(), bson.M{"_id": bson.M{"$in": ids}},
		options.Find().SetProjection(bson.M{"state": 1}))
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.ID] = r.State
	}
	return out, nil
}
