package cron

import (
	"fmt"
	"time"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/util/log"
)

// ServiceLocator carries what job resolvers need.
type ServiceLocator struct {
	// DefaultZone is the IANA fallback zone (values cron.defaultZone).
	DefaultZone string
}

// BuildJobRegistry assembles every registered job from its code definition +
// values overrides. Schedule DEFINITIONS live in code; their times and on/off
// flags live in values (cron.jobs.<name>), so a misbehaving beat dies with a
// values change and a restart — no code, no migration.
//
// A job with no values entry, or enabled: false, is dark — logged and skipped.
func BuildJobRegistry(l *ServiceLocator, v config.CronValues) ([]Job, error) {
	defs := []Job{}
	defs = append(defs, warehouseHubJobs(l)...)

	jobs := make([]Job, 0, len(defs))
	for _, def := range defs {
		jv, ok := v.Jobs[string(def.Name)]
		if !ok || !jv.Enabled {
			log.Info("cron: job disabled", "job", def.Name, "configured", ok)
			continue
		}
		spec, err := applyJobValues(def.Spec, jv)
		if err != nil {
			return nil, fmt.Errorf("cron: job %q values: %w", def.Name, err)
		}
		def.Spec = spec
		if jv.MaxUnits > 0 {
			def.MaxUnits = jv.MaxUnits
		}
		jobs = append(jobs, def)
	}
	return jobs, nil
}

// applyJobValues overlays the values-file schedule knobs onto a code-defined
// spec: at → HourMin, zone → Zone, weekdays → Weekdays, everySeconds →
// Interval. Empty values keep the code default.
func applyJobValues(spec Spec, jv config.CronJobValues) (Spec, error) {
	if jv.At != "" {
		spec.HourMin = jv.At
	}
	if jv.Zone != "" {
		spec.Zone = jv.Zone
	}
	if jv.Weekdays != "" {
		days, err := ParseWeekdays(jv.Weekdays)
		if err != nil {
			return spec, err
		}
		spec.Weekdays = days
	}
	if jv.DayOfMonth > 0 {
		spec.DayOfMonth = jv.DayOfMonth
	}
	if jv.EverySeconds > 0 {
		spec.Interval = time.Duration(jv.EverySeconds) * time.Second
	}
	return spec, nil
}
