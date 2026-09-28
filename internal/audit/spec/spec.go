// Package spec holds the versioned scoring contract: category weights and
// per-check point allocations. Reweighting or recategorizing is ALWAYS a new
// version literal (v2.go, ...), never an edit to an existing one — old
// reports keep their snapshot and stay comparable (decision 13).
package spec

import (
	"fmt"

	"github.com/atharva-ng/crunch/internal/audit/core"
)

// Category is one scored report category with its overall weight.
type Category struct {
	ID     core.CategoryID
	Label  string
	Weight int // SpecV1: sums to 100 across categories (decision 13)
}

// Spec is one scoring-contract version.
type Spec struct {
	Version    string
	Categories []Category
	// Allocations are points within the check's category; each category's
	// score-feeding checks must sum to 100 (validated at boot against the
	// check registry, which knows each check's category).
	Allocations map[core.CheckID]int
}

// CategoryWeight returns the weight for a category id (0, false when the
// category is not in this spec).
func (s Spec) CategoryWeight(id core.CategoryID) (int, bool) {
	for _, c := range s.Categories {
		if c.ID == id {
			return c.Weight, true
		}
	}
	return 0, false
}

// HasCategory reports whether the category exists in this spec.
func (s Spec) HasCategory(id core.CategoryID) bool {
	_, ok := s.CategoryWeight(id)
	return ok
}

// Snapshot is what gets stamped onto every run doc (decision 12): the spec
// version, the weights and allocations actually used, and the resolved
// findings-only set — a report is self-describing forever, even after the
// active spec moves on.
type Snapshot struct {
	Version      string                  `bson:"version" json:"version"`
	Weights      map[core.CategoryID]int `bson:"weights" json:"weights"`
	Allocations  map[core.CheckID]int    `bson:"allocations" json:"allocations"`
	FindingsOnly []core.CheckID          `bson:"findings_only,omitempty" json:"findingsOnly,omitempty"`
}

// IsFindingsOnly reports whether the snapshot demoted the check to
// findings-only (decision 12: values-controlled, frozen per run).
func (s Snapshot) IsFindingsOnly(id core.CheckID) bool {
	for _, c := range s.FindingsOnly {
		if c == id {
			return true
		}
	}
	return false
}

// Snapshot freezes this spec + the resolved findings-only set for one run.
func (s Spec) Snapshot(findingsOnly []core.CheckID) Snapshot {
	weights := make(map[core.CategoryID]int, len(s.Categories))
	for _, c := range s.Categories {
		weights[c.ID] = c.Weight
	}
	allocs := make(map[core.CheckID]int, len(s.Allocations))
	for id, a := range s.Allocations {
		allocs[id] = a
	}
	fo := make([]core.CheckID, len(findingsOnly))
	copy(fo, findingsOnly)
	return Snapshot{
		Version:      s.Version,
		Weights:      weights,
		Allocations:  allocs,
		FindingsOnly: fo,
	}
}

// ByVersion resolves a spec literal from the values knob
// (audit.scoring.specVersion). Unknown versions fail the boot.
func ByVersion(version string) (Spec, error) {
	switch version {
	case "v1", "":
		return SpecV1(), nil
	default:
		return Spec{}, fmt.Errorf("audit spec: unknown spec version %q", version)
	}
}

// validate is the version-independent structural check every literal must
// pass: category weights sum to 100, no duplicate categories, no non-positive
// allocation. Called from the literal constructors so a broken literal can
// never be returned.
func validate(s Spec) error {
	seen := map[core.CategoryID]bool{}
	weightSum := 0
	for _, c := range s.Categories {
		if seen[c.ID] {
			return fmt.Errorf("audit spec %s: duplicate category %q", s.Version, c.ID)
		}
		seen[c.ID] = true
		weightSum += c.Weight
	}
	if weightSum != 100 {
		return fmt.Errorf("audit spec %s: category weights sum to %d, want 100", s.Version, weightSum)
	}
	for id, a := range s.Allocations {
		if a <= 0 {
			return fmt.Errorf("audit spec %s: non-positive allocation for %q (findings-only checks are omitted from Allocations, never zeroed)", s.Version, id)
		}
	}
	return nil
}
