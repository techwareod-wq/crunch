package accountService

import (
	"context"

	"go.mongodb.org/mongo-driver/bson/primitive"

	"github.com/atharva-ng/crunch/internal/models"
)

// DeletionReport is what a deletion actually did — returned to the admin
// endpoint and logged. DataDeleted is keyed by DataCleaner.Name(); a re-run of
// an already-completed cascade reports zeros everywhere.
type DeletionReport struct {
	DataDeleted      map[string]int64 `json:"dataDeleted,omitempty"`
	ClerkUserDeleted bool             `json:"clerkUserDeleted"`
	UserScrubbed     bool             `json:"userScrubbed"`
}

// DataCleaner is one feature's slice of the account-deletion cascade: it
// hard-deletes everything that feature stores for the user and returns how
// many documents it removed. Cleaners MUST be zero-match-OK (a re-run after a
// partial failure converges) — there are no Mongo transactions to lean on.
// Feature modules contribute cleaners at boot (see internal/modules).
type DataCleaner interface {
	Name() string
	DeleteUserData(ctx context.Context, userID primitive.ObjectID) (int64, error)
}

// AccountService owns the admin-initiated destructive cascade. No Mongo
// transactions exist in this codebase, so the operation is ordered (feature
// data first, abortable external calls next, the user doc last) and every step
// is zero-match-OK — a mid-failure is repaired by simply re-running.
type AccountService interface {
	// DeleteUserAccount runs every registered DataCleaner, deletes the user at
	// Clerk (404 = already gone), and finally soft-deletes the user doc with
	// PII scrubbed. adminActions are deliberately kept (audit trail).
	DeleteUserAccount(ctx context.Context, user *models.User, adminEmail string) (*DeletionReport, error)
}
