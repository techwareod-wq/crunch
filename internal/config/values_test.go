package config

import (
	"os"
	"path/filepath"
	"testing"
)

const testValuesYAML = `
server:
  port: "3090"
  defaultDBName: "central"
  awsRegion: "ap-south-1"
async:
  workerCount: 10
  llmWorkerCount: 5
  maxRetries: 3
  shutdownSeconds: 30
siteIntelligence:
  scoring:
    weightVolume: 0.40
`

// writeValuesFile writes content to <dir>/values/<env>/values.yaml and returns
// the file path.
func writeValuesFile(t *testing.T, dir, env, content string) string {
	t.Helper()
	envDir := filepath.Join(dir, "values", env)
	if err := os.MkdirAll(envDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(envDir, "values.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

// (a) YAML parse: values are unmarshalled into AppConfig.Values.
func TestLoadValues_ParsesYAML(t *testing.T) {
	path := writeValuesFile(t, t.TempDir(), "production", testValuesYAML)
	t.Setenv("VALUES_FILE", path)

	var c AppConfig
	if err := LoadValues(&c); err != nil {
		t.Fatalf("LoadValues: %v", err)
	}

	if c.Values.Server.Port != "3090" {
		t.Errorf("Server.Port = %q, want %q", c.Values.Server.Port, "3090")
	}
	if c.Values.Async.WorkerCount != 10 {
		t.Errorf("Async.WorkerCount = %d, want 10", c.Values.Async.WorkerCount)
	}
	if c.Values.SiteIntelligence.Scoring.WeightVolume != 0.40 {
		t.Errorf("Scoring.WeightVolume = %v, want 0.40", c.Values.SiteIntelligence.Scoring.WeightVolume)
	}
}

// (b) env var overriding a YAML default — the layering happens in the re-pointed
// loaders (here LoadAsyncHandlerConfig), which read c.Values as their default.
func TestLoadValues_EnvOverridesYAMLDefault(t *testing.T) {
	path := writeValuesFile(t, t.TempDir(), "production", testValuesYAML)
	t.Setenv("VALUES_FILE", path)

	var c AppConfig
	if err := LoadValues(&c); err != nil {
		t.Fatalf("LoadValues: %v", err)
	}

	// Env unset: the YAML default flows through.
	t.Setenv("ASYNC_WORKER_COUNT", "")
	c.LoadAsyncHandlerConfig()
	if c.AsyncHandler.WorkerCount != 10 {
		t.Errorf("worker count without env = %d, want YAML default 10", c.AsyncHandler.WorkerCount)
	}

	// Env set: it overrides the YAML default.
	t.Setenv("ASYNC_WORKER_COUNT", "2")
	c.LoadAsyncHandlerConfig()
	if c.AsyncHandler.WorkerCount != 2 {
		t.Errorf("worker count with env = %d, want override 2", c.AsyncHandler.WorkerCount)
	}
}

// TestValues_GoldenDefaults loads the committed values/<env>/values.yaml files
// and asserts the externalised tunables load with the defaults that reproduce
// today's previously-hardcoded constants. It is a drift guard: editing a default
// in the YAML (or dropping a key) fails here. All of these keys are identical
// across integration and production, so both envs assert the same values.
func TestValues_GoldenDefaults(t *testing.T) {
	root := repoRoot(t)
	for _, env := range []string{"integration", "production"} {
		t.Run(env, func(t *testing.T) {
			path := filepath.Join(root, "values", env, "values.yaml")
			t.Setenv("VALUES_FILE", path)

			var c AppConfig
			if err := LoadValues(&c); err != nil {
				t.Fatalf("LoadValues(%s): %v", env, err)
			}
			v := c.Values

			// C21: OpenAI fallback model.
			if got := v.LLM.OpenAI.FallbackModel; got != "gpt-4o" {
				t.Errorf("LLM.OpenAI.FallbackModel = %q, want %q", got, "gpt-4o")
			}
			// C22: Gemini fallback model.
			if got := v.LLM.Gemini.FallbackModel; got != "gemini-2.0-flash" {
				t.Errorf("LLM.Gemini.FallbackModel = %q, want %q", got, "gemini-2.0-flash")
			}
			// C23: shared HTTP client timeout.
			if got := v.APIs.HTTPClient.TimeoutSeconds; got != 60 {
				t.Errorf("APIs.HTTPClient.TimeoutSeconds = %d, want 60", got)
			}
			// C24: post-upgrade publishing cadence.
			if got := v.Scheduling.UpgradeCadence; got != 15 {
				t.Errorf("Scheduling.UpgradeCadence = %d, want 15", got)
			}
			// C25: plans/roles cache refresh interval.
			if got := v.Admin.CacheRefreshSeconds; got != 60 {
				t.Errorf("Admin.CacheRefreshSeconds = %d, want 60", got)
			}
			// C26: gated-SQS backpressure pause.
			if got := v.SQS.GatedPauseSeconds; got != 5 {
				t.Errorf("SQS.GatedPauseSeconds = %d, want 5", got)
			}
			// C27: admin pagination caps.
			if got := v.Admin.Pagination; got != (AdminPaginationValues{
				UsersDefault: 20, UsersMax: 100, AuditDefault: 50, AuditMax: 200,
			}) {
				t.Errorf("Admin.Pagination = %+v, want {20 100 50 200}", got)
			}
			// C28: article-length multiplier.
			if got := v.ContentGeneration.ArticleLengthMultiplier; got != 1.25 {
				t.Errorf("ContentGeneration.ArticleLengthMultiplier = %v, want 1.25", got)
			}
		})
	}
}

// Strategy resolution: a strategy entry replaces the filter set and overrides
// only the scoring knobs it lists; unknown strategies fall back to the base
// (balanced_growth) values.
func TestSiteIntelligenceValues_StrategyResolution(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	base := SiteIntelligenceValues{
		KeywordFilters: KeywordFilterValues{MaxRankPosition: 30, MinSearchVolume: 100, DifficultyFloor: 5},
		Scoring:        ScoringValues{VolumeLogAnchor: 100000, WeightVolume: 0.40, WeightDifficulty: 0.40, WeightFunnel: 0.20, OpportunityScale: 100},
		Strategies: map[string]SIEStrategyValues{
			"early_footholds": {
				KeywordFilters: &KeywordFilterValues{MinSearchVolume: 10, MaxSearchVolume: 1000, DifficultyMaxAbsolute: 15},
				Scoring:        &ScoringOverrideValues{VolumeLogAnchor: f(10000), WeightVolume: f(0.25), WeightDifficulty: f(0.55)},
			},
		},
	}

	t.Run("strategy filters replace the base set", func(t *testing.T) {
		got := base.FiltersForStrategy("early_footholds")
		if got.MinSearchVolume != 10 || got.MaxSearchVolume != 1000 || got.DifficultyMaxAbsolute != 15 {
			t.Errorf("FiltersForStrategy(early_footholds) = %+v, want the override set", got)
		}
	})

	t.Run("unknown strategy falls back to base filters", func(t *testing.T) {
		if got := base.FiltersForStrategy("balanced_growth"); got != base.KeywordFilters {
			t.Errorf("FiltersForStrategy(balanced_growth) = %+v, want base filters", got)
		}
	})

	t.Run("scoring overrides listed knobs and inherits the rest", func(t *testing.T) {
		got := base.ScoringForStrategy("early_footholds")
		if got.VolumeLogAnchor != 10000 || got.WeightVolume != 0.25 || got.WeightDifficulty != 0.55 {
			t.Errorf("ScoringForStrategy overrides not applied: %+v", got)
		}
		if got.WeightFunnel != 0.20 || got.OpportunityScale != 100 {
			t.Errorf("unlisted knobs must inherit the base values: %+v", got)
		}
	})

	t.Run("unknown strategy falls back to base scoring", func(t *testing.T) {
		if got := base.ScoringForStrategy("balanced_growth"); got != base.Scoring {
			t.Errorf("ScoringForStrategy(balanced_growth) = %+v, want base scoring", got)
		}
	})
}

// Golden guard for the committed early_footholds strategy: the values that
// implement "vol 10–1000, KD ≤ 15, difficulty-tilted scoring" must survive
// YAML edits in both environments.
func TestValues_StrategyGoldenDefaults(t *testing.T) {
	root := repoRoot(t)
	for _, env := range []string{"integration", "production"} {
		t.Run(env, func(t *testing.T) {
			t.Setenv("VALUES_FILE", filepath.Join(root, "values", env, "values.yaml"))

			var c AppConfig
			if err := LoadValues(&c); err != nil {
				t.Fatalf("LoadValues(%s): %v", env, err)
			}
			si := c.Values.SiteIntelligence

			filters := si.FiltersForStrategy("early_footholds")
			want := KeywordFilterValues{
				MaxRankPosition:       30,
				MinSearchVolume:       10,
				MaxSearchVolume:       1000,
				DifficultyFloor:       0,
				DifficultyMaxAbsolute: 15,
			}
			if filters != want {
				t.Errorf("early_footholds filters = %+v, want %+v", filters, want)
			}

			scoring := si.ScoringForStrategy("early_footholds")
			if scoring.VolumeLogAnchor != 10000.0 || scoring.WeightVolume != 0.25 ||
				scoring.WeightDifficulty != 0.55 || scoring.WeightFunnel != 0.20 {
				t.Errorf("early_footholds scoring knobs = anchor %v, weights %v/%v/%v; want 10000/0.25/0.55/0.20",
					scoring.VolumeLogAnchor, scoring.WeightVolume, scoring.WeightDifficulty, scoring.WeightFunnel)
			}
			// The unlisted knobs must keep tracking the base scoring block.
			if scoring.OpportunityScale != si.Scoring.OpportunityScale ||
				scoring.OpportunityGamma != si.Scoring.OpportunityGamma {
				t.Errorf("early_footholds must inherit unlisted scoring knobs: %+v", scoring)
			}

			// balanced_growth has no entry on purpose: the base blocks are its config.
			if got := si.FiltersForStrategy("balanced_growth"); got != si.KeywordFilters {
				t.Errorf("balanced_growth filters = %+v, want the base keywordFilters", got)
			}
		})
	}
}

// (c) an environment whose values file is absent is fatal — no silent fallback.
func TestLoadValues_UnmatchedEnvironmentIsFatal(t *testing.T) {
	dir := t.TempDir()
	writeValuesFile(t, dir, "production", testValuesYAML)
	t.Setenv("VALUES_FILE", "") // force relative values/<env> resolution

	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() { _ = os.Chdir(origWD) }()

	var c AppConfig
	c.Env.Environment = "staging" // no values/staging/values.yaml on disk
	if err := LoadValues(&c); err == nil {
		t.Fatal("LoadValues: want error for an environment without a values file, got nil")
	}
}

// (d) an empty environment is fatal rather than falling back.
func TestLoadValues_EmptyEnvironmentIsFatal(t *testing.T) {
	t.Setenv("VALUES_FILE", "") // no explicit override

	var c AppConfig
	c.Env.Environment = ""
	if err := LoadValues(&c); err == nil {
		t.Fatal("LoadValues: want error for empty ENVIRONMENT, got nil")
	}
}
