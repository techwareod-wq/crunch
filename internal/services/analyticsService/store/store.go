package store

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/models"
)

// Store is the analytics service's data access (Mongo in production).
type Store interface {
	InsertEvent(ctx context.Context, e *models.SearchEvent) error
	// EachEvent streams the events with at in [from, to), without filters.
	EachEvent(ctx context.Context, from, to time.Time, fn func(models.SearchEvent) error) error
	EventRefs(ctx context.Context, ids []primitive.ObjectID) ([]models.SearchEventRef, error)
	ListEvents(ctx context.Context, f models.SearchEventFilter, page, limit int) ([]models.SearchEvent, int64, error)
	UnsetUser(ctx context.Context, userID primitive.ObjectID) (int64, error)

	// Enquiries reads the enquiries created in [from, to).
	Enquiries(ctx context.Context, from, to time.Time) ([]models.EnquiryStat, error)

	// Days reads the rollups dated in [from, to] (YYYY-MM-DD).
	Days(ctx context.Context, from, to string) ([]models.SearchDay, error)
	ReplaceDays(ctx context.Context, date string, rows []models.SearchDay) error
	// RolledThrough is the last rolled-up day as YYYYMMDD (0 = never).
	RolledThrough(ctx context.Context) (int64, error)
	SetRolledThrough(ctx context.Context, day int64) error
}

type store struct{}

func NewStore() Store {
	return &store{}
}

func (s *store) InsertEvent(ctx context.Context, e *models.SearchEvent) error {
	return models.InsertSearchEvent(ctx, e)
}

func (s *store) EachEvent(ctx context.Context, from, to time.Time, fn func(models.SearchEvent) error) error {
	return models.EachSearchEvent(ctx, from, to, fn)
}

func (s *store) EventRefs(ctx context.Context, ids []primitive.ObjectID) ([]models.SearchEventRef, error) {
	return models.SearchEventRefs(ctx, ids)
}

func (s *store) ListEvents(ctx context.Context, f models.SearchEventFilter, page, limit int) ([]models.SearchEvent, int64, error) {
	return models.ListSearchEvents(ctx, f, page, limit)
}

func (s *store) UnsetUser(ctx context.Context, userID primitive.ObjectID) (int64, error) {
	return models.UnsetSearchEventsUser(ctx, userID)
}

func (s *store) Enquiries(ctx context.Context, from, to time.Time) ([]models.EnquiryStat, error) {
	return models.EnquiryStatsBetween(ctx, from, to)
}

func (s *store) Days(ctx context.Context, from, to string) ([]models.SearchDay, error) {
	return models.ListSearchDays(ctx, from, to)
}

func (s *store) ReplaceDays(ctx context.Context, date string, rows []models.SearchDay) error {
	return models.ReplaceSearchDays(ctx, date, rows)
}

func (s *store) RolledThrough(ctx context.Context) (int64, error) {
	return models.GetCounter(ctx, models.CounterAnalyticsRolledThrough)
}

func (s *store) SetRolledThrough(ctx context.Context, day int64) error {
	return models.RaiseCounter(ctx, models.CounterAnalyticsRolledThrough, day)
}
