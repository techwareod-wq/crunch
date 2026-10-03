// Package enquiryService is WarehouseHub enquiries (spec 06): the signed-in
// visitor's enquiry form (optionally about a listing, D-101) and the admin
// inbox (status, assignee, notes, history, CSV export). Enquiry emails
// (D-103) are deferred until email is re-added.
package enquiryService

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/accountService"
	"github.com/atharva-ng/crunch/internal/services/enquiryService/dto"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// SubmitRequest is POST /v1/enquiries. The email comes from the signed-in
// user, never the body.
type SubmitRequest struct {
	Name           string `json:"name"`
	Company        string `json:"company"`
	Phone          string `json:"phone"`
	Message        string `json:"message"`
	ListingShortID string `json:"listingShortId,omitempty"`
	SearchID       string `json:"searchId,omitempty"`
	SessionID      string `json:"sessionId,omitempty"`
	IdempotencyKey string `json:"idempotencyKey"`
}

// SubmitResult is the submit outcome. Created is false when the
// idempotency key had already been used (the existing id is returned).
type SubmitResult struct {
	ID      string `json:"id"`
	Created bool   `json:"-"`
}

// StatusRequest is POST /v1/admin/enquiries/status (CAS on updatedAt).
type StatusRequest struct {
	ID                primitive.ObjectID `json:"id"`
	Status            string             `json:"status"`
	CloseReason       string             `json:"closeReason,omitempty"`
	CloseNote         string             `json:"closeNote,omitempty"`
	ExpectedUpdatedAt time.Time          `json:"expectedUpdatedAt"`
}

// AssignRequest is POST /v1/admin/enquiries/assign (CAS on updatedAt). A
// nil assignee unassigns.
type AssignRequest struct {
	ID                primitive.ObjectID  `json:"id"`
	AssigneeUserID    *primitive.ObjectID `json:"assigneeUserId"`
	ExpectedUpdatedAt time.Time           `json:"expectedUpdatedAt"`
}

// NoteRequest is POST /v1/admin/enquiries/notes.
type NoteRequest struct {
	ID   primitive.ObjectID `json:"id"`
	Body string             `json:"body"`
}

// EnquiryService runs the enquiry form and the inbox.
type EnquiryService interface {
	// Submit validates and stores a visitor's enquiry and saves their phone
	// + company to their profile (P-4). A repeated idempotency key returns
	// the existing id.
	Submit(ctx context.Context, user *models.User, req SubmitRequest) (SubmitResult, error)

	// Inbox (deleted enquiries are hidden, D-019).
	List(ctx context.Context, f models.EnquiryFilter, page, limit int) ([]models.Enquiry, int64, error)
	Detail(ctx context.Context, id primitive.ObjectID) (*dto.EnquiryDetail, error)
	Export(ctx context.Context, f models.EnquiryFilter) ([]models.Enquiry, error)

	// Inbox writes. Each one writes change_log.
	SetStatus(ctx context.Context, actor domain.Actor, req StatusRequest) (*models.Enquiry, error)
	Assign(ctx context.Context, actor domain.Actor, req AssignRequest) (*models.Enquiry, error)
	AddNote(ctx context.Context, actor domain.Actor, req NoteRequest) (*models.Enquiry, error)

	// Cleaner soft-deletes a deleted account's enquiries (D-019).
	Cleaner() accountService.DataCleaner
}
