package config

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// getenvKeyPattern matches every env var name read via os.Getenv("NAME") or via
// the atoiEnv("NAME") integer-override helper (which reads os.Getenv internally
// with a variable, so its literal keys must be captured here too).
var getenvKeyPattern = regexp.MustCompile(`(?:os\.Getenv|atoiEnv)\("([A-Z0-9_]+)"\)`)

// envExampleKeyPattern matches a declared key line in .env.example, e.g.
// "MONGO_URI=" or "PORT=3090". Comment and blank lines never match.
var envExampleKeyPattern = regexp.MustCompile(`^([A-Z0-9_]+)=`)

// repoRoot walks up from this test file's directory until it finds go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found walking up from test file")
		}
		dir = parent
	}
}

// collectGetenvKeys scans every non-test .go file under internal/ and cmd/ and
// returns the set of env var names passed to os.Getenv.
func collectGetenvKeys(t *testing.T, root string) map[string]struct{} {
	t.Helper()
	keys := make(map[string]struct{})
	for _, sub := range []string{"internal", "cmd"} {
		base := filepath.Join(root, sub)
		if _, err := os.Stat(base); os.IsNotExist(err) {
			continue
		}
		err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			for _, m := range getenvKeyPattern.FindAllStringSubmatch(string(data), -1) {
				keys[m[1]] = struct{}{}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", base, err)
		}
	}
	return keys
}

// collectEnvExampleKeys reads .env.example at the repo root and returns the set
// of declared keys.
func collectEnvExampleKeys(t *testing.T, root string) map[string]struct{} {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, ".env.example"))
	if err != nil {
		t.Fatalf("reading .env.example: %v", err)
	}
	keys := make(map[string]struct{})
	for _, line := range strings.Split(string(data), "\n") {
		if m := envExampleKeyPattern.FindStringSubmatch(line); m != nil {
			keys[m[1]] = struct{}{}
		}
	}
	return keys
}

// TestEnvExampleCoversAllGetenvKeys guards against drift: every env var read via
// os.Getenv in internal/ or cmd/ must be declared in .env.example.
func TestEnvExampleCoversAllGetenvKeys(t *testing.T) {
	root := repoRoot(t)
	used := collectGetenvKeys(t, root)
	declared := collectEnvExampleKeys(t, root)

	var missing []string
	for key := range used {
		if _, ok := declared[key]; !ok {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)

	if len(missing) > 0 {
		t.Errorf("env keys read via os.Getenv but missing from .env.example: %v", missing)
	}
}
