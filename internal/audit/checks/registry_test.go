package checks

import (
	"strings"
	"testing"

	"github.com/atharva-ng/crunch/internal/audit/collectors"
	"github.com/atharva-ng/crunch/internal/audit/core"
	"github.com/atharva-ng/crunch/internal/audit/spec"
	"github.com/atharva-ng/crunch/internal/config"
)

// These are the tests that keep the Tier A/B/C extension recipes safe: any
// registry/spec inconsistency must die at boot, never at runtime.

func testCollectors(t *testing.T) []collectors.Collector {
	t.Helper()
	list, err := collectors.BuildCollectorRegistry(config.AuditValues{})
	if err != nil {
		t.Fatalf("BuildCollectorRegistry: %v", err)
	}
	return list
}

func TestBuildCheckRegistry_HappyPath(t *testing.T) {
	reg, err := BuildCheckRegistry(spec.SpecV1(), testCollectors(t), config.AuditValues{})
	if err != nil {
		t.Fatalf("BuildCheckRegistry: %v", err)
	}
	if len(reg.All()) == 0 {
		t.Fatal("registry is empty")
	}
	// Exactly one LLM check in V1, and it must implement LLMCheck.
	llm := reg.LLMChecks()
	if len(llm) != 1 || llm[0].ID() != "content.llm_judgment" {
		t.Fatalf("LLMChecks = %v, want exactly content.llm_judgment", llm)
	}
	// Every deterministic check reachable by ID.
	for _, c := range reg.Deterministic() {
		if _, ok := reg.ByID(c.ID()); !ok {
			t.Errorf("check %s not resolvable ByID", c.ID())
		}
	}
}

// TestBuildCheckRegistry_UpliftChecksRegistered proves the eight
// quality-uplift checks are registered and the V1 graph stays valid
// (category sums hit 100 with the ratified table).
func TestBuildCheckRegistry_UpliftChecksRegistered(t *testing.T) {
	reg, err := BuildCheckRegistry(spec.SpecV1(), testCollectors(t), config.AuditValues{})
	if err != nil {
		t.Fatalf("BuildCheckRegistry(SpecV1): %v", err)
	}
	for _, id := range []string{
		"technical.security_headers", "technical.soft_404", "technical.site_files",
		"onpage.head_hygiene", "content.scaled_patterns", "schema.coverage",
		"aisearch.brand_footprint", "aisearch.rendered_parity",
	} {
		if _, ok := reg.ByID(core.CheckID(id)); !ok {
			t.Errorf("new check %s not registered", id)
		}
	}
}

// TestBuildCheckRegistry_EveryRequireHasProducer proves the blackboard graph
// is closed: no check demands a kind no collector produces.
func TestBuildCheckRegistry_EveryRequireHasProducer(t *testing.T) {
	produced := collectors.ProducedKinds(testCollectors(t))
	for _, c := range staticChecks() {
		for _, k := range c.Requires() {
			if !produced[k] {
				t.Errorf("check %s requires %q which no collector produces", c.ID(), k)
			}
		}
	}
}

func TestBuildCheckRegistry_UnknownFindingsOnlyRejected(t *testing.T) {
	vals := config.AuditValues{}
	vals.Scoring.FindingsOnly = []string{"nope.not_a_check"}
	_, err := BuildCheckRegistry(spec.SpecV1(), testCollectors(t), vals)
	if err == nil || !strings.Contains(err.Error(), "nope.not_a_check") {
		t.Fatalf("unknown findingsOnly id must fail boot, got %v", err)
	}
}

func TestBuildCheckRegistry_FindingsOnlyDemotionResolved(t *testing.T) {
	vals := config.AuditValues{}
	vals.Scoring.FindingsOnly = []string{"onpage.titles", "onpage.titles"}
	reg, err := BuildCheckRegistry(spec.SpecV1(), testCollectors(t), vals)
	if err != nil {
		t.Fatalf("BuildCheckRegistry: %v", err)
	}
	if got := reg.FindingsOnly(); len(got) != 1 || got[0] != "onpage.titles" {
		t.Fatalf("FindingsOnly = %v, want deduped [onpage.titles]", got)
	}
}

// TestBuildCheckRegistry_AllocationForNonexistentCheck: a spec allocating
// points to a check that isn't registered must fail boot.
func TestBuildCheckRegistry_AllocationForNonexistentCheck(t *testing.T) {
	s := spec.SpecV1()
	s.Allocations["ghost.check"] = 0
	_, err := BuildCheckRegistry(s, testCollectors(t), config.AuditValues{})
	if err == nil || !strings.Contains(err.Error(), "ghost.check") {
		t.Fatalf("ghost allocation must fail boot, got %v", err)
	}
}

// TestBuildCheckRegistry_CategorySumsBroken: rebalancing one allocation off
// 100 must fail boot.
func TestBuildCheckRegistry_CategorySumsBroken(t *testing.T) {
	s := spec.SpecV1()
	s.Allocations["onpage.titles"] = 99 // on_page now sums to 174 ≠ 100... any ≠100 fails
	_, err := BuildCheckRegistry(s, testCollectors(t), config.AuditValues{})
	if err == nil || !strings.Contains(err.Error(), "on_page") {
		t.Fatalf("broken category sum must fail boot, got %v", err)
	}
}
