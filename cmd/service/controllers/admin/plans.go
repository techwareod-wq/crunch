package admin

import (
	"net/http"

	"github.com/atharva-ng/crunch/internal/authz"
	"github.com/atharva-ng/crunch/internal/config"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
)

// Plan-catalog CRUD (plan §8). Path is /v1/admin/plans — deliberately outside
// /v1/admin/payments/* because /v1/admin/payments/plans already serves the
// Paddle price listing and the route registry panics on duplicate paths.

// handleAdminPlans serves GET (list) and POST/PUT (upsert by app_id+tier) on
// /v1/admin/plans. Manual decode: one path, several verbs.
func handleAdminPlans(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		handleAdminListPlans(w, r)
		return
	}

	// Writes are superuser-tier (billing/pricing). The path-level gate only
	// guaranteed plans.read (so admins can list); the mutating verbs require
	// plans.write in-handler because the registry is path-only. A 403 here is
	// audited by the gate.
	if !middleware.CallerHasPermission(r, authz.PermPlansWrite) {
		middleware.SendJSONError(w, r, apperrors.ErrRoleEscalation)
		return
	}

	plan, decodeErr := middleware.DecodeJSONBody[models.Plan](r.Body)
	if decodeErr != nil {
		middleware.SendJSONError(w, r, decodeErr)
		return
	}

	appCtx := config.GetAppContext(r)

	// Structural + uniqueness validation over the MERGED catalog (this doc +
	// every doc it doesn't replace) so a write can never make AppForPrice
	// ambiguous. SeedPlans revalidates before writing.
	existing, err := models.ListAllPlans(r.Context())
	if err != nil {
		middleware.GetLogger(r).Error("failed to load plans for validation", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrAdminCheckFailed)
		return
	}
	merged := []models.Plan{plan}
	for _, p := range existing {
		if !(p.AppID == plan.AppID && p.Tier == plan.Tier) {
			merged = append(merged, p)
		}
	}
	if err := models.ValidatePlanPriceUniqueness(merged); err != nil {
		middleware.GetLogger(r).Warn("plan write rejected", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrInvalidPlanDoc)
		return
	}

	// Every price must be a live Paddle price — a typo'd price_id would
	// quarantine that price's webhooks forever.
	prices, err := appCtx.PaddleProvider.ListActivePrices(r.Context(), appCtx.Config.Paddle.ProductID)
	if err != nil {
		middleware.GetLogger(r).Error("failed to list paddle prices for validation", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrPaymentProviderFailed)
		return
	}
	live := make(map[string]bool, len(prices))
	for _, p := range prices {
		live[p.ID] = true
	}
	for _, id := range plan.PriceIDs() {
		if !live[id] {
			middleware.GetLogger(r).Warn("plan write rejected: price not in paddle", "price_id", id)
			middleware.SendJSONError(w, r, apperrors.ErrInvalidPlanDoc)
			return
		}
	}

	report, err := models.SeedPlans(r.Context(), []models.Plan{plan}, false)
	if err != nil {
		middleware.GetLogger(r).Error("failed to write plan", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrAdminCheckFailed)
		return
	}
	if err := appCtx.PlansCache.Reload(r.Context()); err != nil {
		// The doc is written; the ticker retries the reload within a minute.
		middleware.GetLogger(r).Error("plans cache reload after write failed", "error", err)
	}

	middleware.GetLogger(r).Info("admin wrote plan",
		"app_id", plan.AppID, "tier", plan.Tier,
		"created", report.Created, "updated", report.Updated, "unchanged", report.Unchanged)
	middleware.SendJSONResponse(w, r, http.StatusOK, report)
}

func handleAdminListPlans(w http.ResponseWriter, r *http.Request) {
	appID := r.URL.Query().Get("appId")
	var (
		plans []models.Plan
		err   error
	)
	if appID != "" {
		plans, err = models.ListPlansByApp(r.Context(), appID)
	} else {
		plans, err = models.ListAllPlans(r.Context())
	}
	if err != nil {
		middleware.GetLogger(r).Error("failed to list plans", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrAdminCheckFailed)
		return
	}
	if plans == nil {
		plans = []models.Plan{}
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, plans)
}
