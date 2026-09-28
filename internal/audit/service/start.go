package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/atharva-ng/crunch/internal/audit"
	"github.com/atharva-ng/crunch/internal/audit/core"
	"github.com/atharva-ng/crunch/internal/audit/targetcheck"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/util/log"
)

// StartTenantRun guards (in order: active run → free/trial one-per-website →
// paid weekly cadence), creates the run doc with the frozen spec snapshot,
// and dispatches the crawl. All guards are pure preconditions — the
// controller path stays resolve → StartTenantRun.
func (s *auditService) StartTenantRun(ctx context.Context, user *models.User, entity *models.WebEntity, targetURL string) (*models.AuditRun, error) {
	if targetURL == "" {
		targetURL = entity.WebsiteUrl
	}
	cleanURL, domain, err := targetcheck.Validate(ctx, targetURL)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", audit.ErrAuditInvalidTarget, err)
	}

	if found, _, err := models.FindActiveAuditRunForEntity(ctx, entity.ID); err != nil {
		return nil, fmt.Errorf("audit: check active run: %w", err)
	} else if found {
		return nil, audit.ErrAuditRunActive
	}

	email := strings.ToLower(strings.TrimSpace(user.Email))

	// Plan state from the entitlements projection: anything but a currently
	// valid, non-trialing subscription runs under the free rules
	// (decision 10: one audit per website per email).
	if isFreeOrTrial(user, time.Now().UTC()) {
		count, err := models.CountAuditRunsForEmailAndDomain(ctx, email, domain)
		if err != nil {
			return nil, fmt.Errorf("audit: free limit count: %w", err)
		}
		if count >= 1 {
			return nil, audit.ErrAuditFreeLimitReached
		}
	} else {
		cooldown := time.Duration(s.values.Gating.PaidCooldownDays) * 24 * time.Hour
		count, err := models.CountAuditRunsForEntitySince(ctx, entity.ID, time.Now().Add(-cooldown))
		if err != nil {
			return nil, fmt.Errorf("audit: cadence count: %w", err)
		}
		if count >= 1 {
			return nil, audit.ErrAuditCooldown
		}
	}

	userID := user.ID
	companyID := entity.CompanyID
	entityID := entity.ID
	run := &models.AuditRun{
		Kind:         string(core.RunKindTenant),
		Status:       models.AuditStatusCreated,
		WebEntityID:  &entityID,
		CompanyID:    &companyID,
		UserID:       &userID,
		Email:        &email,
		TargetURL:    cleanURL,
		TargetDomain: domain,
		PageCap:      s.values.Crawl.TenantPageCap,
		SpecSnapshot: s.activeSpec.Snapshot(s.registry.FindingsOnly()),
	}
	if err := models.CreateAuditRun(ctx, run); err != nil {
		return nil, err
	}

	if err := s.dispatchCrawl(ctx, run, user.ID.Hex()); err != nil {
		return nil, err
	}
	return run, nil
}

// StartLeadRun is the Clerk-gated free-audit entry (decision 16 as amended
// 2026-08-30: no anonymous runs). The email is the AUTHENTICATED account's —
// never caller-supplied — and the run carries the user id, so the report is
// account-bound and no poll token is minted. The cost caps are unchanged:
// per-email, per-IP (attempts count; redundant behind auth but harmless), the
// race-proof weekly domain ledger, and the global daily circuit breaker.
func (s *auditService) StartLeadRun(ctx context.Context, user *models.User, targetURL, clientIP string) (*models.AuditRun, error) {
	email := strings.ToLower(strings.TrimSpace(user.Email))
	if email == "" {
		// A just-created auth stub whose Clerk email fetch failed — the
		// webhook backfills it momentarily.
		return nil, fmt.Errorf("%w: account email not available yet", audit.ErrAuditInvalidInput)
	}

	cleanURL, domain, err := targetcheck.Validate(ctx, targetURL)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", audit.ErrAuditInvalidTarget, err)
	}

	now := time.Now().UTC()

	// Circuit breaker first — when capacity is gone, nothing else matters.
	if cap := s.values.Gating.LeadRunsPerDay; cap > 0 {
		count, err := models.CountLeadAuditRunsSince(ctx, now.Add(-24*time.Hour))
		if err != nil {
			return nil, fmt.Errorf("audit lead: global count: %w", err)
		}
		if count >= int64(cap) {
			return nil, audit.ErrAuditCapacity
		}
	}

	if cap := s.values.Gating.LeadRunsPerIPPerDay; cap > 0 && clientIP != "" {
		count, err := models.CountLeadAuditRunsForIPSince(ctx, clientIP, now.Add(-24*time.Hour))
		if err != nil {
			return nil, fmt.Errorf("audit lead: ip count: %w", err)
		}
		if count >= int64(cap) {
			return nil, audit.ErrAuditRateLimited
		}
	}

	count, err := models.CountAuditRunsForEmailAndDomain(ctx, email, domain)
	if err != nil {
		return nil, fmt.Errorf("audit lead: email+domain count: %w", err)
	}
	if count >= 1 {
		return nil, audit.ErrAuditFreeLimitReached
	}

	// The one cap where the count-then-insert race is worth closing: the
	// unique weekly domain claim (LLD §5.3).
	duplicate, err := models.ClaimAuditDomainWeek(ctx, models.AuditDomainLedgerKey(domain, now))
	if err != nil {
		return nil, fmt.Errorf("audit lead: domain claim: %w", err)
	}
	if duplicate {
		return nil, audit.ErrAuditDomainCooldown
	}

	userID := user.ID
	run := &models.AuditRun{
		Kind:         string(core.RunKindLead),
		Status:       models.AuditStatusCreated,
		UserID:       &userID,
		Email:        &email,
		ClientIP:     &clientIP,
		TargetURL:    cleanURL,
		TargetDomain: domain,
		PageCap:      s.values.Crawl.LeadPageCap,
		SpecSnapshot: s.activeSpec.Snapshot(s.registry.FindingsOnly()),
	}
	if err := models.CreateAuditRun(ctx, run); err != nil {
		return nil, err
	}

	if err := s.dispatchCrawl(ctx, run, userID.Hex()); err != nil {
		return nil, err
	}
	return run, nil
}

func (s *auditService) dispatchCrawl(ctx context.Context, run *models.AuditRun, userID string) error {
	if err := s.dispatcher.Dispatch(ctx, string(audit.ProcessAuditCrawl), userID, audit.AuditRunPayload{RunID: run.ID.Hex()}); err != nil {
		if markErr := models.SetAuditRunError(ctx, run.ID.Hex(), string(audit.ProcessAuditCrawl), "crawl dispatch failed", "", false); markErr != nil {
			log.Error("audit: mark run error after dispatch failure", "error", markErr, "runId", run.ID.Hex())
		}
		return fmt.Errorf("audit: dispatch crawl: %w", err)
	}
	return nil
}

// isFreeOrTrial reads the entitlements projection: a user without a
// currently valid subscription, or one whose subscription is a trial, runs
// under the free one-per-website rule.
func isFreeOrTrial(user *models.User, now time.Time) bool {
	ent, ok := user.Entitlements[models.AppIDIndexly]
	if !ok {
		return true
	}
	if !now.Before(ent.ValidTill) {
		return !ent.Comp.Active(now) // an active comp counts as paid
	}
	return ent.Status == models.SubStatusTrialing
}
