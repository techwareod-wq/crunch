package config

import (
	"path/filepath"
	"reflect"
	"testing"
)

// allowlistedEnvDiffs are the ONLY value-level differences permitted between
// values/integration/values.yaml and values/production/values.yaml. Everything
// else must stay in lockstep across the two environments; a new divergence is
// almost always an accidental drift (a default tuned in one file but not the
// other), which this test catches.
//
//   - server.allowedHosts — prod restricts the Host header; integration is open.
//   - async.workerCount    — prod runs a larger general async worker pool;
//     integration stays small.
//   - cron.jobs.*.enabled — the per-beat kill switch, and the whole point of
//     integration: a beat goes live there first and stays dark in prod until it
//     has proven itself. Only the flag is exempt — the job KEY SET and every
//     schedule knob (at/zone/weekdays/everySeconds/maxUnits)
//     are still compared, so a time tuned in one file only still fails.
//
// (If a genuinely per-env key is added, neutralise it below and document why.)

// TestValues_EnvFilesDriftGuard asserts the two environment values files are
// identical once the intentional per-env keys are neutralised. Keeping them in
// sync is what lets integration meaningfully rehearse production behaviour.
func TestValues_EnvFilesDriftGuard(t *testing.T) {
	root := repoRoot(t)

	load := func(env string) Values {
		t.Helper()
		var c AppConfig
		t.Setenv("VALUES_FILE", filepath.Join(root, "values", env, "values.yaml"))
		if err := LoadValues(&c); err != nil {
			t.Fatalf("LoadValues(%s): %v", env, err)
		}
		return c.Values
	}

	integration := load("integration")
	production := load("production")

	// Neutralise the allowlisted per-env keys in both copies so the comparison
	// only sees the surface that is meant to be identical.
	for _, v := range []*Values{&integration, &production} {
		v.Server.AllowedHosts = nil
		v.Async.WorkerCount = 0
		// Blank each cron job's on/off flag but keep the entry, so a job
		// present in one file and missing from the other is still a drift.
		for name, job := range v.Cron.Jobs {
			job.Enabled = false
			v.Cron.Jobs[name] = job
		}
	}

	if reflect.DeepEqual(integration, production) {
		return
	}

	// Report exactly which top-level sections diverged to make a failure
	// actionable instead of dumping two giant structs.
	iv := reflect.ValueOf(integration)
	pv := reflect.ValueOf(production)
	for i := 0; i < iv.NumField(); i++ {
		field := iv.Type().Field(i)
		if !reflect.DeepEqual(iv.Field(i).Interface(), pv.Field(i).Interface()) {
			t.Errorf("values drift in section %q (yaml %q): integration=%+v production=%+v\n"+
				"if this difference is intentional and per-env, add the key to allowlistedEnvDiffs.",
				field.Name, field.Tag.Get("yaml"),
				iv.Field(i).Interface(), pv.Field(i).Interface())
		}
	}
}
