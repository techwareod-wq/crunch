package cron

import (
	"context"
	"time"

	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/services/analyticsService"
	"github.com/atharva-ng/crunch/internal/services/attributeService"
	"github.com/atharva-ng/crunch/internal/services/catalogService"
)

// WarehouseHub beats. All are dark until switched on in values
// (cron.jobs.<name>).
const (
	// JobRecomputeSafetyNet re-runs recompute_all nightly for anything still
	// lagging the current rulesVersion (D-040 safety net, off by default).
	JobRecomputeSafetyNet JobName = "attributes_recompute_safety_net"
	// JobMediaGC sweeps unconfirmed uploads and long-unreferenced media
	// (spec 03; switched on at launch, OP-06).
	JobMediaGC JobName = "catalog_media_gc"
	// JobAnalyticsRollup rolls yesterday's searches + enquiries into
	// search_daily, catching up any missed day (spec 07; on at launch,
	// OP-06).
	JobAnalyticsRollup JobName = "analytics_rollup_daily"

	warehouseHubSystemUser = "system"
	warehouseHubCatchUp    = 6 * time.Hour
)

func warehouseHubJobs(_ *ServiceLocator) []Job {
	return []Job{
		{
			Name:    JobRecomputeSafetyNet,
			Spec:    AtLocal("03:30"),
			Resolve: resolveRecomputeSafetyNet,
			Process: attributeService.ProcessRecomputeAll,
			CatchUp: warehouseHubCatchUp,
		},
		{
			Name:    JobMediaGC,
			Spec:    AtLocal("04:00"),
			Resolve: resolveMediaGC,
			Process: catalogService.ProcessMediaGC,
			CatchUp: warehouseHubCatchUp,
		},
		{
			Name:    JobAnalyticsRollup,
			Spec:    AtLocal("00:20"),
			Resolve: resolveAnalyticsRollup,
			Process: analyticsService.ProcessRollupDaily,
			CatchUp: warehouseHubCatchUp,
		},
	}
}

// resolveRecomputeSafetyNet enqueues one recompute_all at the current
// rulesVersion with a per-day run id, so its batch keys never collide with a
// rules-write fan-out.
func resolveRecomputeSafetyNet(ctx context.Context, occ Occurrence) ([]Unit, error) {
	v, err := models.GetCounter(ctx, models.CounterRulesVersion)
	if err != nil {
		return nil, err
	}
	run := "sn-" + occ.At.UTC().Format("20060102")
	return []Unit{{
		UserID:         warehouseHubSystemUser,
		Payload:        attributeService.RecomputeAllPayload{RulesVersion: v, Run: run},
		IdempotencyKey: attributeService.RecomputeAllKey(v, run),
	}}, nil
}

// resolveMediaGC enqueues one sweep per day.
func resolveMediaGC(_ context.Context, occ Occurrence) ([]Unit, error) {
	return []Unit{{
		UserID:         warehouseHubSystemUser,
		Payload:        struct{}{},
		IdempotencyKey: UnitKey(JobMediaGC, occ.At.UTC().Format("20060102"), "sweep"),
	}}, nil
}

// resolveAnalyticsRollup enqueues one rollup per day; the job itself
// works out which days are still missing.
func resolveAnalyticsRollup(_ context.Context, occ Occurrence) ([]Unit, error) {
	run := occ.At.UTC().Format("20060102")
	return []Unit{{
		UserID:         warehouseHubSystemUser,
		Payload:        analyticsService.RollupPayload{Run: run},
		IdempotencyKey: UnitKey(JobAnalyticsRollup, run, "rollup"),
	}}, nil
}
