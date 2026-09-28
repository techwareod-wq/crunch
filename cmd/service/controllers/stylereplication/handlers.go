// Package stylereplication is the /v1/style-replication controller: the
// wizard's run lifecycle (start → urls → approve → done, with cancel) plus
// the saved-profile Remove action. Flat paths, entity resolved from the
// active company (house convention); the whole surface rides
// styleReplication.enabled (404 masquerade) + the style.replication feature.
package stylereplication

import (
	"errors"
	"net/http"
	"strings"

	"github.com/atharva-ng/crunch/internal/config"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
	sr "github.com/atharva-ng/crunch/internal/services/styleReplicationService"
)

// reviewExcerptChars caps the per-URL content preview sent to the review step
// — the card shows a taster, not the whole scraped body.
const reviewExcerptChars = 300

// resolveEntity loads the caller's web entity; a false return means the error
// response was already written (the analytics helpers pattern, minus the WEC
// ids this surface doesn't need).
func resolveEntity(w http.ResponseWriter, r *http.Request) (*models.WebEntity, bool) {
	user := middleware.GetUserFromContext(r)
	active := middleware.GetActiveCompanyFromContext(r)
	logger := middleware.GetLogger(r)

	found, entity, err := models.FindWebEntityForCompany(r.Context(), active.Company.ID, user.ID)
	if err != nil {
		logger.Error("style replication: resolving web entity failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrStyleReplicationFailed)
		return nil, false
	}
	if !found {
		middleware.SendJSONError(w, r, apperrors.ErrWebEntityNotFound)
		return nil, false
	}
	return entity, true
}

// sendServiceError maps the service's typed errors onto the wire.
func sendServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, sr.ErrStyleRunActive):
		middleware.SendJSONError(w, r, apperrors.ErrStyleRunActive)
	case errors.Is(err, sr.ErrStyleRunRateLimited):
		middleware.SendJSONError(w, r, apperrors.ErrStyleRunRateLimited)
	case errors.Is(err, sr.ErrStyleRunNotFound):
		middleware.SendJSONError(w, r, apperrors.ErrStyleRunNotFound)
	case errors.Is(err, sr.ErrStyleRunWrongState):
		middleware.SendJSONError(w, r, apperrors.ErrStyleRunWrongState)
	case errors.Is(err, sr.ErrStyleRunInvalidInput):
		middleware.SendJSONError(w, r, &apperrors.Error{Code: http.StatusBadRequest, Message: err.Error()})
	default:
		middleware.GetLogger(r).Error("style replication: request failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrStyleReplicationFailed)
	}
}

// runView is the poll payload: status plus the phase-appropriate slices.
type runView struct {
	ID     string                     `json:"id"`
	Status int                        `json:"status"`
	Learn  models.StyleLearnSelection `json:"learn"`
	// Candidates: present from awaiting_urls on (step 1's editable list).
	Candidates []models.StyleCandidateURL `json:"candidates,omitempty"`
	// ScrapeStatuses: present from scraping on (step 3's checklist).
	ScrapeStatuses []scrapeStatusView `json:"scrapeStatuses,omitempty"`
	// Images + Contents: the review assets (step 4).
	Images   []models.StyleScrapedImage `json:"images,omitempty"`
	Contents []contentPreview           `json:"contents,omitempty"`
	// Artifacts: present when done (step 5's reveal).
	Artifacts *artifactsView `json:"artifacts,omitempty"`
	Error     string         `json:"error,omitempty"`
}

type scrapeStatusView struct {
	URL    string `json:"url"`
	Done   bool   `json:"done"`
	Failed bool   `json:"failed"`
}

type contentPreview struct {
	URL     string `json:"url"`
	Title   string `json:"title,omitempty"`
	Excerpt string `json:"excerpt"`
}

type artifactsView struct {
	ToneProfile           string `json:"toneProfile,omitempty"`
	StructurePattern      string `json:"structurePattern,omitempty"`
	TitlePattern          string `json:"titlePattern,omitempty"`
	ThumbnailStylePrompt  string `json:"thumbnailStylePrompt,omitempty"`
	MidArticleStylePrompt string `json:"midArticleStylePrompt,omitempty"`
}

func buildRunView(run *models.StyleReplicationRun) *runView {
	v := &runView{
		ID:         run.ID.Hex(),
		Status:     run.Status,
		Learn:      run.Learn,
		Candidates: run.Candidates,
		Error:      run.Error,
	}
	for _, st := range run.ScrapeStatuses {
		v.ScrapeStatuses = append(v.ScrapeStatuses, scrapeStatusView{URL: st.URL, Done: st.Done, Failed: st.Failed})
	}
	if run.Status == models.StyleRunStatusAwaitingReview {
		// Dedupe by URL: the same image (shared banner, logo that slipped the
		// filters) can be scraped off several source pages, and the concurrent
		// scrape workers each push their own copy. First-seen wins; the
		// deselect write flips every copy anyway (arrayFilters $in).
		seen := map[string]bool{}
		for _, img := range run.ScrapedImages {
			if seen[img.URL] {
				continue
			}
			seen[img.URL] = true
			v.Images = append(v.Images, img)
		}
		for _, c := range run.ScrapedContents {
			excerpt := c.Content
			if len(excerpt) > reviewExcerptChars {
				trimmed := excerpt[:reviewExcerptChars]
				if idx := strings.LastIndex(trimmed, " "); idx > 0 {
					trimmed = trimmed[:idx]
				}
				excerpt = trimmed + "…"
			}
			v.Contents = append(v.Contents, contentPreview{URL: c.URL, Title: c.Title, Excerpt: excerpt})
		}
	}
	if run.Status == models.StyleRunStatusDone {
		v.Artifacts = &artifactsView{
			ToneProfile:           run.ToneProfile,
			StructurePattern:      run.StructurePattern,
			TitlePattern:          run.TitlePattern,
			ThumbnailStylePrompt:  run.ThumbnailStylePrompt,
			MidArticleStylePrompt: run.MidArticleStylePrompt,
		}
	}
	return v
}

// HandleRun serves /v1/style-replication/run: GET = the wizard's poll,
// POST = start a new run (the HandleCompany method-switch pattern).
func HandleRun(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		HandleGetRun(w, r)
	case http.MethodPost:
		HandleStartRun(w, r)
	}
}

// HandleStartRun creates a run and dispatches discovery. 202 — the wizard
// polls GET /run for progress.
func HandleStartRun(w http.ResponseWriter, r *http.Request) {
	entity, ok := resolveEntity(w, r)
	if !ok {
		return
	}
	appCtx := config.GetAppContext(r)
	user := middleware.GetUserFromContext(r)

	run, err := appCtx.InternalServices.StyleReplicationService.StartRun(r.Context(), user.ID.Hex(), entity)
	if err != nil {
		sendServiceError(w, r, err)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusAccepted, buildRunView(run))
}

// HandleGetRun is the wizard's poll: the entity's newest run in any state
// (null when the entity never ran).
func HandleGetRun(w http.ResponseWriter, r *http.Request) {
	entity, ok := resolveEntity(w, r)
	if !ok {
		return
	}
	found, run, err := models.FindLatestStyleReplicationRunForEntity(r.Context(), entity.ID)
	if err != nil {
		middleware.GetLogger(r).Error("style replication: load latest run failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrStyleReplicationFailed)
		return
	}
	if !found {
		middleware.SendJSONResponse(w, r, http.StatusOK, map[string]any{"run": nil})
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]any{"run": buildRunView(run)})
}

// submitURLsRequest finalizes step 1+2: the source list and what to learn.
type submitURLsRequest struct {
	URLs  []string                   `json:"urls"`
	Learn models.StyleLearnSelection `json:"learn"`
}

// HandleSubmitURLs kicks off the scrape fan-out for the entity's active run.
func HandleSubmitURLs(w http.ResponseWriter, r *http.Request) {
	body, ok := r.Context().Value(middleware.DeserializerContextKey).(submitURLsRequest)
	if !ok {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}
	entity, okE := resolveEntity(w, r)
	if !okE {
		return
	}
	appCtx := config.GetAppContext(r)
	user := middleware.GetUserFromContext(r)

	found, run, err := models.FindActiveStyleReplicationRunForEntity(r.Context(), entity.ID)
	if err != nil {
		middleware.GetLogger(r).Error("style replication: load active run failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrStyleReplicationFailed)
		return
	}
	if !found {
		middleware.SendJSONError(w, r, apperrors.ErrStyleRunNotFound)
		return
	}

	if err := appCtx.InternalServices.StyleReplicationService.SubmitURLs(r.Context(), user.ID.Hex(), run, body.URLs, body.Learn); err != nil {
		sendServiceError(w, r, err)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusAccepted, map[string]string{"status": "scraping"})
}

// uploadURLRequest asks for a presigned PUT for one user-uploaded reference
// image (the review-step escape hatch when the scraped images don't represent
// the style). Position picks which bucket the image teaches — thumbnail and
// mid-article references feed separate synthesis calls.
type uploadURLRequest struct {
	Position    string `json:"position"` // "thumbnail" | "mid-article"
	ContentType string `json:"contentType"`
}

// HandleCreateUploadURL issues the presigned PUT + public URL for a reference
// image upload against the entity's active run.
func HandleCreateUploadURL(w http.ResponseWriter, r *http.Request) {
	body, ok := r.Context().Value(middleware.DeserializerContextKey).(uploadURLRequest)
	if !ok || body.ContentType == "" || body.Position == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}
	entity, okE := resolveEntity(w, r)
	if !okE {
		return
	}
	appCtx := config.GetAppContext(r)
	user := middleware.GetUserFromContext(r)

	found, run, err := models.FindActiveStyleReplicationRunForEntity(r.Context(), entity.ID)
	if err != nil {
		middleware.GetLogger(r).Error("style replication: load active run failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrStyleReplicationFailed)
		return
	}
	if !found {
		middleware.SendJSONError(w, r, apperrors.ErrStyleRunNotFound)
		return
	}

	target, err := appCtx.InternalServices.StyleReplicationService.CreateReferenceImageUploadURL(r.Context(), user.ID.Hex(), run, body.Position, body.ContentType)
	if err != nil {
		sendServiceError(w, r, err)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, target)
}

// approveRequest carries the review deselections (whole-URL content cards
// aren't deselectable — decision 5; failed URLs are already excluded) plus
// any user-uploaded reference images minted by HandleCreateUploadURL, each
// tagged with the position bucket it teaches.
type approveRequest struct {
	DeselectedImages []string                   `json:"deselectedImages"`
	UploadedImages   []sr.StyleUploadedImageRef `json:"uploadedImages"`
}

// HandleApprove applies deselections and dispatches synthesis.
func HandleApprove(w http.ResponseWriter, r *http.Request) {
	body, ok := r.Context().Value(middleware.DeserializerContextKey).(approveRequest)
	if !ok {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}
	entity, okE := resolveEntity(w, r)
	if !okE {
		return
	}
	appCtx := config.GetAppContext(r)
	user := middleware.GetUserFromContext(r)

	found, run, err := models.FindActiveStyleReplicationRunForEntity(r.Context(), entity.ID)
	if err != nil {
		middleware.GetLogger(r).Error("style replication: load active run failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrStyleReplicationFailed)
		return
	}
	if !found {
		middleware.SendJSONError(w, r, apperrors.ErrStyleRunNotFound)
		return
	}

	if err := appCtx.InternalServices.StyleReplicationService.Approve(r.Context(), user.ID.Hex(), run, body.DeselectedImages, body.UploadedImages); err != nil {
		sendServiceError(w, r, err)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusAccepted, map[string]string{"status": "synthesizing"})
}

// HandleCancelRun abandons the entity's active run.
func HandleCancelRun(w http.ResponseWriter, r *http.Request) {
	entity, ok := resolveEntity(w, r)
	if !ok {
		return
	}
	appCtx := config.GetAppContext(r)

	found, run, err := models.FindActiveStyleReplicationRunForEntity(r.Context(), entity.ID)
	if err != nil {
		middleware.GetLogger(r).Error("style replication: load active run failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrStyleReplicationFailed)
		return
	}
	if !found {
		middleware.SendJSONError(w, r, apperrors.ErrStyleRunNotFound)
		return
	}
	if err := appCtx.InternalServices.StyleReplicationService.Cancel(r.Context(), run); err != nil {
		middleware.GetLogger(r).Error("style replication: cancel failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrStyleReplicationFailed)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]string{"status": "canceled"})
}

// HandleDeleteProfile is the saved-state "Remove" action: the whole learned
// profile is unset in one shot.
func HandleDeleteProfile(w http.ResponseWriter, r *http.Request) {
	entity, ok := resolveEntity(w, r)
	if !ok {
		return
	}
	if err := models.UnsetWebEntityStyleReplication(r.Context(), entity.ID); err != nil {
		middleware.GetLogger(r).Error("style replication: remove profile failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrStyleReplicationFailed)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]string{"status": "removed"})
}
