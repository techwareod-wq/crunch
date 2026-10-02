package config

import (
	"os"
	"path/filepath"
	"testing"
)

const testValuesYAML = `
server:
  port: "3090"
  defaultDBName: "crunch"
  awsRegion: "ap-south-1"
async:
  workerCount: 10
  llmWorkerCount: 5
  maxRetries: 3
  shutdownSeconds: 30
llm:
  defaultMaxTokens: 5000
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
	if c.Values.LLM.DefaultMaxTokens != 5000 {
		t.Errorf("LLM.DefaultMaxTokens = %d, want 5000", c.Values.LLM.DefaultMaxTokens)
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

			// Shared HTTP client timeout.
			if got := v.APIs.HTTPClient.TimeoutSeconds; got != 60 {
				t.Errorf("APIs.HTTPClient.TimeoutSeconds = %d, want 60", got)
			}
			// Gated-SQS backpressure pause.
			if got := v.SQS.GatedPauseSeconds; got != 5 {
				t.Errorf("SQS.GatedPauseSeconds = %d, want 5", got)
			}
			// Admin pagination caps.
			if got := v.Admin.Pagination; got != (AdminPaginationValues{
				UsersDefault: 20, UsersMax: 100, AuditDefault: 50, AuditMax: 200,
			}) {
				t.Errorf("Admin.Pagination = %+v, want {20 100 50 200}", got)
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
