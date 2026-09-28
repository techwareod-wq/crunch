package scheduledArticleService

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/scheduledArticleService/dto"
)

var (
	// ErrWebEntityNotFound is returned when the requested web entity does not
	// exist or does not belong to the requesting user.
	ErrWebEntityNotFound = errors.New("web entity not found")

	// ErrScheduledArticleNotFound is returned when the requested scheduled
	// article does not exist or does not belong to the requesting user.
	ErrScheduledArticleNotFound = errors.New("scheduled article not found")

	// ErrArticleNotGenerated is returned when a save-draft request targets a
	// scheduled article whose content pipeline has not produced a master context
	// yet — there is nothing to attach edits to.
	ErrArticleNotGenerated = errors.New("article has not been generated yet")

	// ErrScheduledArticleNotEditable is returned when a dashboard edit targets an
	// article whose lifecycle state forbids edits (generating, published, or
	// scheduling). Past and future unpublished rows remain editable. The dashboard
	// edit API surfaces a 409 for this case.
	ErrScheduledArticleNotEditable = errors.New("scheduled article is not editable")

	// ErrFieldNotEditableForStatus is returned when the dashboard payload tries to
	// edit a field that the article's current status doesn't allow — e.g. trying
	// to change article type on a draft / readyForReview row.
	ErrFieldNotEditableForStatus = errors.New("field not editable for this status")

	// ErrInvalidScheduleDate is retained for malformed schedule-date strings on
	// create/edit HTTP handlers. Past calendar dates are accepted for both
	// ScheduleArticle and UpdateFromDashboard.
	ErrInvalidScheduleDate = errors.New("schedule date must be today or later")

	// ErrInvalidArticleType is returned when the requested article type does not
	// match any value in models.AllArticleTypes.
	ErrInvalidArticleType = errors.New("invalid article type")

	// ErrInvalidThumbnailStyle is returned when a dashboard edit sets a thumbnail
	// style that isn't a known catalog style. Surfaced as a 400. A reset-to-inherit
	// (empty string) is not subject to this check.
	ErrInvalidThumbnailStyle = errors.New("invalid thumbnail style")

	// ErrInvalidImagePosition is returned when an image edit targets a position
	// that isn't one of the article's known image slots ("thumbnail" /
	// "mid-article").
	ErrInvalidImagePosition = errors.New("invalid image position")

	// ErrInvalidImageContentType is returned when an image-upload request asks for
	// a presigned URL with a content type that isn't a supported raster image.
	ErrInvalidImageContentType = errors.New("invalid image content type")

	// ErrInvalidImageKey is returned when a save-draft image entry carries a new
	// S3 key outside the server-issued user-uploaded namespace for this user +
	// article. Any of the user's own uploads for the article may back a slot (so an
	// inline upload can be promoted to the thumbnail), but a key belonging to
	// another user or article is rejected, guarding against a client pointing the
	// stored image at an arbitrary object.
	ErrInvalidImageKey = errors.New("invalid image key")
)

// SaveDraftPayload is the partial edit set sent from the review screen. All
// fields are taken as-is; empty strings overwrite the underlying pipeline
// values (i.e. "cleared by the user"). Title is the exception — it lives on
// the ScheduledArticle doc rather than the master-context edited block, and
// an empty value is treated as "not provided" rather than a clear, since a
// titleless article would break the calendar list view.
type SaveDraftPayload struct {
	Title           string
	ArticleContent  string
	MetaTitle       string
	MetaDescription string
	URLSlug         string
	// Images carries the per-position image edits made from the review screen:
	// updated alt text and, when the user replaced an image, the S3 key of the
	// object they uploaded to the user-uploaded namespace via a presigned URL.
	// Positions not present here are left untouched.
	Images []SaveDraftImage
}

// SaveDraftImage is a single per-position image edit from the review screen.
// Alt is always applied to the structured image record. NewS3Key is set only
// when the user replaced the image: the service swaps the stored key, rewrites
// the body's image URL, and deletes the previous object. An empty NewS3Key
// means "alt-only edit, keep the existing image".
type SaveDraftImage struct {
	Position string
	Alt      string
	NewS3Key string
}

// ImageUploadTarget is the presigned single-part PUT the client uses to upload
// a replacement image straight to S3, plus the public URL the object will be
// reachable at once uploaded (handed back so the editor can preview it and so
// the same URL round-trips into the saved article body).
type ImageUploadTarget struct {
	Key       string      `json:"key"`
	UploadURL string      `json:"uploadUrl"`
	Headers   http.Header `json:"headers,omitempty"`
	PublicURL string      `json:"publicUrl"`
}

// OrchestrateContext carries the persisted fields needed to fire a CGE
// orchestrate run for an existing calendar slot. Resolved server-side so
// callers (e.g. the manual "Generate now" trigger) only need to pass a
// scheduled article id — keyword/context ownership is the server's job.
type OrchestrateContext struct {
	WebEntityContextID     string
	KeywordID              string
	ArticleType            string
	Title                  string
	AdditionalInstructions string
	// InternalLinkingEnabled is the article's per-article internal-linking
	// override. Nil means "inherit the web entity default" — resolved when the
	// master context is created.
	InternalLinkingEnabled *bool
	// ThumbnailStyle is the article's per-article thumbnail-style override. Nil
	// means "inherit the web entity default" — resolved when the master context
	// is created. Passed through raw (nullable).
	ThumbnailStyle *string
	// Status is the slot's lifecycle state at resolve time. The trial article
	// cap keys on it: only a slot that has never started generating consumes
	// the cap; re-triggering an already-started slot is count-neutral.
	Status models.ScheduledArticleStatus
}

// DashboardEditPayload is the partial-edit payload the dashboard sidebar /
// drag-and-drop sends through the new edit endpoint. Each field is optional;
// nil means "leave the persisted value untouched". The service applies
// status-based gating before forwarding to the store. Title/ArticleType clear
// is not supported — both are required identity fields for a slot.
type DashboardEditPayload struct {
	Title       *string
	ArticleType *string
	// Reasoning is the retitle reasoning generated when the user changed the
	// article type (via the retitle-preview endpoint). Persisted alongside the
	// new title + type; ignored unless an article-type change is in the payload.
	Reasoning              *string
	AdditionalInstructions *string
	// ScheduleDate is interpreted as a UTC midnight day boundary by the
	// service. Callers should pass time.Date(y, m, d, 0, 0, 0, 0, time.UTC).
	ScheduleDate *time.Time
	// InternalLinkingEnabled toggles the per-article internal-link insertion
	// step. nil leaves the persisted value untouched. Editable on the same
	// terms as AdditionalInstructions (scheduled slots, any date).
	InternalLinkingEnabled *bool
	// ThumbnailStyle overrides the per-article thumbnail style:
	//   nil                 → leave the persisted value untouched
	//   non-nil empty string → reset to inherit the WebEntity default
	//   non-nil non-empty   → set the override (validated against the catalog)
	// Editable on the same terms as AdditionalInstructions — the style is a
	// generation-time input, so post-generation edits are meaningless in v1.
	ThumbnailStyle *string
}

// ScheduledArticleService is a read-only view over the publish calendar. It is
// deliberately independent of the Scheduling Engine write pipeline.
type ScheduledArticleService interface {
	// GetScheduledArticles returns the user's scheduled articles for a web
	// entity within [from, to). When from or to is nil, the current week
	// (Monday–Sunday, UTC) is used instead.
	GetScheduledArticles(ctx context.Context, userId, webEntityId string, from, to *time.Time) (*dto.ScheduledArticlesResponse, error)

	// GetScheduledArticleStatuses returns the compact polling view of the user's
	// scheduled articles for a web entity within [from, to) — the same window
	// semantics as GetScheduledArticles, but only the fields a live poller needs
	// (status, title, word count, publish timing + outcome). Skips the keyword
	// hydration the full view does. Backs the `view=status` query on the list
	// route.
	GetScheduledArticleStatuses(ctx context.Context, userId, webEntityId string, from, to *time.Time) (*dto.ScheduledArticlesStatusResponse, error)

	// GetArticleByScheduleID returns the calendar slot paired with any
	// generated content (final HTML, meta, schema, images). When the article
	// has not been generated yet, only the slot is populated.
	GetArticleByScheduleID(ctx context.Context, userId, scheduledArticleID string) (*dto.ArticleDetailResponse, error)

	// ResolveOrchestrateContext loads the scheduled article and returns the
	// fields the CGE orchestrate entry point needs (keyword id, web entity
	// context id, plus the persisted article type and title as defaults).
	// Verifies the article belongs to the user — returns
	// ErrScheduledArticleNotFound for both missing and unauthorised slots
	// to avoid leaking existence.
	ResolveOrchestrateContext(ctx context.Context, userId, scheduledArticleID string) (*OrchestrateContext, error)

	// SaveDraft persists the user's edits onto the master context for the
	// given calendar slot and flips the scheduled article status to `draft`.
	// Alongside the text fields it applies per-position image edits: updated
	// alt text, and image replacements (swap the stored S3 key, rewrite the
	// body URL, delete the previous object best-effort). Returns
	// ErrScheduledArticleNotFound when the slot is missing or owned by another
	// user, ErrArticleNotGenerated when no master context exists yet,
	// ErrInvalidImagePosition for an unknown image position, and
	// ErrInvalidImageKey when a replacement key is outside this user's
	// user-uploaded namespace for the article + position.
	SaveDraft(ctx context.Context, userId, scheduledArticleID string, payload SaveDraftPayload) error

	// CreateImageUploadURL issues a presigned single-part PUT the client uses
	// to upload a replacement image for the given article + position directly
	// to S3 (under the user-uploaded namespace), plus the public URL the object
	// will be served at. The returned key is what the client echoes back in the
	// next SaveDraft call. Verifies article ownership; returns
	// ErrScheduledArticleNotFound for missing/unauthorised slots,
	// ErrArticleNotGenerated when there is no generated content to attach an
	// image to, ErrInvalidImagePosition / ErrInvalidImageContentType for bad
	// inputs.
	CreateImageUploadURL(ctx context.Context, userId, scheduledArticleID, position, contentType string) (*ImageUploadTarget, error)

	// CreateInlineImageUploadURL issues a presigned single-part PUT for an image
	// the user inserts inline into the article body via the editor toolbar,
	// directly to S3 under the user-uploaded namespace, plus the public URL the
	// object will be served at. Unlike CreateImageUploadURL it is not bound to a
	// structured image slot: the public URL is embedded straight into the saved
	// article content, so there is no position and no key to echo back on
	// SaveDraft. Verifies article ownership; returns ErrScheduledArticleNotFound
	// for missing/unauthorised slots, ErrArticleNotGenerated when there is no
	// generated content to insert an image into, and ErrInvalidImageContentType
	// for an unsupported type.
	CreateInlineImageUploadURL(ctx context.Context, userId, scheduledArticleID, contentType string) (*ImageUploadTarget, error)

	// DiscardPendingRegen is the regen undo: removes exactly one pending
	// regeneration — a section rewrite (by id) or an image slot (by position;
	// the discarded pending object is deleted from S3 best-effort). Exactly
	// one of sectionID / imagePosition must be set. Discarding an entry that
	// no longer exists is a no-op, not an error (it may have been settled from
	// another tab). Returns ErrScheduledArticleNotFound for missing or foreign
	// slots, ErrArticleNotGenerated when no master context exists, and
	// ErrInvalidImagePosition for an unknown position.
	DiscardPendingRegen(ctx context.Context, userId, scheduledArticleID string, sectionID *int64, imagePosition *string) error

	// UpdateFromDashboard applies a partial edit from the dashboard sidebar
	// (or drag-and-drop reschedule). Enforces status-based field restrictions:
	//   - generating / published / scheduling → no edits at all
	//   - scheduled → Title / ArticleType / AdditionalInstructions /
	//     ScheduleDate editable (past and future slots alike)
	//   - draft / readyForReview → Title + ScheduleDate editable
	// A ScheduleDate may be past, today, or future (UTC). A same-day reschedule
	// also stamps PublishAt with an auto-publish target an hour out.
	UpdateFromDashboard(ctx context.Context, userId, scheduledArticleID string, payload DashboardEditPayload) error

	// DeleteScheduledArticle permanently removes a calendar slot (and any
	// generated master context attached to it). Returns
	// ErrScheduledArticleNotFound when the slot is missing or owned by another
	// user, and ErrScheduledArticleNotEditable when the article is mid-pipeline
	// (generating), where a delete would race the running generation.
	DeleteScheduledArticle(ctx context.Context, userId, scheduledArticleID string) error

	// SetPublishState persists the per-article live/draft override (writes an
	// explicit override — inherit-until-touched). Allowed in every state except
	// mid-generation. Uses a focused write path rather than UpdateFromDashboard
	// because the control must remain editable when the article is published
	// (drives Republish), which the dashboard-edit path rejects. Returns
	// ErrScheduledArticleNotFound for missing/unauthorised slots and
	// ErrScheduledArticleNotEditable while generating.
	SetPublishState(ctx context.Context, userId, scheduledArticleID string, live bool) error
}
