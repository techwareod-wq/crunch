package models

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Enquiry statuses (D-104). Moves are free in any direction.
const (
	EnquiryNew       = "new"
	EnquiryContacted = "contacted"
	EnquiryClosed    = "closed"
)

// Close reasons (D-104), required when status = closed.
const (
	CloseWon   = "won"
	CloseLost  = "lost"
	CloseOther = "other"
)

// Enquiry history fields.
const (
	EnquiryFieldStatus   = "status"
	EnquiryFieldAssignee = "assignee"
)

// Enquiry is an `enquiries` doc (spec 06): one visitor enquiry, optionally
// about a listing (D-101), with free-text requirements (D-102). Soft-deleted
// when the visitor deletes their account; contact details are kept (D-019).
type Enquiry struct {
	ID      primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	UserID  primitive.ObjectID `bson:"user_id"       json:"userId"`
	ClerkID string             `bson:"clerk_id"      json:"clerkId"`

	Name      string `bson:"name"              json:"name"`
	Company   string `bson:"company,omitempty" json:"company,omitempty"`
	Email     string `bson:"email"             json:"email"`
	Phone     string `bson:"phone"             json:"phone"`
	PhoneE164 string `bson:"phone_e164"        json:"phoneE164"`
	Message   string `bson:"message"           json:"message"`

	// Listing is a snapshot of the listing at submit time; nil for a general
	// requirement.
	Listing *EnquiryListing `bson:"listing,omitempty" json:"listing"`
	// Country is the listing's country (IN for a general requirement); the
	// analytics rollup counts enquiries per country.
	Country string `bson:"country" json:"country"`

	// SearchID is the search that led here; SessionID the visitor's
	// anonymous session (conversion, D-105).
	SearchID  *primitive.ObjectID `bson:"search_id,omitempty"  json:"searchId,omitempty"`
	SessionID string              `bson:"session_id,omitempty" json:"sessionId,omitempty"`

	Status      string `bson:"status"                 json:"status"`
	CloseReason string `bson:"close_reason,omitempty" json:"closeReason,omitempty"`
	CloseNote   string `bson:"close_note,omitempty"   json:"closeNote,omitempty"`

	AssigneeUserID *primitive.ObjectID `bson:"assignee_user_id,omitempty" json:"assigneeUserId"`
	AssigneeEmail  string              `bson:"assignee_email,omitempty"   json:"assigneeEmail,omitempty"`

	Notes   []EnquiryNote   `bson:"notes"   json:"notes"`
	History []EnquiryChange `bson:"history" json:"history"`

	IdempotencyKey string     `bson:"idempotency_key"      json:"-"`
	CreatedAt      time.Time  `bson:"created_at"           json:"createdAt"`
	UpdatedAt      time.Time  `bson:"updated_at"           json:"updatedAt"`
	DeletedAt      *time.Time `bson:"deleted_at,omitempty" json:"-"`
}

// EnquiryListing is the listing snapshot on an enquiry.
type EnquiryListing struct {
	WarehouseID primitive.ObjectID `bson:"warehouse_id"   json:"warehouseId"`
	ShortID     string             `bson:"short_id"       json:"shortId"`
	Slug        string             `bson:"slug"           json:"slug"`
	Name        string             `bson:"name"           json:"name"`
	City        string             `bson:"city,omitempty" json:"city,omitempty"`
}

// EnquiryNote is one staff note.
type EnquiryNote struct {
	ID       primitive.ObjectID `bson:"id"       json:"id"`
	ByUserID string             `bson:"by"       json:"by"`
	ByEmail  string             `bson:"by_email" json:"byEmail"`
	At       time.Time          `bson:"at"       json:"at"`
	Body     string             `bson:"body"     json:"body"`
}

// EnquiryChange is one status or assignee move.
type EnquiryChange struct {
	At      time.Time `bson:"at"       json:"at"`
	By      string    `bson:"by"       json:"by"`
	ByEmail string    `bson:"by_email" json:"byEmail"`
	Field   string    `bson:"field"    json:"field"`
	From    string    `bson:"from"     json:"from"`
	To      string    `bson:"to"       json:"to"`
}

// EnquiryFilter is the inbox filter. Zero fields are ignored.
type EnquiryFilter struct {
	Status string
	// Assignee is a user id; Unassigned matches enquiries with none.
	Assignee   *primitive.ObjectID
	Unassigned bool
	// ListingShortID matches the listing snapshot.
	ListingShortID string
	// Q is a case-insensitive substring of name, company, email or phone.
	Q    string
	From *time.Time
	To   *time.Time
}

// EnquiryStat is the slice of an enquiry the analytics rollup reads.
type EnquiryStat struct {
	ID        primitive.ObjectID  `bson:"_id"`
	CreatedAt time.Time           `bson:"created_at"`
	Country   string              `bson:"country"`
	SearchID  *primitive.ObjectID `bson:"search_id,omitempty"`
	Listing   *EnquiryListing     `bson:"listing,omitempty"`
}

func enquiries() *mongo.Collection { return Collection(enquiriesCollection) }

// notDeleted hides soft-deleted enquiries (D-019).
var notDeleted = bson.M{"$exists": false}

// EnsureEnquiryIndexes: the inbox views, the listing / search joins, the
// account-deletion cleaner and the per-user idempotency key.
func EnsureEnquiryIndexes(ctx context.Context) error {
	_, err := enquiries().Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "status", Value: 1}, {Key: "created_at", Value: -1}}},
		{Keys: bson.D{{Key: "assignee_user_id", Value: 1}, {Key: "status", Value: 1}}},
		{Keys: bson.D{{Key: "listing.warehouse_id", Value: 1}}},
		{Keys: bson.D{{Key: "user_id", Value: 1}}},
		{Keys: bson.D{{Key: "search_id", Value: 1}}},
		{Keys: bson.D{{Key: "created_at", Value: -1}}},
		{Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "idempotency_key", Value: 1}}, Options: options.Index().SetUnique(true)},
	})
	if err != nil {
		return fmt.Errorf("ensure enquiries indexes: %w", err)
	}
	return nil
}

// InsertEnquiry inserts e; ErrDuplicateKey when the user already sent this
// idempotency key.
func InsertEnquiry(ctx context.Context, e *Enquiry) error {
	return insertDoc(ctx, enquiriesCollection, e, &e.ID)
}

// FindEnquiryByID reads a non-deleted enquiry; ErrNotFound when missing.
func FindEnquiryByID(ctx context.Context, id primitive.ObjectID) (*Enquiry, error) {
	return findOneDoc[Enquiry](ctx, enquiries(), bson.M{"_id": id, "deleted_at": notDeleted})
}

// FindEnquiryByIdempotencyKey reads the user's enquiry sent with key
// (deleted or not); ErrNotFound when missing.
func FindEnquiryByIdempotencyKey(ctx context.Context, userID primitive.ObjectID, key string) (*Enquiry, error) {
	return findOneDoc[Enquiry](ctx, enquiries(), bson.M{"user_id": userID, "idempotency_key": key})
}

// ReplaceEnquiry CAS-replaces e over the non-deleted doc last written at
// expectedUpdatedAt; ErrVersionConflict on a miss.
func ReplaceEnquiry(ctx context.Context, e *Enquiry, expectedUpdatedAt time.Time) error {
	res, err := enquiries().ReplaceOne(ctx, bson.M{"_id": e.ID, "updated_at": expectedUpdatedAt, "deleted_at": notDeleted}, e)
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return ErrVersionConflict
	}
	return nil
}

// PushEnquiryNote appends n and returns the updated enquiry; ErrNotFound
// when missing or deleted.
func PushEnquiryNote(ctx context.Context, id primitive.ObjectID, n EnquiryNote) (*Enquiry, error) {
	var out Enquiry
	err := enquiries().FindOneAndUpdate(ctx,
		bson.M{"_id": id, "deleted_at": notDeleted},
		bson.M{"$push": bson.M{"notes": n}, "$set": bson.M{fieldUpdatedAt: n.At}},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(&out)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func enquiryQuery(f EnquiryFilter) bson.M {
	q := bson.M{"deleted_at": notDeleted}
	if f.Status != "" {
		q["status"] = f.Status
	}
	if f.Assignee != nil {
		q["assignee_user_id"] = *f.Assignee
	} else if f.Unassigned {
		q["assignee_user_id"] = bson.M{"$exists": false}
	}
	if f.ListingShortID != "" {
		q["listing.short_id"] = f.ListingShortID
	}
	if f.Q != "" {
		re := primitive.Regex{Pattern: regexp.QuoteMeta(f.Q), Options: "i"}
		q["$or"] = bson.A{
			bson.M{"name": re}, bson.M{"company": re}, bson.M{"email": re},
			bson.M{"phone": re}, bson.M{"phone_e164": re},
		}
	}
	if f.From != nil || f.To != nil {
		at := bson.M{}
		if f.From != nil {
			at["$gte"] = f.From.UTC()
		}
		if f.To != nil {
			at["$lt"] = f.To.UTC()
		}
		q[fieldCreatedAt] = at
	}
	return q
}

// ListEnquiries pages non-deleted enquiries newest first, without notes and
// history.
func ListEnquiries(ctx context.Context, f EnquiryFilter, page, limit int) ([]Enquiry, int64, error) {
	q := enquiryQuery(f)
	total, err := enquiries().CountDocuments(ctx, q)
	if err != nil {
		return nil, 0, err
	}
	items, err := findAllDocs[Enquiry](ctx, enquiries(), q, options.Find().
		SetSort(bson.D{{Key: fieldCreatedAt, Value: -1}, {Key: fieldID, Value: -1}}).
		SetSkip(skipFor(page, limit)).SetLimit(int64(limit)).
		SetProjection(bson.M{"notes": 0, "history": 0}))
	return items, total, err
}

// ExportEnquiries returns up to limit non-deleted enquiries matching f,
// newest first (CSV export).
func ExportEnquiries(ctx context.Context, f EnquiryFilter, limit int) ([]Enquiry, error) {
	return findAllDocs[Enquiry](ctx, enquiries(), enquiryQuery(f), options.Find().
		SetSort(bson.D{{Key: fieldCreatedAt, Value: -1}, {Key: fieldID, Value: -1}}).
		SetLimit(int64(limit)))
}

// SoftDeleteUserEnquiries hides the user's enquiries from the inbox (D-019),
// keeping contact details. Zero-match-OK; returns how many were hidden.
func SoftDeleteUserEnquiries(ctx context.Context, userID primitive.ObjectID, at time.Time) (int64, error) {
	res, err := enquiries().UpdateMany(ctx,
		bson.M{"user_id": userID, "deleted_at": notDeleted},
		bson.M{"$set": bson.M{"deleted_at": at}})
	if err != nil {
		return 0, err
	}
	return res.ModifiedCount, nil
}

// EnquiryStatsBetween reads every enquiry created in [from, to) — deleted
// ones too, they still count as conversions (analytics, spec 07).
func EnquiryStatsBetween(ctx context.Context, from, to time.Time) ([]EnquiryStat, error) {
	return findAllDocs[EnquiryStat](ctx, enquiries(),
		bson.M{fieldCreatedAt: bson.M{"$gte": from.UTC(), "$lt": to.UTC()}},
		options.Find().SetProjection(bson.M{fieldCreatedAt: 1, "country": 1, "search_id": 1, "listing": 1}))
}
