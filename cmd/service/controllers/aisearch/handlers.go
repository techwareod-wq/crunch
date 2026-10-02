package aisearch

import (
	"errors"
	"net/http"

	"github.com/atharva-ng/crunch/internal/config"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/aiSearchService"
	"github.com/atharva-ng/crunch/internal/services/searchService"
)

func service(r *http.Request) aiSearchService.AISearchService {
	return config.GetAppContext(r).InternalServices.AISearchService
}

func viewerOf(r *http.Request) searchService.Viewer {
	u := middleware.GetOptionalUserFromContext(r)
	if u == nil {
		return searchService.Viewer{}
	}
	return searchService.Viewer{UserID: u.ID.Hex(), Staff: u.Role != "" && u.Role != models.RoleUser}
}

func HandleSearch(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(aiSearchService.Request)
	if !ok {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}
	resp, err := service(r).Search(r.Context(), req, viewerOf(r))
	var ve *aiSearchService.ValidationError
	switch {
	case errors.As(err, &ve):
		middleware.SendJSONError(w, r, &apperrors.Error{Code: http.StatusBadRequest, Message: ve.Msg, ErrCode: "invalid"})
	case err != nil:
		middleware.GetLogger(r).Error("ai search failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrInternal)
	default:
		middleware.SendJSONResponse(w, r, http.StatusOK, resp)
	}
}

func HandleReembedAll(w http.ResponseWriter, r *http.Request) {
	run, err := service(r).StartReembedAll(r.Context())
	if err != nil {
		middleware.GetLogger(r).Error("reembed-all dispatch failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrInternal)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusAccepted, map[string]string{"run": run})
}
