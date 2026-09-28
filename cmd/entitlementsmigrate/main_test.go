package main

import (
	"path/filepath"
	"testing"
)

// resolvePlansFile mirrors config.LoadValues' resolution order: explicit
// override wins, otherwise the per-environment file, and neither an empty
// environment nor a missing file may silently fall back — seeding one
// environment's price_ids into another's catalog is the exact mistake the
// per-environment split prevents.
func TestResolvePlansFile(t *testing.T) {
	// Explicit override is returned verbatim, no environment consulted.
	if got, err := resolvePlansFile("custom.json", ""); err != nil || got != "custom.json" {
		t.Fatalf("explicit override = (%q, %v), want (custom.json, nil)", got, err)
	}

	// The repo's real integration seed resolves for its environment.
	dir := filepath.Join("..", "..")
	t.Chdir(dir)
	want := filepath.Join("values", "integration", "plans.json")
	if got, err := resolvePlansFile("", "integration"); err != nil || got != want {
		t.Fatalf("integration = (%q, %v), want (%q, nil)", got, want, got)
	}

	// An empty environment is fatal — no fallback.
	if _, err := resolvePlansFile("", ""); err == nil {
		t.Error("empty environment: want error, got nil")
	}

	// An environment with no seed file on disk is fatal — no fallback.
	if _, err := resolvePlansFile("", "staging"); err == nil {
		t.Error("environment without a seed file: want error, got nil")
	}
}
