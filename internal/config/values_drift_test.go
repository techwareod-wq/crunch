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
//   - async.tokenLimit     — prod runs a higher LLM token budget.
//   - async.llmWorkerCount — prod runs a larger LLM worker pool (sized to the
//     Scale-tier Anthropic rate limits); integration stays small.
//   - async.workerCount    — prod runs a larger general async worker pool;
//     integration stays small.
//   - apis.sidecar.baseURL — prod uses the compose-internal service name.
//   - company.inviteUrlTemplate — a frontend URL by nature (integration
//     points at the local `next dev`; prod at the app domain). The TTL knob
//     is NOT exempt and stays in lockstep.
//   - company.enabled — the company-surface master switch, same lifecycle as
//     the cron flags: live in integration first, dark in prod until proven.
//   - gsc.enabled — the GSC analytics-surface master switch, same lifecycle
//     as company.enabled. The top-N fetch caps are NOT exempt and stay in
//     lockstep.
//   - audit.enabled — the audit-surface master switch, same lifecycle as
//     gsc.enabled (live in integration first, dark in prod until launch).
//     Every other audit knob (caps, sampling, gating, models) is NOT exempt
//     and stays in lockstep.
//   - cron.jobs.*.enabled — the per-beat kill switch, and the whole point of
//     integration: a beat goes live there first and stays dark in prod until it
//     has proven itself. Only the flag is exempt — the job KEY SET and every
//     schedule knob (at/zone/weekdays/everySeconds/maxUnits)
//     are still compared, so a time tuned in one file only still fails.
//
// (See docs/IMPLEMENTATION_PLAN.md C39. If a genuinely per-env key is added,
// neutralise it below and document why.)

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
		v.Async.TokenLimit = 0
		v.Async.LLMWorkerCount = 0
		v.Async.WorkerCount = 0
		v.APIs.Sidecar.BaseURL = ""
		v.Company.InviteURLTemplate = ""
		v.Company.Enabled = false
		v.GSC.Enabled = false
		v.Audit.Enabled = false
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
