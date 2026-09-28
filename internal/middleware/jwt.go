package middleware

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	clerkjwt "github.com/clerk/clerk-sdk-go/v2/jwt"
	clerkuser "github.com/clerk/clerk-sdk-go/v2/user"
	"go.mongodb.org/mongo-driver/mongo"

	apperrors "github.com/atharva-ng/crunch/internal/errors"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/util/log"
)

const bearerPrefixLen = 7 // length of "Bearer "

func (p pattern) WithJWTAuthentication() pattern {
	decorate := authenticateWithClerk()
	ro := routes[string(p)]
	h := decorate(ro)
	routes[string(p)] = h
	return p
}

func authenticateWithClerk() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, err := doClerkAuthentication(r)
			if err != nil {
				log.Error("clerk authentication failed", "error", err, "path", r.URL.Path)
				SendJSONError(w, r, apperrors.ErrInvalidOrExpiredToken)
				return
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func doClerkAuthentication(r *http.Request) (context.Context, error) {
	ah := r.Header.Get(HeaderAuthorization)
	if ah == "" {
		return nil, fmt.Errorf("missing authorization header")
	}

	if len(ah) <= bearerPrefixLen || !strings.EqualFold(ah[0:bearerPrefixLen], bearerPrefix) {
		return nil, fmt.Errorf("invalid authorization format")
	}

	tokenStr := ah[bearerPrefixLen:]

	claims, err := clerkjwt.Verify(r.Context(), &clerkjwt.VerifyParams{
		Token: tokenStr,
	})
	if err != nil {
		return nil, fmt.Errorf("clerk token verification failed: %w", err)
	}

	// claims.Subject is the Clerk user ID (e.g. "user_2abc...")
	clerkUserID := claims.Subject
	if clerkUserID == "" {
		return nil, fmt.Errorf("missing subject in clerk token")
	}

	user, err := resolveUserByClerkID(r.Context(), clerkUserID)
	if err != nil {
		return nil, fmt.Errorf("user resolution failed: %w", err)
	}

	ctx := context.WithValue(r.Context(), UserContextKey, user)
	ctx = context.WithValue(ctx, UserEmailKey, user.Email)
	ctx = context.WithValue(ctx, AuthTypeKey, JWTAuthIdentifier)

	return ctx, nil
}

// resolveUserByClerkID finds the user by their Clerk ID. If not found, it creates
// a stub record that will be fully populated by the Clerk webhook.
//
// Tombstone-aware: a row whose deactivated_at is set is treated as a hard 401.
// Without this guard, a still-valid Clerk session JWT presented after a
// user.deleted webhook would silently resurrect the account.
func resolveUserByClerkID(ctx context.Context, clerkID string) (*models.User, error) {
	found, user, err := models.FindUserByClerkIDIncludingDeactivated(ctx, clerkID)
	if err != nil {
		return nil, err
	}
	if found && user.DeactivatedAt != nil {
		return nil, fmt.Errorf("user is deactivated: clerk_id=%s", clerkID)
	}
	if found {
		return user, nil
	}

	// First-time user — create a minimal record. The webhook will fill in details.
	newUser := &models.User{
		ClerkID: clerkID,
		Role:    models.RoleKeyUser,
	}

	clerkUsr, err := clerkuser.Get(ctx, clerkID)
	if err != nil {
		log.Warn("clerk user fetch failed during stub creation — email backfilled by webhook", "clerk_id", clerkID, "error", err)
	}
	if err == nil && clerkUsr != nil {
		if len(clerkUsr.EmailAddresses) > 0 {
			for _, ea := range clerkUsr.EmailAddresses {
				if clerkUsr.PrimaryEmailAddressID != nil && ea.ID == *clerkUsr.PrimaryEmailAddressID {
					newUser.Email = ea.EmailAddress
					break
				}
			}
			// No primary marker (or none matched) — fall back to the first
			// address rather than minting an email-less user (mirrors the
			// Clerk webhook handler's extractor).
			if newUser.Email == "" {
				newUser.Email = clerkUsr.EmailAddresses[0].EmailAddress
			}
		}
		if clerkUsr.FirstName != nil {
			newUser.Name = *clerkUsr.FirstName
		}
		if clerkUsr.LastName != nil && *clerkUsr.LastName != "" {
			if newUser.Name != "" {
				newUser.Name += " "
			}
			newUser.Name += *clerkUsr.LastName
		}
	}

	if err := models.CreateUser(ctx, newUser); err != nil {
		if !mongo.IsDuplicateKeyError(err) {
			return nil, fmt.Errorf("failed to create user record: %w", err)
		}

		// Race lost: the Clerk webhook (or a concurrent JWT request) inserted
		// first. Re-read and return the winner's record.
		found, existing, rerr := models.FindUserByClerkIDIncludingDeactivated(ctx, clerkID)
		if rerr != nil {
			return nil, fmt.Errorf("re-read after dup key: %w", rerr)
		}
		if !found {
			return nil, fmt.Errorf("user missing after dup key for clerk_id %s", clerkID)
		}
		if existing.DeactivatedAt != nil {
			return nil, fmt.Errorf("user is deactivated: clerk_id=%s", clerkID)
		}
		return existing, nil
	}

	return newUser, nil
}

// GetUserFromContext extracts the authenticated user from the request context.
// Panics if the auth middleware was not applied — this is intentional to catch
// misconfiguration immediately.
func GetUserFromContext(r *http.Request) *models.User {
	return r.Context().Value(UserContextKey).(*models.User)
}

func GetUserEmailFromContext(r *http.Request) string {
	email, _ := r.Context().Value(UserEmailKey).(string)
	return email
}
