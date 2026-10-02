package models

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
)

// User roles. The permission logic lives in internal/authz; models only needs
// the keys.
const (
	RoleUser      = "user"      // visitor; no admin panel
	RoleAdmin     = "admin"     // panel access; editor/approver/attributes via Permissions
	RoleSuperuser = "superuser" // everything; minted only by cmd/superuser
)

// SuperuserPromoteReport summarizes one PromoteSuperusers run.
type SuperuserPromoteReport struct {
	Promoted         []string // emails resolved to an active user and promoted (or would be)
	AlreadySuperuser []string // already role=superuser — no write
	Unmatched        []string // no active user doc yet (promote after they first sign in)
	Updated          int      // docs actually written (apply only)
}

// PromoteSuperusers sets role=superuser on the active users with these
// emails — the cmd/superuser bootstrap and lockout recovery. Idempotent.
// Bypasses the adminActions audit (it runs outside the API). Emails with no
// user doc yet are reported unmatched: the person signs in once, then re-run.
func PromoteSuperusers(ctx context.Context, emails []string, dryRun bool) (*SuperuserPromoteReport, error) {
	report := &SuperuserPromoteReport{}
	for _, email := range emails {
		found, user, err := FindUserByEmail(ctx, email)
		if err != nil {
			return report, fmt.Errorf("lookup %s: %w", email, err)
		}
		if !found {
			report.Unmatched = append(report.Unmatched, email)
			continue
		}
		if user.Role == RoleSuperuser {
			report.AlreadySuperuser = append(report.AlreadySuperuser, email)
			continue
		}
		report.Promoted = append(report.Promoted, email)
		if dryRun {
			continue
		}
		now := time.Now().UTC()
		if _, err := Collection(usersCollection).UpdateOne(ctx,
			activeFilter(bson.M{fieldID: user.ID}),
			bson.M{
				"$set":   bson.M{"role": RoleSuperuser, "role_updated_at": now, fieldUpdatedAt: now},
				"$unset": bson.M{"permissions": ""},
			},
		); err != nil {
			return report, fmt.Errorf("promote %s: %w", email, err)
		}
		report.Updated++
	}
	return report, nil
}
