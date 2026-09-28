package admin

import (
	"errors"
	"net/http"
	"time"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/entitlements"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
)

// Entitlements admin surface (plan §8): inspect a user's projection +
// overrides + effective set, replace overrides (optimistic concurrency), and
// recompute projections (the standing drift-repair tool). Overrides write
// dotted paths only — never the projection.

type adminOverrideEntryDTO struct {
	Key       string     `json:"key"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	By        string     `json:"by,omitempty"`
	At        *time.Time `json:"at,omitempty"`
}

type adminEntitlementResponse struct {
	AppID string `json:"appId"`

	// projection (derived)
	Status      string     `json:"status,omitempty"`
	PriceID     string     `json:"priceId,omitempty"`
	ValidTill   *time.Time `json:"validTill,omitempty"`
	LastEventAt *time.Time `json:"lastEventAt,omitempty"`
	Ver         int64      `json:"ver"`

	// overrides
	Grants             []adminOverrideEntryDTO `json:"grants"`
	Revokes            []adminOverrideEntryDTO `json:"revokes"`
	OverridesUpdatedAt *time.Time              `json:"overridesUpdatedAt,omitempty"`

	// comp (admin complimentary access, §3.7)
	Comp          bool       `json:"comp"`
	CompGrantedBy string     `json:"compGrantedBy,omitempty"`
	CompGrantedAt *time.Time `json:"compGrantedAt,omitempty"`
	CompValidTill *time.Time `json:"compValidTill,omitempty"`

	// effective set via the resolver (nil = no access at all)
	Effective []string `json:"effective"`
}

func overrideDTOs(entries []models.OverrideEntry) []adminOverrideEntryDTO {
	out := make([]adminOverrideEntryDTO, 0, len(entries))
	for _, e := range entries {
		at := e.At
		out = append(out, adminOverrideEntryDTO{Key: e.Key, ExpiresAt: e.ExpiresAt, By: e.By, At: &at})
	}
	return out
}

// HandleAdminGetEntitlements serves GET /v1/admin/entitlements?targetUserId=&appId=.
func HandleAdminGetEntitlements(w http.ResponseWriter, r *http.Request) {
	target, r, appErr := resolveTargetUser(r, r.URL.Query().Get("targetUserId"))
	if appErr != nil {
		middleware.SendJSONError(w, r, appErr)
		return
	}
	appID := r.URL.Query().Get("appId")
	if appID == "" {
		appID = models.AppIDIndexly
	}

	appCtx := config.GetAppContext(r)
	ent := target.Entitlements[appID]

	resp := adminEntitlementResponse{
		AppID:              appID,
		Status:             ent.Status,
		PriceID:            ent.PriceID,
		Ver:                ent.Ver,
		Grants:             overrideDTOs(ent.Grants),
		Revokes:            overrideDTOs(ent.Revokes),
		OverridesUpdatedAt: ent.OverridesUpdatedAt,
		Effective:          entitlements.EffectiveFeatures(target, appID, appCtx.PlansCache, time.Now().UTC()),
	}
	if !ent.ValidTill.IsZero() {
		v := ent.ValidTill
		resp.ValidTill = &v
	}
	if !ent.LastEventAt.IsZero() {
		v := ent.LastEventAt
		resp.LastEventAt = &v
	}
	if ent.Comp != nil {
		resp.Comp = ent.Comp.Active(time.Now().UTC())
		resp.CompGrantedBy = ent.Comp.GrantedBy
		at := ent.Comp.GrantedAt
		resp.CompGrantedAt = &at
		resp.CompValidTill = ent.Comp.ValidTill
	}
	if resp.Effective == nil {
		resp.Effective = []string{}
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, resp)
}

type adminPutOverridesRequest struct {
	TargetUserID      string                  `json:"targetUserId"`
	AppID             string                  `json:"appId"`
	Grants            []adminOverrideEntryDTO `json:"grants"`
	Revokes           []adminOverrideEntryDTO `json:"revokes"`
	ExpectedUpdatedAt *time.Time              `json:"expectedUpdatedAt"`
}

// validateOverrideKeys rejects unknown feature keys — a typo'd revoke that
// silently no-ops would leave an admin believing access was removed.
func validateOverrideKeys(entries []adminOverrideEntryDTO) bool {
	for _, e := range entries {
		if !entitlements.IsKnownFeature(e.Key) {
			return false
		}
	}
	return true
}

// HandleAdminPutOverrides serves PUT /v1/admin/entitlements/overrides.
func HandleAdminPutOverrides(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(adminPutOverridesRequest)
	if !ok {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}
	if !validateOverrideKeys(req.Grants) || !validateOverrideKeys(req.Revokes) {
		middleware.SendJSONError(w, r, apperrors.ErrUnknownFeatureKey)
		return
	}

	target, r, appErr := resolveTargetUser(r, req.TargetUserID)
	if appErr != nil {
		middleware.SendJSONError(w, r, appErr)
		return
	}
	appID := req.AppID
	if appID == "" {
		appID = models.AppIDIndexly
	}

	adminEmail := middleware.GetUserFromContext(r).Email
	now := time.Now().UTC()
	toEntries := func(dtos []adminOverrideEntryDTO) []models.OverrideEntry {
		out := make([]models.OverrideEntry, 0, len(dtos))
		for _, d := range dtos {
			out = append(out, models.OverrideEntry{Key: d.Key, ExpiresAt: d.ExpiresAt, By: adminEmail, At: now})
		}
		return out
	}

	err := models.SetUserOverrides(r.Context(), target.ID, appID,
		toEntries(req.Grants), toEntries(req.Revokes), req.ExpectedUpdatedAt)
	if err != nil {
		if errors.Is(err, models.ErrOverridesConflict) {
			middleware.SendJSONError(w, r, apperrors.ErrEntitlementsConflict)
			return
		}
		middleware.GetLogger(r).Error("failed to set overrides", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrAdminCheckFailed)
		return
	}

	middleware.GetLogger(r).Info("admin replaced entitlement overrides",
		"app_id", appID, "grants", len(req.Grants), "revokes", len(req.Revokes))
	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]any{"updatedAt": now})
}

type adminCompRequest struct {
	TargetUserID string     `json:"targetUserId"`
	AppID        string     `json:"appId"`
	ValidTill    *time.Time `json:"validTill"` // nil = indefinite
}

// handleAdminComp serves PUT (grant) and DELETE (revoke) on
// /v1/admin/entitlements/comp — the admin complimentary-access mechanism
// (cutover escape hatch, §3.7). Manual decode: one path, two verbs. Both
// recompute-and-return the projection so the response reflects final state.
func handleAdminComp(w http.ResponseWriter, r *http.Request) {
	req, decodeErr := middleware.DecodeJSONBody[adminCompRequest](r.Body)
	if decodeErr != nil {
		middleware.SendJSONError(w, r, decodeErr)
		return
	}
	middleware.SanitizeStruct(&req)
	if req.AppID == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	target, r, appErr := resolveTargetUser(r, req.TargetUserID)
	if appErr != nil {
		middleware.SendJSONError(w, r, appErr)
		return
	}

	switch r.Method {
	case http.MethodPut:
		comp := models.CompGrant{
			GrantedBy: middleware.GetUserFromContext(r).Email,
			GrantedAt: time.Now().UTC(),
			ValidTill: req.ValidTill,
		}
		if err := models.SetUserComp(r.Context(), target.ID, req.AppID, comp); err != nil {
			middleware.GetLogger(r).Error("failed to set comp", "error", err)
			middleware.SendJSONError(w, r, apperrors.ErrAdminCheckFailed)
			return
		}
		middleware.GetLogger(r).Info("admin granted comp", "app_id", req.AppID, "valid_till", req.ValidTill)
	case http.MethodDelete:
		if err := models.RemoveUserComp(r.Context(), target.ID, req.AppID); err != nil {
			middleware.GetLogger(r).Error("failed to remove comp", "error", err)
			middleware.SendJSONError(w, r, apperrors.ErrAdminCheckFailed)
			return
		}
		middleware.GetLogger(r).Info("admin revoked comp", "app_id", req.AppID)
	default:
		middleware.SendJSONError(w, r, apperrors.ErrMethodNotAllowed)
		return
	}

	// Recompute + re-read so the caller sees the final projection (the comp
	// write itself never touches projection fields; the recompute is for
	// consistency with every other entitlement-mutating admin path).
	if err := models.RecomputeUserEntitlement(r.Context(), target.ID, req.AppID); err != nil {
		middleware.GetLogger(r).Error("recompute after comp write failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrAdminCheckFailed)
		return
	}
	found, fresh, err := models.FindUserByID(r.Context(), target.ID.Hex())
	if err != nil || !found {
		middleware.GetLogger(r).Error("re-read after comp write failed", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrAdminCheckFailed)
		return
	}

	now := time.Now().UTC()
	ent := fresh.Entitlements[req.AppID]
	resp := map[string]any{
		"appId": req.AppID,
		"comp":  ent.Comp.Active(now),
	}
	if ent.Comp != nil {
		resp["grantedBy"] = ent.Comp.GrantedBy
		resp["grantedAt"] = ent.Comp.GrantedAt
		if ent.Comp.ValidTill != nil {
			resp["validTill"] = ent.Comp.ValidTill
		}
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, resp)
}

// Test seam — the sweep is the panel twin of cmd/entitlementsmigrate (same
// convention as the seams in migrations.go).
var backfillEntitlementsSweep = models.BackfillEntitlements

type adminRecomputeRequest struct {
	TargetUserID string `json:"targetUserId"`
	AppID        string `json:"appId"`
	// Apply gates the no-target full sweep only (dry-run default, like every
	// other migration surface). Targeted recomputes ignore it — a single-user
	// projection rebuild is the repair, not a cutover.
	Apply bool `json:"apply"`
}

// HandleAdminRecompute serves POST /v1/admin/entitlements/recompute — the
// standing drift-repair tool. With a target: rebuild that user's projection.
// Without: sweep every user with subscriptions (drift-driven, same engine as
// the backfill migration). The sweep defaults to a dry run that reports the
// drifted users without rewriting; "apply": true performs the recompute.
func HandleAdminRecompute(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(adminRecomputeRequest)
	if !ok {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	if req.TargetUserID != "" {
		target, r, appErr := resolveTargetUser(r, req.TargetUserID)
		if appErr != nil {
			middleware.SendJSONError(w, r, appErr)
			return
		}
		appID := req.AppID
		if appID == "" {
			appID = models.AppIDIndexly
		}
		if err := models.RecomputeUserEntitlement(r.Context(), target.ID, appID); err != nil {
			middleware.GetLogger(r).Error("failed to recompute entitlement", "error", err)
			middleware.SendJSONError(w, r, apperrors.ErrAdminCheckFailed)
			return
		}
		middleware.GetLogger(r).Info("admin recomputed entitlement", "app_id", appID)
		middleware.SendJSONResponse(w, r, http.StatusOK, map[string]any{"recomputed": 1})
		return
	}

	dryRun := !req.Apply
	report, err := backfillEntitlementsSweep(r.Context(), dryRun)
	if err != nil {
		middleware.GetLogger(r).Error("failed to run entitlements sweep", "error", err, "dry_run", dryRun)
		middleware.SendJSONError(w, r, apperrors.ErrAdminCheckFailed)
		return
	}
	if !dryRun {
		for _, id := range report.DriftedUserIDs {
			middleware.GetLogger(r).Info("entitlement projection repaired", "user_id", id)
		}
	}
	middleware.GetLogger(r).Info("admin ran entitlements sweep", "dry_run", dryRun,
		"users_scanned", report.UsersScanned, "drifted", report.Drifted,
		"recomputed", report.Recomputed, "errors", len(report.Errors))
	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]any{
		"dryRun":         dryRun,
		"usersScanned":   report.UsersScanned,
		"missingAppId":   report.MissingAppID,
		"stamped":        report.Stamped,
		"drifted":        report.Drifted,
		"driftedUserIds": report.DriftedUserIDs,
		"recomputed":     report.Recomputed,
		"errors":         report.Errors,
	})
}
