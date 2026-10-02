package store

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/models"
)

// Store is the enquiry service's data access (Mongo in production).
type Store interface {
	Insert(ctx context.Context, e *models.Enquiry) error // models.ErrDuplicateKey on a repeated idempotency key
	Get(ctx context.Context, id primitive.ObjectID) (*models.Enquiry, error)
	GetByIdempotencyKey(ctx context.Context, userID primitive.ObjectID, key string) (*models.Enquiry, error)
	// Replace CAS-writes e over the doc last written at expectedUpdatedAt.
	Replace(ctx context.Context, e *models.Enquiry, expectedUpdatedAt time.Time) error
	PushNote(ctx context.Context, id primitive.ObjectID, n models.EnquiryNote) (*models.Enquiry, error)
	List(ctx context.Context, f models.EnquiryFilter, page, limit int) ([]models.Enquiry, int64, error)
	Export(ctx context.Context, f models.EnquiryFilter, limit int) ([]models.Enquiry, error)
	SoftDeleteUser(ctx context.Context, userID primitive.ObjectID, at time.Time) (int64, error)

	WarehouseByShortID(ctx context.Context, shortID string) (*models.Warehouse, error)
	Warehouse(ctx context.Context, id primitive.ObjectID) (*models.Warehouse, error)
	SearchEvent(ctx context.Context, id primitive.ObjectID) (*models.SearchEvent, error)
	// User reads an active user; models.ErrNotFound when missing.
	User(ctx context.Context, id primitive.ObjectID) (*models.User, error)
	UpdateUserProfile(ctx context.Context, id primitive.ObjectID, phone, phoneE164, company string, at time.Time) error
}

type store struct{}

func NewStore() Store {
	return &store{}
}

func (s *store) Insert(ctx context.Context, e *models.Enquiry) error {
	return models.InsertEnquiry(ctx, e)
}

func (s *store) Get(ctx context.Context, id primitive.ObjectID) (*models.Enquiry, error) {
	return models.FindEnquiryByID(ctx, id)
}

func (s *store) GetByIdempotencyKey(ctx context.Context, userID primitive.ObjectID, key string) (*models.Enquiry, error) {
	return models.FindEnquiryByIdempotencyKey(ctx, userID, key)
}

func (s *store) Replace(ctx context.Context, e *models.Enquiry, expectedUpdatedAt time.Time) error {
	return models.ReplaceEnquiry(ctx, e, expectedUpdatedAt)
}

func (s *store) PushNote(ctx context.Context, id primitive.ObjectID, n models.EnquiryNote) (*models.Enquiry, error) {
	return models.PushEnquiryNote(ctx, id, n)
}

func (s *store) List(ctx context.Context, f models.EnquiryFilter, page, limit int) ([]models.Enquiry, int64, error) {
	return models.ListEnquiries(ctx, f, page, limit)
}

func (s *store) Export(ctx context.Context, f models.EnquiryFilter, limit int) ([]models.Enquiry, error) {
	return models.ExportEnquiries(ctx, f, limit)
}

func (s *store) SoftDeleteUser(ctx context.Context, userID primitive.ObjectID, at time.Time) (int64, error) {
	return models.SoftDeleteUserEnquiries(ctx, userID, at)
}

func (s *store) WarehouseByShortID(ctx context.Context, shortID string) (*models.Warehouse, error) {
	return models.FindWarehouseByShortID(ctx, shortID)
}

func (s *store) Warehouse(ctx context.Context, id primitive.ObjectID) (*models.Warehouse, error) {
	return models.FindWarehouseByID(ctx, id)
}

func (s *store) SearchEvent(ctx context.Context, id primitive.ObjectID) (*models.SearchEvent, error) {
	return models.FindSearchEventByID(ctx, id)
}

func (s *store) User(ctx context.Context, id primitive.ObjectID) (*models.User, error) {
	found, u, err := models.FindUserByID(ctx, id.Hex())
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, models.ErrNotFound
	}
	return u, nil
}

func (s *store) UpdateUserProfile(ctx context.Context, id primitive.ObjectID, phone, phoneE164, company string, at time.Time) error {
	return models.UpdateUser(ctx, id.Hex(), bson.M{
		"phone":              phone,
		"phone_e164":         phoneE164,
		"company":            company,
		"profile_updated_at": at,
	})
}
