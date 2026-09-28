package admin

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

type adminProcessRequest struct {
	UserID      string `json:"userId"`
	WebEntityID string `json:"webEntityId"`
}

type adminDispatchFunnelClassificationRequest struct {
	UserID             string `json:"userId"`
	WebEntityContextID string `json:"webEntityContextId"`
}

type adminDispatchOpportunityScoreRequest struct {
	UserID             string `json:"userId"`
	WebEntityContextID string `json:"webEntityContextId"`
}

type adminDispatchClusteringRequest struct {
	UserID             string `json:"userId"`
	WebEntityContextID string `json:"webEntityContextId"`
}

type adminAddManualKeywordRequest struct {
	UserID      string `json:"userId"`
	WebEntityID string `json:"webEntityId"`
	Keyword     string `json:"keyword"`
}

// HandleAdminProcess mirrors POST /v1/site-intelligence/process for a target
// user — the full SIE pipeline (DataForSEO + clustering LLM spend).
func HandleAdminProcess(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(adminProcessRequest)
	if !ok || req.WebEntityID == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	target, r, appErr := resolveTargetUser(r, req.UserID)
	if appErr != nil {
		middleware.SendJSONError(w, r, appErr)
		return
	}

	appCtx := config.GetAppContext(r)
	logger := middleware.GetLogger(r)

	err := appCtx.InternalServices.SiteIntelligenceService.Orchestrate(r.Context(), target.ID.Hex(), req.WebEntityID)
	if err != nil {
		logger.Error("Failed to orchestrate site intelligence", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrSiteIntelligenceProcessingFailed)
		return
	}

	middleware.SendJSONResponse(w, r, http.StatusAccepted, map[string]string{"status": "processing"})
}

// HandleAdminDispatchFunnelClassification mirrors the user equivalent for a
// target user — repair/ops re-dispatch.
func HandleAdminDispatchFunnelClassification(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(adminDispatchFunnelClassificationRequest)
	if !ok || req.WebEntityContextID == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	target, r, appErr := resolveTargetUser(r, req.UserID)
	if appErr != nil {
		middleware.SendJSONError(w, r, appErr)
		return
	}

	appCtx := config.GetAppContext(r)
	logger := middleware.GetLogger(r)

	err := appCtx.InternalServices.SiteIntelligenceService.DispatchFunnelClassification(r.Context(), target.ID.Hex(), req.WebEntityContextID)
	if err != nil {
		logger.Error("Failed to dispatch funnel classification", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrSiteIntelligenceProcessingFailed)
		return
	}

	middleware.SendJSONResponse(w, r, http.StatusAccepted, map[string]string{"status": "processing"})
}

// HandleAdminDispatchOpportunityScore mirrors the user equivalent for a target
// user.
func HandleAdminDispatchOpportunityScore(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(adminDispatchOpportunityScoreRequest)
	if !ok || req.WebEntityContextID == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	target, r, appErr := resolveTargetUser(r, req.UserID)
	if appErr != nil {
		middleware.SendJSONError(w, r, appErr)
		return
	}

	appCtx := config.GetAppContext(r)
	logger := middleware.GetLogger(r)

	err := appCtx.InternalServices.SiteIntelligenceService.DispatchOpportunityScore(r.Context(), target.ID.Hex(), req.WebEntityContextID)
	if err != nil {
		logger.Error("Failed to dispatch opportunity score", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrSiteIntelligenceProcessingFailed)
		return
	}

	middleware.SendJSONResponse(w, r, http.StatusAccepted, map[string]string{"status": "processing"})
}

// HandleAdminDispatchClustering mirrors the user equivalent for a target user.
func HandleAdminDispatchClustering(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(adminDispatchClusteringRequest)
	if !ok || req.WebEntityContextID == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	target, r, appErr := resolveTargetUser(r, req.UserID)
	if appErr != nil {
		middleware.SendJSONError(w, r, appErr)
		return
	}

	appCtx := config.GetAppContext(r)
	logger := middleware.GetLogger(r)

	err := appCtx.InternalServices.SiteIntelligenceService.DispatchClustering(r.Context(), target.ID.Hex(), req.WebEntityContextID)
	if err != nil {
		logger.Error("Failed to dispatch clustering", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrSiteIntelligenceProcessingFailed)
		return
	}

	middleware.SendJSONResponse(w, r, http.StatusAccepted, map[string]string{"status": "clustering"})
}

// HandleAdminAddManualKeyword mirrors POST /v1/site-intelligence/manual-keyword
// for a target user (DataForSEO enrichment spend).
func HandleAdminAddManualKeyword(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(adminAddManualKeywordRequest)
	if !ok || req.WebEntityID == "" || req.Keyword == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	target, r, appErr := resolveTargetUser(r, req.UserID)
	if appErr != nil {
		middleware.SendJSONError(w, r, appErr)
		return
	}

	appCtx := config.GetAppContext(r)
	logger := middleware.GetLogger(r)

	resp, err := appCtx.InternalServices.SiteIntelligenceService.AddManualKeyword(r.Context(), target.ID.Hex(), sieDto.AddManualKeywordRequest{
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

	// Same status split as the user route: suggestions insert nothing and start
	// no enrichment, so 200 OK; an enriching keyword is 202 Accepted.
	if resp.Status == "suggestions" {
		middleware.SendJSONResponse(w, r, http.StatusOK, resp)
		return
	}

	middleware.SendJSONResponse(w, r, http.StatusAccepted, resp)
}

// HandleAdminGetKeywordData mirrors GET /v1/site-intelligence/keyword-data for
// a target user (?userId=&webEntityId=).
func HandleAdminGetKeywordData(w http.ResponseWriter, r *http.Request) {
	webEntityId := r.URL.Query().Get("webEntityId")
	if webEntityId == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	target, r, appErr := resolveTargetUser(r, r.URL.Query().Get("userId"))
	if appErr != nil {
		middleware.SendJSONError(w, r, appErr)
		return
	}

	appCtx := config.GetAppContext(r)
	logger := middleware.GetLogger(r)

	resp, err := appCtx.InternalServices.SiteIntelligenceService.GetKeywordData(r.Context(), target.ID.Hex(), webEntityId)
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

// HandleAdminGetClusterSupporting mirrors GET
// /v1/site-intelligence/cluster-supporting for a target user
// (?userId=&webEntityId=&clusterIndex=&page=).
func HandleAdminGetClusterSupporting(w http.ResponseWriter, r *http.Request) {
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

	page := 0
	if raw := r.URL.Query().Get("page"); raw != "" {
		parsed, perr := strconv.Atoi(raw)
		if perr != nil || parsed < 0 {
			middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
			return
		}
		page = parsed
	}

	target, r, appErr := resolveTargetUser(r, r.URL.Query().Get("userId"))
	if appErr != nil {
		middleware.SendJSONError(w, r, appErr)
		return
	}

	appCtx := config.GetAppContext(r)
	logger := middleware.GetLogger(r)

	resp, err := appCtx.InternalServices.SiteIntelligenceService.GetClusterSupportingKeywords(r.Context(), target.ID.Hex(), webEntityId, clusterIndex, page)
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

// HandleAdminSearchKeywords mirrors GET /v1/site-intelligence/keyword-search
// for a target user (?userId=&webEntityId=&q=&status=&usage=).
func HandleAdminSearchKeywords(w http.ResponseWriter, r *http.Request) {
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

	target, r, appErr := resolveTargetUser(r, r.URL.Query().Get("userId"))
	if appErr != nil {
		middleware.SendJSONError(w, r, appErr)
		return
	}

	appCtx := config.GetAppContext(r)
	logger := middleware.GetLogger(r)

	resp, err := appCtx.InternalServices.SiteIntelligenceService.SearchKeywords(r.Context(), target.ID.Hex(), webEntityId, query, status, usage)
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
