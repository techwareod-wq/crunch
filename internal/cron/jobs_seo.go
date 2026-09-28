package cron

import (
	"context"
	"time"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/entitlements"
	"github.com/atharva-ng/crunch/internal/models"
	cge "github.com/atharva-ng/crunch/internal/services/contentGenerationEngine"
	"github.com/atharva-ng/crunch/internal/util/log"
)

// JobSEODailyGeneration is the SEO scheduling engine's daily article sweep:
// every scheduled article whose calendar day is TODAY gets its
// CGE_ORCHESTRATE dispatched — the same entry point the dashboard "Generate"
// button and the first-article auto-kickoff already use. Articles from past
// days are never auto-generated: the sweep does no backlog catch-up, so a
// stale calendar costs nothing until the user generates those slots manually.
const JobSEODailyGeneration JobName = "seo_daily_generation"

const (
	// seoDefaultAt is the code-default sweep time; values cron.jobs.*.at
	// overrides it per environment.
	seoDefaultAt = "09:00"
	// seoCatchUp: idempotent downstream (the CGE status flip), so running
	// late is harmless and the window is generous.
	seoCatchUp = 4 * time.Hour
)

func seoSchedulingJobs(l *ServiceLocator, v config.CronValues) []Job {
	jv := v.Jobs[string(JobSEODailyGeneration)]
	zone := jv.Zone
	if zone == "" {
		zone = l.DefaultZone
	}
	return []Job{{
		Name:    JobSEODailyGeneration,
		Spec:    AtLocal(seoDefaultAt),
		Resolve: seoDailyResolver(l, zone),
		Process: cge.ProcessCGEOrchestrate,
		CatchUp: seoCatchUp,
	}}
}

// seoDailyResolver is the daily sweep: one indexed query for every article in
// state scheduled with schedule_date <= today, then an entitlement gate.
// Articles dated before today are skipped outright (logged) — auto-generation
// is for today's slots only; overdue slots stay scheduled and remain reachable
// through the dashboard's manual Generate. It does NOT flip status — CGE
// Orchestrate marks the slot generating itself as its first act; a
// resolver-side CAS crashing before enqueue would strand the article invisible
// to every future sweep.
func seoDailyResolver(l *ServiceLocator, zone string) Resolver {
	return func(ctx context.Context, occ Occurrence) ([]Unit, error) {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			return nil, err
		}
		// schedule_date is stored as 00:00 UTC of the calendar day, so "today"
		// is the occurrence's local date re-anchored to UTC midnight.
		y, m, d := occ.At.In(loc).Date()
		startOfToday := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)

		due, err := models.ListDueScheduledArticles(ctx, occ.At)
		if err != nil {
			return nil, err
		}

		now := time.Now().UTC()
		allowed := map[string]bool{} // per-user entitlement verdicts, one lookup each
		var units []Unit
		skippedOverdue := 0

		for _, sa := range due {
			// Overdue articles (before today) are never auto-generated; only
			// today's slots dispatch. Logged — a silent skip reads as "covered
			// everything" when it didn't.
			if sa.ScheduleDate.Before(startOfToday) {
				skippedOverdue++
				continue
			}

			userID := sa.UserID.Hex()
			verdict, seen := allowed[userID]
			if !seen {
				_, user, err := models.FindUserByID(ctx, userID)
				if err != nil {
					return nil, err
				}
				verdict = user != nil &&
					entitlements.Resolve(user, models.AppIDIndexly, entitlements.FeatureArticleGenerate, l.PlansCache, now) == entitlements.Allow
				allowed[userID] = verdict
			}
			if !verdict {
				continue
			}

			units = append(units, Unit{
				UserID: userID,
				Payload: cge.CGEOrchestratePayload{
					ScheduledArticleID:     sa.ID.Hex(),
					WebEntityContextID:     sa.WebEntityContextID.Hex(),
					KeywordID:              sa.KeywordID.Hex(),
					ArticleType:            string(sa.ArticleType),
					ProposedTitle:          sa.Title,
					AdditionalInstructions: sa.AdditionalInstructions,
					InternalLinkingEnabled: sa.InternalLinkingEnabled,
					ThumbnailStyle:         sa.ThumbnailStyle,
				},
				// Entity-scoped key: the per-user default would dedupe a user's
				// second due article into oblivion.
				IdempotencyKey: UnitKey(occ.Job, occ.At.UTC().Format(time.RFC3339), sa.ID.Hex()),
			})
		}

		if skippedOverdue > 0 {
			log.Warn("cron: seo sweep skipped overdue articles (no backlog catch-up)",
				"skipped", skippedOverdue, "occurrence", occ.At)
		}
		return units, nil
	}
}
