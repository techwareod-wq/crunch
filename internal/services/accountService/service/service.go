package service

import (
	"context"
	"fmt"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	"github.com/atharva-ng/crunch/internal/services/accountService"
	accountstore "github.com/atharva-ng/crunch/internal/services/accountService/store"
	"github.com/atharva-ng/crunch/internal/util/log"
)

type service struct {
	store    accountstore.Store
	clerk    interfaces.ClerkAccounts
	cleaners []accountService.DataCleaner
}

func NewService(
	st accountstore.Store,
	clerk interfaces.ClerkAccounts,
	cleaners []accountService.DataCleaner,
) accountService.AccountService {
	return &service{store: st, clerk: clerk, cleaners: cleaners}
}

// DeleteUserAccount runs the cascade. Step order is the safety argument (no
// transactions exist — ordering and idempotency are the only tools):
//
//  1. feature data via each DataCleaner, in registration order — a failure
//     aborts with the user doc (the re-run anchor) intact
//  2. Clerk delete — external and abortable: a failure leaves the user doc
//     intact and the endpoint cleanly retryable
//  3. scrub the user doc LAST
//
// Once Clerk succeeds its user.deleted webhook may fire concurrently — the
// webhook handler's tombstone is an idempotent no-op against this cascade, and
// the scrub's writes absorb either ordering.
func (s *service) DeleteUserAccount(ctx context.Context, user *models.User, adminEmail string) (*accountService.DeletionReport, error) {
	report := &accountService.DeletionReport{}

	for _, c := range s.cleaners {
		n, err := c.DeleteUserData(ctx, user.ID)
		if err != nil {
			return nil, fmt.Errorf("delete %s data: %w", c.Name(), err)
		}
		if report.DataDeleted == nil {
			report.DataDeleted = map[string]int64{}
		}
		report.DataDeleted[c.Name()] = n
	}

	if user.ClerkID != "" {
		if err := s.clerk.DeleteUser(ctx, user.ClerkID); err != nil {
			return nil, err
		}
	}
	report.ClerkUserDeleted = true

	if err := s.store.DeactivateAndScrubUserByID(ctx, user.ID); err != nil {
		return nil, fmt.Errorf("scrub user: %w", err)
	}
	report.UserScrubbed = true

	log.Info("user account deleted",
		"user_id", user.ID.Hex(), "admin", adminEmail,
		"clerk_user_deleted", report.ClerkUserDeleted,
		"data_deleted", report.DataDeleted)
	return report, nil
}
