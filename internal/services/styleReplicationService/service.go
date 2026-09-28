// Package styleReplicationService owns the style replication feature: point
// Indexly at ~10 existing articles (the user's own or a competitor's), scrape
// them, and have an LLM derive three reusable artifacts — a tone profile, a
// structure pattern, and an image style prompt — persisted on the WebEntity
// and injected into every generation path. See [[Style Replication]].
package styleReplicationService

import (
	"context"
	"errors"
	"net/http"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/pipeline"
)

// Async process types. Discovery and scrape ride the primary queue; synthesis
// is an LLM call and rides the secondary (rate-limited) queue.
const (
	ProcessStyleDiscovery pipeline.ProcessType = "STYLE_REPLICATION_DISCOVERY"
	ProcessStyleScrape    pipeline.ProcessType = "STYLE_REPLICATION_SCRAPE"
	ProcessStyleSynthesis pipeline.ProcessType = "STYLE_REPLICATION_SYNTHESIS"
)

// StyleRunPayload identifies a run for the discovery/synthesis handlers.
type StyleRunPayload struct {
	RunID string `json:"runId"`
}

// StyleScrapePayload is one URL of the scrape fan-out.
type StyleScrapePayload struct {
	RunID string `json:"runId"`
	URL   string `json:"url"`
}

// Typed errors the synchronous entry points surface for the HTTP layer.
var (
	// ErrStyleRunActive: the entity already has an in-flight run (409).
	ErrStyleRunActive = errors.New("a style replication run is already active")
	// ErrStyleRunRateLimited: the entity hit maxRunsPerDay (429).
	ErrStyleRunRateLimited = errors.New("style replication daily run limit reached")
	// ErrStyleRunNotFound: no run in the state the call requires (404/409).
	ErrStyleRunNotFound = errors.New("style replication run not found")
	// ErrStyleRunWrongState: the run exists but is not in the state the call
	// requires (double-submit, stale client) (409).
	ErrStyleRunWrongState = errors.New("style replication run is not awaiting this action")
	// ErrStyleRunInvalidInput: malformed URL list / learn selection (400).
	ErrStyleRunInvalidInput = errors.New("invalid style replication input")
)

// StyleImageUploadTarget is the presigned PUT + public URL for one
// user-uploaded reference image (the ImageUploadTarget shape).
type StyleImageUploadTarget struct {
	Key       string      `json:"key"`
	UploadURL string      `json:"uploadUrl"`
	Headers   http.Header `json:"headers,omitempty"`
	PublicURL string      `json:"publicUrl"`
}

// StyleUploadedImageRef ties an uploaded reference key to the image position
// it should teach — thumbnail and mid-article references feed separate
// synthesis calls.
type StyleUploadedImageRef struct {
	Key      string `json:"key"`
	Position string `json:"position"` // models.ImagePositionThumbnail | models.ImagePositionMidArticle
}

type StyleReplicationService interface {
	// StartRun guards the rate limit + single-active-run invariant, creates
	// the run doc, and dispatches discovery. Returns the created run.
	StartRun(ctx context.Context, userID string, entity *models.WebEntity) (*models.StyleReplicationRun, error)

	// SubmitURLs finalizes the source list (1-maxUrls validated URLs) and the
	// learn selection, then dispatches the scrape fan-out.
	SubmitURLs(ctx context.Context, userID string, run *models.StyleReplicationRun, urls []string, learn models.StyleLearnSelection) error

	// CreateReferenceImageUploadURL issues a presigned single-part PUT for a
	// user-uploaded reference image for one position (the escape hatch when
	// none of the scraped images represent the style). Only valid while the
	// run awaits review; the key lives under the run's upload namespace so
	// Approve can validate that a returned key really came from here.
	CreateReferenceImageUploadURL(ctx context.Context, userID string, run *models.StyleReplicationRun, position, contentType string) (*StyleImageUploadTarget, error)

	// Approve applies the review deselections, registers any user-uploaded
	// reference images (keys from CreateReferenceImageUploadURL, tagged with
	// their position), and dispatches synthesis.
	Approve(ctx context.Context, userID string, run *models.StyleReplicationRun, deselectedImageURLs []string, uploadedImages []StyleUploadedImageRef) error

	// Cancel abandons the active run, freeing the single-active-run slot.
	Cancel(ctx context.Context, run *models.StyleReplicationRun) error

	// Async handlers (SQS).
	HandleDiscovery(ctx context.Context, userID string, payload StyleRunPayload) error
	HandleScrape(ctx context.Context, userID string, payload StyleScrapePayload) error
	HandleSynthesis(ctx context.Context, userID string, payload StyleRunPayload) error
}
