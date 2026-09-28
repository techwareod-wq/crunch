package scheduledarticles

import (
	"net/http"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/entitlements"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
)

func Handle(appCtx *config.AppContext) {
	middleware.Handle("/v1/scheduled-articles", http.HandlerFunc(HandleGetScheduledArticles)).
		WithFeature(models.AppIDIndexly, entitlements.FeatureScheduleView).
		WithJWTAuthentication().
		WithMethods("GET").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/scheduled-articles/article", http.HandlerFunc(HandleGetArticleBySchedule)).
		WithFeature(models.AppIDIndexly, entitlements.FeatureScheduleView).
		WithJWTAuthentication().
		WithMethods("GET").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/scheduled-articles/schedule", http.HandlerFunc(HandleScheduleArticle)).
		WithFeature(models.AppIDIndexly, entitlements.FeatureArticleSchedule).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[scheduleArticleRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	// `/article/delete` rather than re-using `/article` because the route
	// registry is path-only (no method-based dispatch), so GET and DELETE on
	// the same path would clash at registration time.
	middleware.Handle("/v1/scheduled-articles/article/delete", http.HandlerFunc(HandleDeleteScheduledArticle)).
		WithFeature(models.AppIDIndexly, entitlements.FeatureArticleEdit).
		WithJWTAuthentication().
		WithMethods("DELETE").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/scheduled-articles/article/draft", http.HandlerFunc(HandleSaveArticleDraft)).
		WithFeature(models.AppIDIndexly, entitlements.FeatureArticleEdit).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[saveDraftRequest]()).
		WithMethods("PATCH").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/scheduled-articles/article/image-upload-url", http.HandlerFunc(HandleCreateImageUploadURL)).
		WithFeature(models.AppIDIndexly, entitlements.FeatureArticleEdit).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[imageUploadURLRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/scheduled-articles/article/inline-image-upload-url", http.HandlerFunc(HandleCreateInlineImageUploadURL)).
		WithFeature(models.AppIDIndexly, entitlements.FeatureArticleEdit).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[inlineImageUploadURLRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/scheduled-articles/article/suggest-titles", http.HandlerFunc(HandleSuggestTitles)).
		WithFeature(models.AppIDIndexly, entitlements.FeatureArticleAIAssist).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[suggestTitlesRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	middleware.Handle("/v1/scheduled-articles/article/retitle-preview", http.HandlerFunc(HandleRetitlePreview)).
		WithFeature(models.AppIDIndexly, entitlements.FeatureArticleAIAssist).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[retitlePreviewRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	// Stateless section rewrite: returns regenerated markdown for a selected
	// passage; the client splices it into the editor and commits via
	// /article/draft. Own path — the route registry is path-only.
	middleware.Handle("/v1/scheduled-articles/article/regenerate-section", http.HandlerFunc(HandleRegenerateSection)).
		WithFeature(models.AppIDIndexly, entitlements.FeatureArticleAIAssist).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[regenerateSectionRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	// Single-image regen to a fresh versioned S3 key, recorded as the slot's
	// pending entry. The stored article is untouched — the client previews the
	// URL and commits the key via /article/draft (images[].newS3Key), like a
	// user-uploaded replacement.
	middleware.Handle("/v1/scheduled-articles/article/regenerate-image", http.HandlerFunc(HandleRegenerateImage)).
		WithFeature(models.AppIDIndexly, entitlements.FeatureArticleAIAssist).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[regenerateImageRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	// Regen undo: drops one pending regeneration (and garbage-collects a
	// discarded pending image object). Gated on article.edit, not ai_assist —
	// undoing must work even where AI assistance doesn't.
	middleware.Handle("/v1/scheduled-articles/article/regenerate-discard", http.HandlerFunc(HandleDiscardRegen)).
		WithFeature(models.AppIDIndexly, entitlements.FeatureArticleEdit).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[discardRegenRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	// `/article/edit` rather than re-using `/article` because the route
	// registry is path-only (no method-based dispatch), so GET and PATCH on
	// the same path would clash at registration time.
	middleware.Handle("/v1/scheduled-articles/article/edit", http.HandlerFunc(HandleUpdateScheduledArticle)).
		WithFeature(models.AppIDIndexly, entitlements.FeatureArticleEdit).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[updateScheduledArticleRequest]()).
		WithMethods("PATCH").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()

	// Per-article live/draft override. Separate from `/article/edit` because it
	// must remain writable when the article is published (drives Republish),
	// which the dashboard-edit path rejects.
	middleware.Handle("/v1/scheduled-articles/article/publish-state", http.HandlerFunc(HandleSetPublishState)).
		WithFeature(models.AppIDIndexly, entitlements.FeatureArticleEdit).
		WithJWTAuthentication().
		With(middleware.DeserializeJson[setPublishStateRequest]()).
		WithMethods("POST").
		With(appCtx.Middleware()).
		AllowCORS().
		WithLogEnabled()
}
