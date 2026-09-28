package admin

import (
	"errors"
	"net/http"
	"regexp"
	"sort"
	"time"

	"github.com/atharva-ng/crunch/internal/authz"
	"github.com/atharva-ng/crunch/internal/config"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
)

// Seams for tests (mirror findUserByID in target.go / listAdminActions in
// console.go): the escalation guards are pure, but they sit between DB reads
// (role lookups, superuser count) and DB writes (role upsert/delete, user role
// assignment). Routing those through package vars lets the handler-level guard
// tests run against in-memory fixtures without a live Mongo.
var (
	findRoleByKey         = models.FindRoleByKey
	createRole            = models.CreateRole
	updateRole            = models.UpdateRole
	deleteRoleByKey       = models.DeleteRoleByKey
	countUsersWithRole    = models.CountUsersWithRole
	countActiveSuperusers = models.CountActiveSuperusers
	setUserRole           = models.SetUserRole
)

// roleKeyPattern constrains custom role/permission keys to a hygienic slug
// (guard K1). Keys are only ever equality VALUES in bson.M (never field names
// or operators), so this is cosmetic hardening, not an injection fix.
var roleKeyPattern = regexp.MustCompile(`^[a-z0-9_.-]+$`)

// reloadRolesCache write-through refreshes the cache after a catalog write. A
// failed reload is logged, not fatal: the doc is written and the background
// ticker retries within a minute (mirrors the plans-cache reload-after-write).
// A package var so the handler-guard tests can no-op the DB-backed reload.
var reloadRolesCache = func(r *http.Request, appCtx *config.AppContext) {
	if err := appCtx.RolesCache.Reload(r.Context()); err != nil {
		middleware.GetLogger(r).Error("roles cache reload after write failed", "error", err)
	}
}

// RBAC admin surface (plan §6): read the role catalog, upsert/delete custom
// roles, assign a user's role, and set a user's per-user grants/revokes. Every
// write path enforces the privilege-escalation guards (plan §7): grant only
// what you hold, no rank climbing, superuser-tier containment, immutable system
// roles, and last-superuser lockout protection.
//
// The route registry is path-only, so GET+POST share /v1/admin/roles under one
// gate tagged roles.read; the write branch re-checks roles.write in-handler.
// The dedicated POST paths (roles/delete, users/role, users/grants) are gated
// at roles.write directly.

// --- catalog: GET (list) + POST (upsert) on /v1/admin/roles ---

// HandleAdminRoles dispatches GET (list, roles.read) vs POST (upsert,
// roles.write) on the shared path. Manual decode: one path, two verbs (mirrors
// handleAdminPlans).
func HandleAdminRoles(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		handleAdminListRoles(w, r)
		return
	}
	// POST → upsert. The path-level gate only guaranteed roles.read; the write
	// requires roles.write (superuser). A 403 here is audited by the gate.
	if !middleware.CallerHasPermission(r, authz.PermRolesWrite) {
		middleware.SendJSONError(w, r, apperrors.ErrRoleEscalation)
		return
	}
	handleAdminUpsertRole(w, r)
}

func handleAdminListRoles(w http.ResponseWriter, r *http.Request) {
	roles, err := models.ListAllRoles(r.Context())
	if err != nil {
		middleware.GetLogger(r).Error("failed to list roles", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrAdminCheckFailed)
		return
	}
	if roles == nil {
		roles = []models.Role{}
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, roles)
}

type adminUpsertRoleRequest struct {
	Key               string     `json:"key"`
	Rank              int        `json:"rank"`
	Permissions       []string   `json:"permissions"`
	Description       string     `json:"description"`
	ExpectedUpdatedAt *time.Time `json:"expectedUpdatedAt"`
}

func handleAdminUpsertRole(w http.ResponseWriter, r *http.Request) {
	req, decodeErr := middleware.DecodeJSONBody[adminUpsertRoleRequest](r.Body)
	if decodeErr != nil {
		middleware.SendJSONError(w, r, decodeErr)
		return
	}
	// Key must be a non-empty hygienic slug (guard K1). Not an injection guard —
	// keys are equality values, never bson field names — just catches a
	// cosmetically odd key (stray "$"/"/"/whitespace) before it reaches the
	// catalog. The reserved system keys ("user"/"admin"/"superuser") all pass.
	if req.Key == "" || !roleKeyPattern.MatchString(req.Key) {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRoleDoc)
		return
	}
	// Every permission key must be a defined constant or the wildcard — an
	// unknown key resolves to nothing and silently 403s the surface it meant to
	// open.
	for _, p := range req.Permissions {
		if !authz.IsValidRolePermission(p) {
			middleware.SendJSONError(w, r, apperrors.ErrUnknownPermissionKey)
			return
		}
	}

	appCtx := config.GetAppContext(r)
	caller := middleware.GetUserFromContext(r)
	now := time.Now().UTC()
	callerPerms := authz.EffectivePermissions(caller, appCtx.RolesCache, now)
	callerRank := authz.Rank(caller, appCtx.RolesCache)

	found, existing, err := findRoleByKey(r.Context(), req.Key)
	if err != nil {
		middleware.GetLogger(r).Error("failed to load role for upsert", "error", err, "key", req.Key)
		middleware.SendJSONError(w, r, apperrors.ErrAdminCheckFailed)
		return
	}

	// System-role immutability (guard §7.5): `user` and `superuser` are frozen
	// against rank/permission edits (description stays editable); the reserved
	// keys can't be minted via the API either (rolesmigrate re-seeds them).
	if authz.IsImmutableSystemRole(req.Key) {
		if !found {
			middleware.SendJSONError(w, r, apperrors.ErrImmutableRole)
			return
		}
		if req.Rank != existing.Rank || !permsEqual(req.Permissions, existing.Permissions) {
			middleware.SendJSONError(w, r, apperrors.ErrImmutableRole)
			return
		}
		// Description-only edit (guard §7.5): the description stays editable even
		// on immutable roles, but never one ABOVE you. Strict ">" (not ">=") so a
		// top-rank caller can edit the top role's own description — the only role
		// at their rank — while a lower caller still can't touch a superior role.
		if existing.Rank > callerRank {
			middleware.SendJSONError(w, r, apperrors.ErrRoleEscalation)
			return
		}
		writeRole := &models.Role{Key: req.Key, Rank: existing.Rank, Permissions: existing.Permissions, Description: req.Description}
		if err := writeRoleUpdate(w, r, appCtx, writeRole, req.ExpectedUpdatedAt); err != nil {
			return
		}
		respondRole(w, r, appCtx, req.Key)
		return
	}

	// Privilege containment (guard §7.6): the superuser-tier set may live ONLY
	// on the immutable superuser role — never on an editable role.
	for _, p := range req.Permissions {
		if authz.IsSuperuserTier(p) {
			middleware.SendJSONError(w, r, apperrors.ErrRoleEscalation)
			return
		}
	}
	// No rank climbing (guard §7.2): the new rank must be STRICTLY below the
	// caller's, and you can't edit a role that already sits at/above you.
	if req.Rank >= callerRank || (found && existing.Rank >= callerRank) {
		middleware.SendJSONError(w, r, apperrors.ErrRoleEscalation)
		return
	}
	// Grant only what you hold (guard §7.1): the role's perms ⊆ the caller's.
	if !authz.IsSubset(authz.ExpandKeys(req.Permissions), callerPerms) {
		middleware.SendJSONError(w, r, apperrors.ErrRoleEscalation)
		return
	}

	if found {
		writeRole := &models.Role{Key: req.Key, Rank: req.Rank, Permissions: req.Permissions, Description: req.Description}
		if err := writeRoleUpdate(w, r, appCtx, writeRole, req.ExpectedUpdatedAt); err != nil {
			return
		}
	} else {
		writeRole := &models.Role{Key: req.Key, Rank: req.Rank, Permissions: req.Permissions, Description: req.Description, System: false}
		if err := createRole(r.Context(), writeRole); err != nil {
			if errors.Is(err, models.ErrRoleKeyExists) {
				middleware.SendJSONError(w, r, apperrors.ErrRolesConflict)
				return
			}
			middleware.GetLogger(r).Error("failed to create role", "error", err, "key", req.Key)
			middleware.SendJSONError(w, r, apperrors.ErrAdminCheckFailed)
			return
		}
		reloadRolesCache(r, appCtx)
	}
	middleware.GetLogger(r).Info("admin upserted role", "key", req.Key, "rank", req.Rank, "created", !found)
	respondRole(w, r, appCtx, req.Key)
}

// writeRoleUpdate applies a CAS update + cache reload, mapping the conflict
// error to a 409. Returns a non-nil error only to signal the caller to stop
// (the response has already been written).
func writeRoleUpdate(w http.ResponseWriter, r *http.Request, appCtx *config.AppContext, role *models.Role, expected *time.Time) error {
	if err := updateRole(r.Context(), role, expected); err != nil {
		if errors.Is(err, models.ErrRoleConflict) {
			middleware.SendJSONError(w, r, apperrors.ErrRolesConflict)
			return err
		}
		middleware.GetLogger(r).Error("failed to update role", "error", err, "key", role.Key)
		middleware.SendJSONError(w, r, apperrors.ErrAdminCheckFailed)
		return err
	}
	reloadRolesCache(r, appCtx)
	return nil
}

// respondRole re-reads and returns the role doc so the client gets the fresh
// updated_at for its next optimistic-concurrency write.
func respondRole(w http.ResponseWriter, r *http.Request, appCtx *config.AppContext, key string) {
	if role, ok := appCtx.RolesCache.ByKey(key); ok {
		middleware.SendJSONResponse(w, r, http.StatusOK, role)
		return
	}
	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]any{"ok": true, "key": key})
}

// --- delete: POST /v1/admin/roles/delete (roles.write) ---

type adminDeleteRoleRequest struct {
	Key string `json:"key"`
}

// HandleAdminDeleteRole deletes a non-system role, refusing while any active
// user still holds it (plan §6). System roles are undeletable.
func HandleAdminDeleteRole(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(adminDeleteRoleRequest)
	if !ok || req.Key == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	found, role, err := findRoleByKey(r.Context(), req.Key)
	if err != nil {
		middleware.GetLogger(r).Error("failed to load role for delete", "error", err, "key", req.Key)
		middleware.SendJSONError(w, r, apperrors.ErrAdminCheckFailed)
		return
	}
	if !found {
		middleware.SendJSONError(w, r, apperrors.ErrRoleNotFound)
		return
	}
	if role.System {
		middleware.SendJSONError(w, r, apperrors.ErrImmutableRole)
		return
	}

	count, err := countUsersWithRole(r.Context(), req.Key)
	if err != nil {
		middleware.GetLogger(r).Error("failed to count role holders", "error", err, "key", req.Key)
		middleware.SendJSONError(w, r, apperrors.ErrAdminCheckFailed)
		return
	}
	if count > 0 {
		middleware.SendJSONError(w, r, apperrors.ErrRoleInUse)
		return
	}

	if err := deleteRoleByKey(r.Context(), req.Key); err != nil {
		middleware.GetLogger(r).Error("failed to delete role", "error", err, "key", req.Key)
		middleware.SendJSONError(w, r, apperrors.ErrAdminCheckFailed)
		return
	}
	appCtx := config.GetAppContext(r)
	reloadRolesCache(r, appCtx)
	middleware.GetLogger(r).Info("admin deleted role", "key", req.Key)
	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]any{"ok": true, "key": req.Key})
}

// --- assignment: POST /v1/admin/users/role (roles.write) ---

type adminSetUserRoleRequest struct {
	TargetUserID          string     `json:"targetUserId"`
	Role                  string     `json:"role"`
	ExpectedRoleUpdatedAt *time.Time `json:"expectedRoleUpdatedAt"`
}

// HandleAdminSetUserRole assigns a user's role under the escalation guards
// (plan §7): the target role must exist, sit strictly below the caller's rank
// (so superuser can never be minted via the API), and confer only permissions
// the caller holds; demoting the last superuser is refused.
func HandleAdminSetUserRole(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(adminSetUserRoleRequest)
	if !ok || req.Role == "" {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}

	target, r, appErr := resolveTargetUser(r, req.TargetUserID)
	if appErr != nil {
		middleware.SendJSONError(w, r, appErr)
		return
	}

	appCtx := config.GetAppContext(r)
	now := time.Now().UTC()
	caller := middleware.GetUserFromContext(r)
	callerPerms := authz.EffectivePermissions(caller, appCtx.RolesCache, now)
	callerRank := authz.Rank(caller, appCtx.RolesCache)

	newRole, ok := appCtx.RolesCache.ByKey(req.Role)
	if !ok {
		middleware.SendJSONError(w, r, apperrors.ErrRoleNotFound)
		return
	}

	// Guard §7.2 (strict): can't assign a role at/above your own rank. Blocks
	// minting a peer/superior — additional superusers come from rolesmigrate.
	if newRole.Rank >= callerRank {
		middleware.SendJSONError(w, r, apperrors.ErrRoleEscalation)
		return
	}
	// Guard §7.1: grant only what you hold.
	if !authz.IsSubset(authz.ExpandRolePermissions(newRole), callerPerms) {
		middleware.SendJSONError(w, r, apperrors.ErrRoleEscalation)
		return
	}
	// Guard §7.3: last-superuser lockout protection on demotion.
	if target.Role == models.RoleKeySuperuser && req.Role != models.RoleKeySuperuser {
		count, err := countActiveSuperusers(r.Context())
		if err != nil {
			middleware.GetLogger(r).Error("failed to count superusers", "error", err)
			middleware.SendJSONError(w, r, apperrors.ErrAdminCheckFailed)
			return
		}
		if count <= 1 {
			middleware.SendJSONError(w, r, apperrors.ErrLastSuperuser)
			return
		}
	}

	if err := setUserRole(r.Context(), target.ID, req.Role, req.ExpectedRoleUpdatedAt); err != nil {
		if errors.Is(err, models.ErrRoleConflictOnUser) {
			middleware.SendJSONError(w, r, apperrors.ErrRolesConflict)
			return
		}
		middleware.GetLogger(r).Error("failed to set user role", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrAdminCheckFailed)
		return
	}
	middleware.GetLogger(r).Info("admin set user role", "role", req.Role)
	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]any{"role": req.Role})
}

// --- grants: POST /v1/admin/users/grants (roles.write) ---

type adminPermissionEntryDTO struct {
	Key       string     `json:"key"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	By        string     `json:"by,omitempty"`
	At        *time.Time `json:"at,omitempty"`
}

type adminSetUserGrantsRequest struct {
	TargetUserID          string                    `json:"targetUserId"`
	Grants                []adminPermissionEntryDTO `json:"grants"`
	Revokes               []adminPermissionEntryDTO `json:"revokes"`
	ExpectedRoleUpdatedAt *time.Time                `json:"expectedRoleUpdatedAt"`
}

// HandleAdminSetUserGrants replaces a user's extra_grants / extra_revokes.
// Containment (plan §7.6): panel entry is role-derived ONLY, so neither set may
// touch admin.access — grants can't confer it and revokes can't strip it — and
// neither may carry a superuser-tier key. Both sets reject unknown keys.
// Grant-only-what-you-hold (§7.1) applies to the granted set.
func HandleAdminSetUserGrants(w http.ResponseWriter, r *http.Request) {
	req, ok := r.Context().Value(middleware.DeserializerContextKey).(adminSetUserGrantsRequest)
	if !ok {
		middleware.SendJSONError(w, r, apperrors.ErrInvalidRequestBody)
		return
	}
	if appErr := validateGrantEntries(req.Grants); appErr != nil {
		middleware.SendJSONError(w, r, appErr)
		return
	}
	if appErr := validateRevokeEntries(req.Revokes); appErr != nil {
		middleware.SendJSONError(w, r, appErr)
		return
	}

	target, r, appErr := resolveTargetUser(r, req.TargetUserID)
	if appErr != nil {
		middleware.SendJSONError(w, r, appErr)
		return
	}

	appCtx := config.GetAppContext(r)
	now := time.Now().UTC()
	caller := middleware.GetUserFromContext(r)
	callerPerms := authz.EffectivePermissions(caller, appCtx.RolesCache, now)

	grantedSet := map[authz.Permission]bool{}
	for _, e := range req.Grants {
		grantedSet[authz.Permission(e.Key)] = true
	}
	if !authz.IsSubset(grantedSet, callerPerms) {
		middleware.SendJSONError(w, r, apperrors.ErrRoleEscalation)
		return
	}

	adminEmail := caller.Email
	toEntries := func(dtos []adminPermissionEntryDTO) []models.OverrideEntry {
		out := make([]models.OverrideEntry, 0, len(dtos))
		for _, d := range dtos {
			out = append(out, models.OverrideEntry{Key: d.Key, ExpiresAt: d.ExpiresAt, By: adminEmail, At: now})
		}
		return out
	}

	if err := models.SetUserGrants(r.Context(), target.ID, toEntries(req.Grants), toEntries(req.Revokes), req.ExpectedRoleUpdatedAt); err != nil {
		if errors.Is(err, models.ErrRoleConflictOnUser) {
			middleware.SendJSONError(w, r, apperrors.ErrRolesConflict)
			return
		}
		middleware.GetLogger(r).Error("failed to set user grants", "error", err)
		middleware.SendJSONError(w, r, apperrors.ErrAdminCheckFailed)
		return
	}
	middleware.GetLogger(r).Info("admin set user grants", "grants", len(req.Grants), "revokes", len(req.Revokes))
	middleware.SendJSONResponse(w, r, http.StatusOK, map[string]any{"grants": len(req.Grants), "revokes": len(req.Revokes)})
}

// validateGrantEntries rejects unknown keys (400) and — as an escalation guard
// (403) — superuser-tier keys and admin.access: a grant must never make someone
// staff or hand them superuser powers.
func validateGrantEntries(entries []adminPermissionEntryDTO) *apperrors.Error {
	for _, e := range entries {
		if !authz.IsKnownPermission(e.Key) {
			return apperrors.ErrUnknownPermissionKey
		}
		if authz.IsSuperuserTier(e.Key) || e.Key == string(authz.PermAdminAccess) {
			return apperrors.ErrRoleEscalation
		}
	}
	return nil
}

// validateRevokeEntries rejects unknown keys (400) and — as an escalation guard
// (403), symmetric with validateGrantEntries — superuser-tier keys and
// admin.access. Superuser-tier: a revoke would strip a superuser's core powers,
// a lockout the role-count guard (which keys off role==superuser, not resolved
// perms) can't see. admin.access: revoking the universal baseline API-locks the
// target out of the entire admin surface while they still count as an active
// superuser — an availability foot-gun. Panel entry is role-derived only, so a
// revoke never needs to touch it.
func validateRevokeEntries(entries []adminPermissionEntryDTO) *apperrors.Error {
	for _, e := range entries {
		if !authz.IsKnownPermission(e.Key) {
			return apperrors.ErrUnknownPermissionKey
		}
		if authz.IsSuperuserTier(e.Key) || e.Key == string(authz.PermAdminAccess) {
			return apperrors.ErrRoleEscalation
		}
	}
	return nil
}

// permsEqual compares two permission lists as sets (order-insensitive) — used
// to detect whether an immutable-role edit changed the permission set.
func permsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as := append([]string{}, a...)
	bs := append([]string{}, b...)
	sort.Strings(as)
	sort.Strings(bs)
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}
