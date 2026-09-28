package analytics

import (
	"net/http"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/analytics/queries"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
)

// HandlePagesTable: per-page rollup + decay flags + isArticle.
func HandlePagesTable(w http.ResponseWriter, r *http.Request) {
	window, err := gscWindow(r)
	if err != nil {
		middleware.SendJSONResponse(w, r, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	req, ok := resolveEntity(w, r)
	if !ok {
		return
	}
	rows, err := queries.PagesTable(r.Context(), req.Entity.ID, window.From, window.To, window.Settled)
	if err != nil {
		middleware.GetLogger(r).Error("analytics: pages table failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrAnalyticsFailed)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]any{
		"pages": rows, "from": window.From, "to": window.To,
	})
}

// HandleArticlesTable: per-article rollup joined to scheduledArticle/keyword —
// target-keyword tracking, traffic value, demand capture.
func HandleArticlesTable(w http.ResponseWriter, r *http.Request) {
	window, err := gscWindow(r)
	if err != nil {
		middleware.SendJSONResponse(w, r, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	req, ok := resolveEntity(w, r)
	if !ok {
		return
	}
	rows, err := queries.ArticlesTable(r.Context(), req.Entity.ID, req.WECIDs, window.From, window.To, window.Settled)
	if err != nil {
		middleware.GetLogger(r).Error("analytics: articles table failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrAnalyticsFailed)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]any{
		"articles": rows, "from": window.From, "to": window.To,
	})
}

// HandleArticleDetail: one article's trend + top queries + target-keyword
// line. Query param: scheduledArticleId.
func HandleArticleDetail(w http.ResponseWriter, r *http.Request) {
	articleHex := r.URL.Query().Get("scheduledArticleId")
	articleID, err := primitive.ObjectIDFromHex(articleHex)
	if err != nil {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}
	window, err := gscWindow(r)
	if err != nil {
		middleware.SendJSONResponse(w, r, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	req, ok := resolveEntity(w, r)
	if !ok {
		return
	}
	ctx := r.Context()

	// The article must belong to the caller's entity — analytics rows are
	// entity-scoped, but the id comes from the client.
	found, article, err := models.GetScheduledArticle(ctx, articleHex)
	if err != nil {
		middleware.GetLogger(r).Error("analytics: article lookup failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrAnalyticsFailed)
		return
	}
	if !found || article.WebEntityID != req.Entity.ID {
		middleware.SendJSONError(w, r, apperrors.ErrScheduledArticleNotFound)
		return
	}

	detail, err := queries.ArticleDetailView(ctx, req.Entity.ID, req.WECIDs, articleID, window.From, window.To, window.Settled)
	if err != nil {
		middleware.GetLogger(r).Error("analytics: article detail failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrAnalyticsFailed)
		return
	}
	if detail.Title == "" {
		detail.Title = article.Title
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]any{
		"article": detail, "from": window.From, "to": window.To,
	})
}

// HandleQueriesTable: site query table; ?strikingDistance=true filters to the
// position 5–20 band with an impressions floor.
func HandleQueriesTable(w http.ResponseWriter, r *http.Request) {
	window, err := gscWindow(r)
	if err != nil {
		middleware.SendJSONResponse(w, r, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	req, ok := resolveEntity(w, r)
	if !ok {
		return
	}
	striking := r.URL.Query().Get("strikingDistance") == "true"
	rows, err := queries.QueriesTable(r.Context(), req.Entity.ID, req.WECIDs, window.From, window.To, window.Settled, striking)
	if err != nil {
		middleware.GetLogger(r).Error("analytics: queries table failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrAnalyticsFailed)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]any{
		"queries": rows, "from": window.From, "to": window.To,
	})
}
