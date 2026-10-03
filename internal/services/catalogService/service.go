// Package catalogService is the WarehouseHub catalog (spec 03): warehouses
// and their revision lifecycle (draft → review → live → archived), rent
// terms, media uploads, geocoding, slugs and the public listing API.
package catalogService

import (
	"context"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/pipeline"
	"github.com/atharva-ng/crunch/internal/services/catalogService/dto"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

// Save warnings (D-061).
const (
	WarnPinManual       = "address_changed_pin_manual"
	WarnGeocodeOff      = "geocoding_unavailable"
	WarnAddressNotFound = "address_not_found"
	WarnGeocodePending  = "geocode_pending"
)

// Async work.
const (
	// ProcessGeocodeRetry re-geocodes a draft whose address couldn't be
	// geocoded on save (D-061). SQS retries give the backoff.
	ProcessGeocodeRetry pipeline.ProcessType = "catalog.geocode_retry"
	// ProcessMediaGC is the daily media sweep (cron catalog_media_gc).
	ProcessMediaGC pipeline.ProcessType = "catalog.media_gc"
)

// GeocodeRetryPayload is the catalog.geocode_retry body.
type GeocodeRetryPayload struct {
	RevisionID  string `json:"revisionId"`
	AddressHash string `json:"addressHash"`
}

// SaveRequest is a draft save (CAS on rev, D-052).
type SaveRequest struct {
	RevisionID primitive.ObjectID    `json:"revisionId"`
	Rev        int                   `json:"rev"`
	Content    models.ListingContent `json:"content"`
}

// RevisionResult is returned by every revision write.
type RevisionResult struct {
	Revision *models.WarehouseRevision `json:"revision"`
	Preview  *dto.Preview              `json:"preview,omitempty"`
	Warnings []string                  `json:"warnings,omitempty"`
}

// BulkItem is one bulk-approve result.
type BulkItem struct {
	RevisionID string `json:"revisionId"`
	OK         bool   `json:"ok"`
	Code       string `json:"code,omitempty"`
	Error      string `json:"error,omitempty"`
}

// UploadRequest is POST /v1/admin/media/upload-url.
type UploadRequest struct {
	WarehouseID primitive.ObjectID `json:"warehouseId"`
	Kind        string             `json:"kind"`
	DocType     string             `json:"docType"`
	Visibility  string             `json:"visibility"`
	Filename    string             `json:"filename"`
	ContentType string             `json:"contentType"`
	Bytes       int64              `json:"bytes"`
}

// UploadResponse carries the presigned PUT.
type UploadResponse struct {
	MediaID string              `json:"mediaId"`
	PutURL  string              `json:"putUrl"`
	Headers map[string][]string `json:"headers,omitempty"`
}

// NeedsInfoAnswer answers one Needs-info item on one warehouse (D-039):
// Status yes (with Fields holding at least the node's required values) or
// no for an unknown node; Status empty to fill missing fields on a yes node.
type NeedsInfoAnswer struct {
	WarehouseID primitive.ObjectID            `json:"warehouseId"`
	Node        string                        `json:"node"`
	Status      models.NodeStatus             `json:"status,omitempty"`
	Fields      map[string]*models.FieldValue `json:"fields,omitempty"`
}

// AnswerRequest is POST /v1/admin/needs-info/answer.
type AnswerRequest struct {
	Items []NeedsInfoAnswer `json:"items"`
	// Submit sends each saved draft to review under the shared batchId.
	Submit bool `json:"submit"`
}

// AnswerItem is one warehouse's result. OK = its answers were saved;
// Submitted = it also went to review.
type AnswerItem struct {
	WarehouseID string `json:"warehouseId"`
	OK          bool   `json:"ok"`
	RevisionID  string `json:"revisionId,omitempty"`
	Submitted   bool   `json:"submitted"`
	Code        string `json:"code,omitempty"`
	Error       string `json:"error,omitempty"`
}

// LookupResult is the public slug lookup outcome (spec 03 Slugs): a listing
// (200), a redirect (301), or gone with nearby listings (410).
type LookupResult struct {
	Listing    *dto.PublicListing
	RedirectTo string
	IsGone     bool
	Nearby     []domain.ListingCard
}

// CatalogService runs the listing lifecycle and the public listing API.
type CatalogService interface {
	// Lifecycle. Every transition is a CAS on the revision (state + rev)
	// and writes change_log.
	Create(ctx context.Context, actor domain.Actor, content models.ListingContent) (*models.Warehouse, RevisionResult, error)
	Open(ctx context.Context, actor domain.Actor, warehouseID primitive.ObjectID) (RevisionResult, error)
	Restore(ctx context.Context, actor domain.Actor, warehouseID primitive.ObjectID) (RevisionResult, error)
	Save(ctx context.Context, actor domain.Actor, req SaveRequest, batchID string) (RevisionResult, error)
	Submit(ctx context.Context, actor domain.Actor, revisionID primitive.ObjectID, batchID string) (RevisionResult, error)
	Withdraw(ctx context.Context, actor domain.Actor, revisionID primitive.ObjectID) (RevisionResult, error)
	Reject(ctx context.Context, actor domain.Actor, revisionID primitive.ObjectID, comment string) (RevisionResult, error)
	Discard(ctx context.Context, actor domain.Actor, revisionID primitive.ObjectID) (RevisionResult, error)
	Approve(ctx context.Context, actor domain.Actor, revisionID primitive.ObjectID, batchID string) (RevisionResult, error)
	BulkApprove(ctx context.Context, actor domain.Actor, revisionIDs []primitive.ObjectID) (string, []BulkItem, error)
	Archive(ctx context.Context, actor domain.Actor, warehouseID primitive.ObjectID) error
	Delete(ctx context.Context, actor domain.Actor, warehouseID primitive.ObjectID) error

	// Admin reads.
	ListWarehouses(ctx context.Context, f models.WarehouseFilter, page, limit int) ([]models.Warehouse, int64, error)
	WarehouseDetail(ctx context.Context, warehouseID primitive.ObjectID) (*dto.WarehouseDetail, error)
	GetRevision(ctx context.Context, revisionID primitive.ObjectID) (*models.WarehouseRevision, error)
	RevisionHistory(ctx context.Context, warehouseID primitive.ObjectID) ([]models.WarehouseRevision, error)
	ReviewQueue(ctx context.Context, page, limit int) ([]models.WarehouseRevision, int64, error)

	// Needs-info queue (spec 02, D-039): live warehouses only.
	NeedsInfoSummary(ctx context.Context) ([]dto.NeedsInfoKey, error)
	NeedsInfoList(ctx context.Context, key string, page, limit int) ([]dto.NeedsInfoWarehouse, int64, error)
	// AnswerNeedsInfo applies answers per warehouse into its open draft
	// (opened from live when none), optionally submitting; in-review
	// warehouses are skipped. Everything goes through review (D-050).
	AnswerNeedsInfo(ctx context.Context, actor domain.Actor, req AnswerRequest) (string, []AnswerItem, error)

	// Geocoding (D-061).
	GeocodePreview(ctx context.Context, addr models.Address) (models.Location, error)

	// Media (D-015, D-062).
	MediaUploadURL(ctx context.Context, actor domain.Actor, req UploadRequest) (UploadResponse, error)
	ConfirmMedia(ctx context.Context, mediaID primitive.ObjectID) (*models.WarehouseMedia, error)
	MediaLink(ctx context.Context, mediaID primitive.ObjectID) (string, error)
	ListMedia(ctx context.Context, warehouseID primitive.ObjectID) ([]models.WarehouseMedia, error)

	// Public (no auth).
	PublicListing(ctx context.Context, slug string) (LookupResult, error)
	PublicSlugs(ctx context.Context, page int, withCover bool) ([]dto.SlugItem, error)

	// Async handlers.
	GeocodeRetry(ctx context.Context, p GeocodeRetryPayload) error
	MediaGC(ctx context.Context) error

	// SetSearchEngine wires search (04) for the 410 page's nearby list.
	SetSearchEngine(e domain.SearchEngine)
}
