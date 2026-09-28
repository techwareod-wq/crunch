package middleware

import (
	"context"
	"net/http"

	"github.com/atharva-ng/crunch/internal/authz"
	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/util/log"
)

// ActiveCompany is what the active-company middleware resolves onto the
// request context (tenancy plan §4): the company, the caller's claimed
// membership in it, and the resolved company permission set (owner rule
// applied). Company routes read this and gate through
// RequireCompanyPermission — never through the global authz.Has (D17).
type ActiveCompany struct {
	Company    *models.Company
	Membership *models.CompanyMembership
	Perms      map[authz.Permission]bool
}

// CompanyRole is the caller's company role key (owner reads as user_admin —
// the D12 owner rule).
func (a *ActiveCompany) CompanyRole() string {
	if a == nil || a.Company == nil {
		return ""
	}
	if a.Membership != nil && a.Membership.UserID != nil && a.Company.OwnerUserID == *a.Membership.UserID {
		return models.CompanyRoleKeyAdmin
	}
	if a.Membership != nil {
		return a.Membership.Role
	}
	return ""
}

// WithActiveCompany resolves the caller's active company per request (D9 —
// DB-resolved, never a token claim): user.LastActiveCompanyID is validated
// against a live CLAIMED membership; a stale/absent/invalid pointer falls
// through to the personal company, NEVER a 403 (a removed member's stale
// pointer must degrade, not lock them out). Chain BEFORE
// .WithJWTAuthentication() so the user is already on the context:
//
//	middleware.Handle(path, handler).
//	    WithActiveCompany().          // runs second (inner)
//	    WithJWTAuthentication().      // runs first  (outer)
func (p pattern) WithActiveCompany() pattern {
	ro := routes[string(p)]
	routes[string(p)] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, ok := r.Context().Value(UserContextKey).(*models.User)
		if !ok {
			log.Error("active-company middleware ran without authenticated user — check middleware chain order",
				"path", r.URL.Path)
			SendJSONError(w, r, apperrors.ErrAdminCheckFailed)
			return
		}

		active, err := resolveActiveCompany(r.Context(), user)
		if err != nil {
			GetLogger(r).Error("active company resolution failed", "error", err, "user_id", user.ID.Hex())
			SendJSONError(w, r, apperrors.ErrAdminCheckFailed)
			return
		}

		ctx := context.WithValue(r.Context(), ActiveCompanyContextKey, active)
		ro.ServeHTTP(w, r.WithContext(ctx))
	})
	return p
}

// resolveActiveCompany implements the §4 dashboard rule. Split from the
// middleware for testability and for controllers that resolve lazily.
func resolveActiveCompany(ctx context.Context, user *models.User) (*ActiveCompany, error) {
	// 1. The stored pointer, validated against a live claimed membership.
	if user.LastActiveCompanyID != nil {
		found, m, err := models.FindClaimedMembership(ctx, user.ID, *user.LastActiveCompanyID)
		if err != nil {
			return nil, err
		}
		if found {
			cFound, company, err := models.FindCompanyByID(ctx, m.CompanyID)
			if err != nil {
				return nil, err
			}
			if cFound {
				return newActiveCompany(user, company, m), nil
			}
		}
		// Stale pointer — fall through to the personal company (never 403).
	}

	// 2. Personal-company fallback, self-healing: legacy users predating the
	// signup hook get their personal company minted right here (idempotent
	// via the D20 index).
	company, err := models.EnsurePersonalCompany(ctx, user.ID, user.Email, user.Name)
	if err != nil {
		return nil, err
	}
	found, m, err := models.FindClaimedMembership(ctx, user.ID, company.ID)
	if err != nil {
		return nil, err
	}
	if !found {
		// Owner without a claimed membership doc shouldn't happen (the mint
		// upserts it) — the owner rule still grants full perms.
		m = nil
	}
	return newActiveCompany(user, company, m), nil
}

func newActiveCompany(user *models.User, company *models.Company, m *models.CompanyMembership) *ActiveCompany {
	return &ActiveCompany{
		Company:    company,
		Membership: m,
		Perms:      authz.ResolveCompanyPermissions(user.ID, company, m, rolesCache),
	}
}

// GetActiveCompanyFromContext extracts the resolved active company. Panics if
// WithActiveCompany was not chained — intentional, like GetUserFromContext.
func GetActiveCompanyFromContext(r *http.Request) *ActiveCompany {
	return r.Context().Value(ActiveCompanyContextKey).(*ActiveCompany)
}

// RequireCompanyPermission is the per-handler company gate: true when the
// resolved set holds p. The company axis never consults the global resolver
// (D17), so a platform admin with no membership gets nothing here.
func RequireCompanyPermission(r *http.Request, p authz.Permission) bool {
	active, ok := r.Context().Value(ActiveCompanyContextKey).(*ActiveCompany)
	if !ok || active == nil {
		return false
	}
	return authz.CompanyHas(active.Perms, p)
}

// ResolveActiveCompanyForUser is the exported resolver for callers outside
// the middleware chain (e.g. the switcher endpoint validating a target, or
// service-layer code holding a *models.User). Reads live state.
func ResolveActiveCompanyForUser(ctx context.Context, user *models.User) (*ActiveCompany, error) {
	return resolveActiveCompany(ctx, user)
}
