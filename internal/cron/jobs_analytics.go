package cron

import (
	"context"
	"fmt"
	"time"

	"github.com/atharva-ng/crunch/internal/analytics"
	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/util/log"
)

// Analytics ingest beats — auto-registered per source from the analytics
// source registry (LLD §3.2): one Job per registered source, named
// analytics_<source>, all enqueueing the same shared ProcessAnalyticsIngest
// with the source name in the payload. Adding a source needs zero cron-layer
// edits — it appears here (and in the values kill-switch namespace) the moment
// it's registered at boot.
const (
	// analyticsDailyCatchUp: the rolling re-fetch window self-heals a missed
	// day anyway, so catching up late is harmless and the window is generous.
	analyticsDailyCatchUp = 6 * time.Hour
	// analyticsMonthlyCatchUp: a monthly beat missed on its calendar day would
	// otherwise wait a month — allow two days of catch-up.
	analyticsMonthlyCatchUp = 48 * time.Hour
)

func analyticsJobs(l *ServiceLocator, v config.CronValues) []Job {
	var jobs []Job
	for _, src := range analytics.Sources() {
		name := JobName("analytics_" + src.Name())
		sched := src.Schedule()
		spec := AtLocal(sched.DefaultAt)
		catchUp := analyticsDailyCatchUp
		if sched.Cadence == "monthly" {
			spec = spec.OnDayOfMonth(sched.DayOfMonth)
			catchUp = analyticsMonthlyCatchUp
		}
		jobs = append(jobs, Job{
			Name:    name,
			Spec:    spec,
			Resolve: analyticsResolver(src, name),
			Process: analytics.ProcessAnalyticsIngest,
			CatchUp: catchUp,
		})
	}
	return jobs
}

// analyticsResolver lists the entities due for one source: connected ones for
// connection-requiring sources (gsc), all finalised ones for connection-less
// sources (dfs_domain_rating). One unit per entity; the idempotency key
// includes the WINDOW END date — the per-occurrence default would block
// legitimate daily re-runs of overlapping windows, and the date family is what
// lets a verify-triggered immediate first ingest dedupe cleanly against that
// night's cron occurrence.
func analyticsResolver(src analytics.Source, job JobName) Resolver {
	return func(ctx context.Context, occ Occurrence) ([]Unit, error) {
		var entities []*models.WebEntity
		var err error
		if src.RequiresConnection() {
			entities, err = models.FindWebEntitiesWithIntegrationStatus(ctx, src.Name(), models.IntegrationStatusConnected)
		} else {
			entities, err = models.FindFinalisedWebEntities(ctx)
		}
		if err != nil {
			return nil, fmt.Errorf("resolve %s entities: %w", src.Name(), err)
		}

		window := analytics.IngestWindow(src)
		var units []Unit
		for _, entity := range entities {
			userID, err := models.RepresentativeUserIDForWebEntity(ctx, entity)
			if err != nil {
				// One orphaned entity must not sink the whole occurrence.
				log.Error("cron: analytics unit skipped, no representative user",
					"job", job, "webEntityId", entity.ID.Hex(), "error", err)
				continue
			}
			units = append(units, Unit{
				UserID: userID,
				Payload: analytics.IngestPayload{
					Source:      src.Name(),
					WebEntityID: entity.ID.Hex(),
					Window:      window,
				},
				IdempotencyKey: UnitKey(job, window.To, entity.ID.Hex()),
			})
		}
		return units, nil
	}
}
