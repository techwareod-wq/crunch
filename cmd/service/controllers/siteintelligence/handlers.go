package siteintelligence

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/atharva-ng/crunch/internal/config"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	sieDto "github.com/atharva-ng/crunch/internal/services/siteIntelligenceEngine/dto"
	sieService "github.com/atharva-ng/crunch/internal/services/siteIntelligenceEngine/service"
)

type processRequest struct {
	WebEntityID string `json:"webEntityId"`
}

type dispatchFunnelClassificationRequest struct {
	WebEntityContextID string `json:"webEntityContextId"`
}

type dispatchOpportunityScoreRequest struct {
	WebEntityContextID string `json:"webEntityContextId"`
}

type dispatchClusteringRequest struct {
	WebEntityContextID string `json:"webEntityContextId"`
}

type addManualKeywordRequest struct {
	WebEntityID string `json:"webEntityId"`
	Keyword     string `json:"keyword"`
}

func HandleProcess(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(processRequest)
	if !ok {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	if req.WebEntityID == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	userFromContext := middleware.GetUserFromContext(r)
	userId := userFromContext.ID.Hex()

	appCtx := config.GetAppContext(r)
	ctx := r.Context()
	logger := middleware.GetLogger(r)

	sieService := appCtx.InternalServices.SiteIntelligenceService

	err := sieService.Orchestrate(ctx, userId, req.WebEntityID)
	if err != nil {
		logger.Error("Failed to orchestrate site intelligence", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrSiteIntelligenceProcessingFailed)
		return
	}

	middleware.SendJSONResponse(w, r, http.StatusAccepted, map[string]string{"status": "processing"})
}

func HandleDispatchFunnelClassification(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(dispatchFunnelClassificationRequest)
	if !ok {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	if req.WebEntityContextID == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	userFromContext := middleware.GetUserFromContext(r)
	userId := userFromContext.ID.Hex()

	appCtx := config.GetAppContext(r)
	ctx := r.Context()
	logger := middleware.GetLogger(r)

	sieService := appCtx.InternalServices.SiteIntelligenceService

	err := sieService.DispatchFunnelClassification(ctx, userId, req.WebEntityContextID)
	if err != nil {
		logger.Error("Failed to dispatch funnel classification", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrSiteIntelligenceProcessingFailed)
		return
	}

	middleware.SendJSONResponse(w, r, http.StatusAccepted, map[string]string{"status": "processing"})
}

func HandleDispatchOpportunityScore(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(dispatchOpportunityScoreRequest)
	if !ok {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	if req.WebEntityContextID == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	userFromContext := middleware.GetUserFromContext(r)
	userId := userFromContext.ID.Hex()

	appCtx := config.GetAppContext(r)
	ctx := r.Context()
	logger := middleware.GetLogger(r)

	sieService := appCtx.InternalServices.SiteIntelligenceService

	err := sieService.DispatchOpportunityScore(ctx, userId, req.WebEntityContextID)
	if err != nil {
		logger.Error("Failed to dispatch opportunity score", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrSiteIntelligenceProcessingFailed)
		return
	}

	middleware.SendJSONResponse(w, r, http.StatusAccepted, map[string]string{"status": "processing"})
}

func HandleGetKeywordData(w http.ResponseWriter, r *http.Request) {
	webEntityId := r.URL.Query().Get("webEntityId")
	if webEntityId == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	userFromContext := middleware.GetUserFromContext(r)
	userId := userFromContext.ID.Hex()

	appCtx := config.GetAppContext(r)
	ctx := r.Context()
	logger := middleware.GetLogger(r)

	resp, err := appCtx.InternalServices.SiteIntelligenceService.GetKeywordData(ctx, userId, webEntityId)
	if err != nil {
		switch {
		case errors.Is(err, sieService.ErrWebEntityContextNotFound):
			middleware.SendJSONError(w, r, apperrors.ErrWebEntityContextNotFound)
		case errors.Is(err, sieService.ErrPipelineNotReady):
			middleware.SendJSONError(w, r, apperrors.ErrSiteIntelligenceNotReady)
		default:
			logger.Error("Failed to get keyword data", "error", err)
			middleware.SendJSONError(w, r, apperrors.ErrSiteIntelligenceProcessingFailed)
		}
		return
	}

	middleware.SendJSONResponse(w, r, http.StatusOK, resp)
}

// HandleGetClusterSupporting serves one page of a single cluster's supporting
// keywords so the full keyword-data payload never ships every keyword at once.
// Query params: webEntityId (required), clusterIndex (required, zero-based),
// page (optional, defaults to 0).
func HandleGetClusterSupporting(w http.ResponseWriter, r *http.Request) {
	webEntityId := r.URL.Query().Get("webEntityId")
	clusterIndexRaw := r.URL.Query().Get("clusterIndex")
	if webEntityId == "" || clusterIndexRaw == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	clusterIndex, err := strconv.Atoi(clusterIndexRaw)
	if err != nil || clusterIndex < 0 {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	// page is optional; a missing or malformed value falls back to the first page.
	page := 0
	if raw := r.URL.Query().Get("page"); raw != "" {
		parsed, perr := strconv.Atoi(raw)
		if perr != nil || parsed < 0 {
			middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
			return
		}
		page = parsed
	}

	userFromContext := middleware.GetUserFromContext(r)
	userId := userFromContext.ID.Hex()

	appCtx := config.GetAppContext(r)
	ctx := r.Context()
	logger := middleware.GetLogger(r)

	resp, err := appCtx.InternalServices.SiteIntelligenceService.GetClusterSupportingKeywords(ctx, userId, webEntityId, clusterIndex, page)
	if err != nil {
		switch {
		case errors.Is(err, sieService.ErrWebEntityContextNotFound):
			middleware.SendJSONError(w, r, apperrors.ErrWebEntityContextNotFound)
		case errors.Is(err, sieService.ErrClusterNotFound):
			middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		case errors.Is(err, sieService.ErrPipelineNotReady):
			middleware.SendJSONError(w, r, apperrors.ErrSiteIntelligenceNotReady)
		default:
			logger.Error("Failed to get cluster supporting keywords", "error", err)
			middleware.SendJSONError(w, r, apperrors.ErrSiteIntelligenceProcessingFailed)
		}
		return
	}

	middleware.SendJSONResponse(w, r, http.StatusOK, resp)
}

// HandleSearchKeywords searches a web entity's keywords across every cluster and
// every page. The keyword-data payload carries only the first page of each
// cluster, so a client filtering that payload in memory can only search a slice
// of the keyword set — this endpoint is how the article picker searches the
// whole thing. Query params: webEntityId (required), q (optional; empty returns
// the highest-opportunity keywords), status (optional, e.g. "queued"), usage
// (optional: "all"/"used"/"unused" — filters on whether the keyword already
// carries an article).
func HandleSearchKeywords(w http.ResponseWriter, r *http.Request) {
	webEntityId := r.URL.Query().Get("webEntityId")
	if webEntityId == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	query := r.URL.Query().Get("q")
	status := r.URL.Query().Get("status")
	usage, ok := sieDto.ParseKeywordUsage(r.URL.Query().Get("usage"))
	if !ok {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	userFromContext := middleware.GetUserFromContext(r)
	userId := userFromContext.ID.Hex()

	appCtx := config.GetAppContext(r)
	ctx := r.Context()
	logger := middleware.GetLogger(r)

	resp, err := appCtx.InternalServices.SiteIntelligenceService.SearchKeywords(ctx, userId, webEntityId, query, status, usage)
	if err != nil {
		switch {
		case errors.Is(err, sieService.ErrWebEntityContextNotFound):
			middleware.SendJSONError(w, r, apperrors.ErrWebEntityContextNotFound)
		case errors.Is(err, sieService.ErrPipelineNotReady):
			middleware.SendJSONError(w, r, apperrors.ErrSiteIntelligenceNotReady)
		default:
			logger.Error("Failed to search keywords", "error", err)
			middleware.SendJSONError(w, r, apperrors.ErrSiteIntelligenceProcessingFailed)
		}
		return
	}

	middleware.SendJSONResponse(w, r, http.StatusOK, resp)
}

func HandleAddManualKeyword(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(addManualKeywordRequest)
	if !ok {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	if req.WebEntityID == "" || req.Keyword == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	userFromContext := middleware.GetUserFromContext(r)
	userId := userFromContext.ID.Hex()

	appCtx := config.GetAppContext(r)
	ctx := r.Context()
	logger := middleware.GetLogger(r)

	resp, err := appCtx.InternalServices.SiteIntelligenceService.AddManualKeyword(ctx, userId, sieDto.AddManualKeywordRequest{
		WebEntityID: req.WebEntityID,
		Keyword:     req.Keyword,
	})
	if err != nil {
		switch {
		case errors.Is(err, sieService.ErrWebEntityContextNotFound):
			middleware.SendJSONError(w, r, apperrors.ErrWebEntityContextNotFound)
		case errors.Is(err, sieService.ErrPipelineNotReady):
			middleware.SendJSONError(w, r, apperrors.ErrSiteIntelligenceNotReady)
		case errors.Is(err, sieService.ErrManualKeywordDuplicate):
			middleware.SendJSONError(w, r, apperrors.ErrManualKeywordDuplicate)
		case errors.Is(err, sieService.ErrManualKeywordNoData):
			middleware.SendJSONError(w, r, apperrors.ErrManualKeywordNoData)
		default:
			logger.Error("Failed to add manual keyword", "error", err)
			middleware.SendJSONError(w, r, apperrors.ErrSiteIntelligenceProcessingFailed)
		}
		return
	}

	// A suggestions response inserts nothing and starts no enrichment, so 200 OK
	// is more accurate than the 202 Accepted used when a keyword is enriching.
	if resp.Status == "suggestions" {
		middleware.SendJSONResponse(w, r, http.StatusOK, resp)
		return
	}

	middleware.SendJSONResponse(w, r, http.StatusAccepted, resp)
}

func HandleDispatchClustering(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(dispatchClusteringRequest)
	if !ok {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	if req.WebEntityContextID == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	userFromContext := middleware.GetUserFromContext(r)
	userId := userFromContext.ID.Hex()

	appCtx := config.GetAppContext(r)
	ctx := r.Context()
	logger := middleware.GetLogger(r)

	sieService := appCtx.InternalServices.SiteIntelligenceService

	err := sieService.DispatchClustering(ctx, userId, req.WebEntityContextID)
	if err != nil {
		logger.Error("Failed to dispatch clustering", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrSiteIntelligenceProcessingFailed)
		return
	}

	middleware.SendJSONResponse(w, r, http.StatusAccepted, map[string]string{"status": "clustering"})
}
