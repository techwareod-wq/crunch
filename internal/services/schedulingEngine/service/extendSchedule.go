package service

import (
	"context"
	"fmt"
	"time"

	"github.com/atharva-ng/crunch/internal/models"
	se "github.com/atharva-ng/crunch/internal/services/schedulingEngine"
	"github.com/atharva-ng/crunch/internal/services/schedulingEngine/utils"
	"github.com/atharva-ng/crunch/internal/util/log"
)

// ExtendSchedule is the SE_EXTEND_SCHEDULE handler — the final stage of the
// trial→paid upgrade re-run (dispatched by SIE's post-clustering hand-off in
// place of SE Orchestrate when upgrade_state=="expanding"; the WEC arrives at
// SIEStatusClusteringDone). It sets the WebEntity's cadence to
// scheduling.upgradeCadence, appends slots until the calendar holds
// scheduling.upgradeCadence × scheduling.weeks rows, fans out article-type
// generation for the NEW rows only, and finalizes
// the WEC upgrade (upgrade_state="complete"); the WEC then returns to
// SIEStatusSchedulingDone via markSchedulingDoneIfComplete once every row is
// titled.
//
// The existing trial rows are never touched — this is an append, which is
// exactly why it isn't Orchestrate (whose count>0 guard makes a second
// schedule a no-op). Idempotent: a redelivery recounts and fills only the
// remainder; the not-yet-scheduled candidate filter keeps auto-scheduling from
// double-booking a keyword (manual reuse of a used keyword is allowed, but
// this bulk path never reuses).
func (s *schedulingEngineService) ExtendSchedule(ctx context.Context, userId string, payload se.SEOrchestratePayload) error {
	ok, wec, err := s.store.GetWebEntityContext(ctx, payload.WebEntityContextID)
	if err != nil {
		return fmt.Errorf("extend schedule: get web entity context: %w", err)
	}
	if !ok {
		return fmt.Errorf("extend schedule: web entity context not found: %s", payload.WebEntityContextID)
	}
	if wec.UpgradeState == models.WECUpgradeStateComplete {
		return nil
	}

	ok, we, err := s.store.GetWebEntityByID(ctx, payload.WebEntityID)
	if err != nil {
		return fmt.Errorf("extend schedule: get web entity: %w", err)
	}
	if !ok {
		return fmt.Errorf("extend schedule: web entity not found: %s", payload.WebEntityID)
	}

	// Locked upgrade cadence — Pro publishes at the full rate from here on.
	if err := s.store.SetWebEntityPublishingCadence(ctx, payload.WebEntityID, s.values.UpgradeCadence); err != nil {
		return fmt.Errorf("extend schedule: set upgrade cadence: %w", err)
	}

	existing, err := s.store.GetScheduledArticlesByWebEntityContext(ctx, payload.WebEntityContextID)
	if err != nil {
		return fmt.Errorf("extend schedule: load existing rows: %w", err)
	}

	target, err := utils.SlotsForWindow(s.values.UpgradeCadence, s.values.Weeks)
	if err != nil {
		return fmt.Errorf("extend schedule: compute target slots: %w", err)
	}
	need := target - len(existing)
	if need <= 0 {
		return s.finalizeUpgradeAndRestoreStatus(ctx, payload.WebEntityContextID)
	}

	// Candidates = highest-OS keywords without a calendar row yet.
	candidates, keywordByID, err := s.unscheduledCandidates(ctx, payload.WebEntityContextID, existing)
	if err != nil {
		return fmt.Errorf("extend schedule: %w", err)
	}
	if len(candidates) == 0 {
		log.Warn("extend schedule: no unscheduled keywords to append — finalizing as-is",
			"webEntityContextId", payload.WebEntityContextID)
		return s.finalizeUpgradeAndRestoreStatus(ctx, payload.WebEntityContextID)
	}

	slots, err := utils.GenerateSlots(s.values.UpgradeCadence, time.Now().UTC(), need)
	if err != nil {
		return fmt.Errorf("extend schedule: generate slots: %w", err)
	}

	// The cluster/day constraint is seeded from the untouched trial rows so an
	// appended slot landing on an existing publish day still respects them.
	assignments := assignKeywordsToSlotsSeeded(candidates, slots, seedUsedClusters(existing, keywordByID))
	if len(assignments) == 0 {
		return fmt.Errorf("extend schedule: produced zero assignments")
	}

	docs, err := s.persistAndFanOut(ctx, userId, payload, we, assignments, len(existing)+len(assignments))
	if err != nil {
		return fmt.Errorf("extend schedule: %w", err)
	}

	log.Info("upgrade schedule extended", "webEntityContextId", payload.WebEntityContextID,
		"appended", len(docs), "total", len(existing)+len(docs), "cadence", s.values.UpgradeCadence)

	return s.finalizeUpgradeAndRestoreStatus(ctx, payload.WebEntityContextID)
}

// finalizeUpgradeAndRestoreStatus marks the upgrade complete and immediately
// re-checks scheduling completion. During the re-run markSchedulingDoneIfComplete
// deliberately no-ops (upgrade_state=="expanding"), so without this post-finalize
// check a WEC whose appended rows all titled before the finalize — or whose
// extend appended nothing (need<=0 / no candidates, every existing row already
// titled) — would never return to SIEStatusSchedulingDone and the CGE gate
// would stay shut.
func (s *schedulingEngineService) finalizeUpgradeAndRestoreStatus(ctx context.Context, webEntityContextID string) error {
	if err := s.store.FinalizeWECUpgrade(ctx, webEntityContextID); err != nil {
		return err
	}
	return s.markSchedulingDoneIfComplete(ctx, webEntityContextID)
}
