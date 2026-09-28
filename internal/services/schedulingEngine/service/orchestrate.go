package service

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/atharva-ng/crunch/internal/models"
	se "github.com/atharva-ng/crunch/internal/services/schedulingEngine"
	"github.com/atharva-ng/crunch/internal/services/schedulingEngine/utils"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Orchestrate performs steps 1+2 of the scheduling flow:
//  1. select the top N processed keywords from the WebEntityContext, ranked
//     by OpportunityScore, where N is dictated by the WebEntity's cadence
//     (articles_per_week × values.scheduling.weeks);
//  2. assign each keyword to a publish date — starting from today, snapping
//     forward to the next valid publish weekday, respecting the rule that
//     two keywords from the same cluster must not land on the same day.
//
// The resulting ScheduledArticle docs are persisted in one batch insert. Each
// doc is then fanned-out to the article-type generation step. Idempotent:
// a second call for the same WebEntityContext is a no-op.
func (s *schedulingEngineService) Orchestrate(ctx context.Context, userId string, payload se.SEOrchestratePayload) error {
	count, err := s.store.CountScheduledArticlesForContext(ctx, payload.WebEntityContextID)
	if err != nil {
		return fmt.Errorf("count existing schedules: %w", err)
	}
	if count > 0 {
		// Already scheduled for this context — treat as a successful no-op so
		// pipeline retries don't double-schedule.
		return nil
	}

	ok, wec, err := s.store.GetWebEntityContext(ctx, payload.WebEntityContextID)
	if err != nil {
		return fmt.Errorf("get web entity context: %w", err)
	}
	if !ok {
		return fmt.Errorf("web entity context not found: %s", payload.WebEntityContextID)
	}
	if wec.Status < models.SIEStatusClusteringDone {
		return fmt.Errorf("scheduling requires clustering done (status %d), got %d", models.SIEStatusClusteringDone, wec.Status)
	}

	ok, we, err := s.store.GetWebEntityByID(ctx, payload.WebEntityID)
	if err != nil {
		return fmt.Errorf("get web entity: %w", err)
	}
	if !ok {
		return fmt.Errorf("web entity not found: %s", payload.WebEntityID)
	}

	// Trial-mode WECs get the fixed trial calendar regardless of the chosen
	// publishing cadence (which resolveCadence would require to be set):
	// trial.cadence articles/week for trial.scheduleWeeks week(s). The upgrade
	// expand pipeline (SE_EXTEND_SCHEDULE) later appends the full calendar.
	var cadence, weeks int
	if wec.EffectiveSIEMode() == models.SIEModeTrial {
		cadence = s.trial.Cadence
		weeks = s.trial.ScheduleWeeks
	} else {
		cadence, err = resolveCadence(we)
		if err != nil {
			return err
		}
		weeks = s.values.Weeks
	}

	totalSlots, err := utils.SlotsForWindow(cadence, weeks)
	if err != nil {
		return fmt.Errorf("compute window slots: %w", err)
	}

	processed, err := s.store.GetKeywordsForWEC(ctx, payload.WebEntityContextID)
	if err != nil {
		return fmt.Errorf("load processed keywords: %w", err)
	}
	candidates := sortByOpportunityScore(processed)
	if len(candidates) == 0 {
		return fmt.Errorf("no processed keywords available for scheduling")
	}

	slots, err := utils.GenerateSlots(cadence, time.Now().UTC(), totalSlots)
	if err != nil {
		return fmt.Errorf("generate slots: %w", err)
	}

	assignments := assignKeywordsToSlots(candidates, slots)
	if len(assignments) == 0 {
		return fmt.Errorf("scheduling produced zero assignments")
	}

	if _, err := s.persistAndFanOut(ctx, userId, payload, we, assignments, len(assignments)); err != nil {
		return err
	}
	return nil
}

// resolveCadence pulls articles_per_week off the WebEntity's PublishingConfig.
// Returns an error rather than silently defaulting — onboarding is supposed
// to set this before SIE runs.
func resolveCadence(we *models.WebEntity) (int, error) {
	if we.Publishing == nil || we.Publishing.ArticlesPerWeek == 0 {
		return 0, fmt.Errorf("web entity %s has no publishing cadence configured", we.ID.Hex())
	}
	return we.Publishing.ArticlesPerWeek, nil
}

// sortByOpportunityScore returns a copy of the input sorted descending by
// OpportunityScore. Uses a copy so the caller's slice is not mutated.
func sortByOpportunityScore(in []models.Keyword) []models.Keyword {
	out := make([]models.Keyword, len(in))
	copy(out, in)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].OpportunityScore > out[j].OpportunityScore
	})
	return out
}

type assignment struct {
	keyword models.Keyword
	date    time.Time
}

// assignKeywordsToSlots walks the publish slots in chronological order. For
// each slot it picks the highest-OpportunityScore unassigned keyword whose
// cluster is not yet represented on that calendar date — satisfying the
// "no two keywords from the same cluster on a single day" rule.
//
// If no candidate satisfies the cluster constraint (e.g. a single cluster
// dominates the top of the list and we're filling a 2-per-day cadence), the
// algorithm degrades by taking the next available keyword regardless of
// cluster — better to ship a less-balanced schedule than to drop a slot.
//
// Returns at most min(len(candidates), len(slots)) assignments.
func assignKeywordsToSlots(candidates []models.Keyword, slots []time.Time) []assignment {
	return assignKeywordsToSlotsSeeded(candidates, slots, nil)
}

// assignKeywordsToSlotsSeeded is assignKeywordsToSlots with a pre-seeded
// cluster/day map — the schedule-extension path seeds it from the rows that
// already exist so appended slots respect the constraint against them.
func assignKeywordsToSlotsSeeded(candidates []models.Keyword, slots []time.Time, usedClustersByDate map[time.Time]map[string]bool) []assignment {
	pool := make([]models.Keyword, len(candidates))
	copy(pool, candidates)

	if usedClustersByDate == nil {
		usedClustersByDate = make(map[time.Time]map[string]bool)
	}
	out := make([]assignment, 0, len(slots))

	for _, slot := range slots {
		if len(pool) == 0 {
			break
		}

		used, ok := usedClustersByDate[slot]
		if !ok {
			used = make(map[string]bool)
			usedClustersByDate[slot] = used
		}

		pickIdx := -1
		for i, kw := range pool {
			if kw.Cluster == "" || !used[kw.Cluster] {
				pickIdx = i
				break
			}
		}
		if pickIdx == -1 {
			// Cluster constraint cannot be satisfied — fall back to highest-OS.
			pickIdx = 0
		}

		chosen := pool[pickIdx]
		pool = append(pool[:pickIdx], pool[pickIdx+1:]...)
		if chosen.Cluster != "" {
			used[chosen.Cluster] = true
		}
		out = append(out, assignment{keyword: chosen, date: slot})
	}
	return out
}

// unscheduledCandidates returns the WEC's keywords that have no calendar row
// yet, sorted descending by OpportunityScore, plus a keyword-by-ID lookup over
// the full keyword set (seedUsedClusters needs the scheduled ones too). Shared
// by the two append paths (ExtendSchedule, RerunSchedule).
func (s *schedulingEngineService) unscheduledCandidates(ctx context.Context, webEntityContextID string, existing []*models.ScheduledArticle) ([]models.Keyword, map[primitive.ObjectID]models.Keyword, error) {
	keywords, err := s.store.GetKeywordsForWEC(ctx, webEntityContextID)
	if err != nil {
		return nil, nil, fmt.Errorf("load keywords: %w", err)
	}
	keywordByID := make(map[primitive.ObjectID]models.Keyword, len(keywords))
	for _, kw := range keywords {
		keywordByID[kw.ID] = kw
	}

	alreadyScheduled := make(map[primitive.ObjectID]bool, len(existing))
	for _, sa := range existing {
		alreadyScheduled[sa.KeywordID] = true
	}
	unscheduled := make([]models.Keyword, 0, len(keywords))
	for _, kw := range keywords {
		if !alreadyScheduled[kw.ID] {
			unscheduled = append(unscheduled, kw)
		}
	}
	return sortByOpportunityScore(unscheduled), keywordByID, nil
}

// seedUsedClusters builds the cluster/day constraint map from the existing
// calendar rows so an appended slot landing on an already-populated day still
// respects the "no two keywords from one cluster on the same day" rule.
func seedUsedClusters(existing []*models.ScheduledArticle, keywordByID map[primitive.ObjectID]models.Keyword) map[time.Time]map[string]bool {
	used := make(map[time.Time]map[string]bool)
	for _, sa := range existing {
		kw, found := keywordByID[sa.KeywordID]
		if !found || kw.Cluster == "" {
			continue
		}
		day := sa.ScheduleDate
		if used[day] == nil {
			used[day] = make(map[string]bool)
		}
		used[day][kw.Cluster] = true
	}
	return used
}

// persistAndFanOut turns slot assignments into ScheduledArticle rows,
// batch-inserts them, stamps the WEC's scheduling total with newTotal (the
// calendar's full row count after the insert), and dispatches the article-type
// generation fan-out for the new rows only. The web entity's internal-linking
// default is snapshotted onto every slot so each article's per-article toggle
// can be edited independently later.
func (s *schedulingEngineService) persistAndFanOut(ctx context.Context, userId string, payload se.SEOrchestratePayload, we *models.WebEntity, assignments []assignment, newTotal int) ([]*models.ScheduledArticle, error) {
	userOID, err := primitive.ObjectIDFromHex(userId)
	if err != nil {
		return nil, fmt.Errorf("invalid user ID: %w", err)
	}
	webEntityOID, err := primitive.ObjectIDFromHex(payload.WebEntityID)
	if err != nil {
		return nil, fmt.Errorf("invalid web entity ID: %w", err)
	}
	wecOID, err := primitive.ObjectIDFromHex(payload.WebEntityContextID)
	if err != nil {
		return nil, fmt.Errorf("invalid web entity context ID: %w", err)
	}

	internalLinkingDefault := we.InternalLinkingEnabledOrDefault()
	docs := make([]*models.ScheduledArticle, len(assignments))
	for i, a := range assignments {
		docs[i] = &models.ScheduledArticle{
			KeywordID:              a.keyword.ID,
			UserID:                 userOID,
			WebEntityID:            webEntityOID,
			WebEntityContextID:     wecOID,
			ScheduleDate:           a.date,
			InternalLinkingEnabled: &internalLinkingDefault,
		}
	}

	if err := s.store.CreateScheduledArticles(ctx, docs); err != nil {
		return nil, fmt.Errorf("persist scheduled articles: %w", err)
	}

	if err := s.store.SetSchedulingTotal(ctx, payload.WebEntityContextID, newTotal); err != nil {
		return nil, fmt.Errorf("set scheduling total: %w", err)
	}

	for _, doc := range docs {
		msg := se.SEArticleStepPayload{ScheduledArticleID: doc.ID.Hex()}
		if err := s.dispatcher.Dispatch(ctx, string(se.ProcessSchedulingGenerateArticleType), userId, msg); err != nil {
			return nil, fmt.Errorf("dispatch article-type fan-out for %s: %w", doc.ID.Hex(), err)
		}
	}
	return docs, nil
}
