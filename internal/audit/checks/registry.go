package checks

import (
	"fmt"

	"github.com/atharva-ng/crunch/internal/audit/collectors"
	"github.com/atharva-ng/crunch/internal/audit/core"
	"github.com/atharva-ng/crunch/internal/audit/spec"
	"github.com/atharva-ng/crunch/internal/config"
)

// Registry is the frozen check set + the resolved findings-only demotions.
// Built once at boot; never mutated after.
type Registry struct {
	checks       []Check
	byID         map[core.CheckID]Check
	findingsOnly []core.CheckID
}

// All returns every registered check in registration order.
func (r *Registry) All() []Check {
	return r.checks
}

// ByID resolves one check.
func (r *Registry) ByID(id core.CheckID) (Check, bool) {
	c, ok := r.byID[id]
	return c, ok
}

// Deterministic returns the KindDeterministic checks (AUDIT_SCORE's work
// list) in registration order.
func (r *Registry) Deterministic() []Check {
	var out []Check
	for _, c := range r.checks {
		if c.Kind() == KindDeterministic {
			out = append(out, c)
		}
	}
	return out
}

// LLMChecks returns the KindLLMJudgment checks (AUDIT_JUDGE_CONTENT's work
// list — V1: exactly one).
func (r *Registry) LLMChecks() []LLMCheck {
	var out []LLMCheck
	for _, c := range r.checks {
		if c.Kind() == KindLLMJudgment {
			// Boot validation guarantees the assertion holds.
			out = append(out, c.(LLMCheck))
		}
	}
	return out
}

// FindingsOnly is the resolved values-demotion set, in values order — what
// gets frozen onto each run's spec snapshot (decision 12).
func (r *Registry) FindingsOnly() []core.CheckID {
	return r.findingsOnly
}

// staticChecks is THE registration list — the one line a new check adds
// (extension recipe Tier A step 2).
func staticChecks() []Check {
	return []Check{
		// technical
		&technicalBrokenLinks{},
		&technicalRedirectsStatus{},
		&technicalCanonicalization{},
		&technicalIndexabilityRobots{},
		&technicalHTTPS{},
		&technicalSitemap{},
		&technicalIndexNow{},
		&technicalSecurityHeaders{},
		&technicalSoft404{},
		&technicalSiteFiles{},
		// on_page
		&onpageTitles{},
		&onpageMetas{},
		&onpageHeadings{},
		&onpageDuplicateContent{},
		&onpageSXOMismatch{},
		&onpageProgrammaticGates{},
		&onpageHeadHygiene{},
		// content_eeat
		&contentReadability{},
		&contentSubstance{},
		&contentKeywordDensity{},
		&contentInternalLinking{},
		&contentFreshness{},
		&contentTrustSignals{},
		&contentFillerAIPatterns{},
		&contentScaledPatterns{},
		&contentParasiteMarkers{},
		&contentLLMJudgment{},
		// schema
		&schemaValidity{},
		&schemaCoverage{},
		&schemaDeprecations{},
		// performance
		&performanceCWV{},
		&performanceSpeculationBfcache{},
		&performancePSIOpportunities{},
		// ai_search
		&aisearchAnswerBlocks{},
		&aisearchStructure{},
		&aisearchAuthorityProxies{},
		&aisearchCrawlerAccess{},
		&aisearchAgentUX{},
		&aisearchMultimodal{},
		&aisearchBrandFootprint{},
		&aisearchRenderedParity{},
		// images
		&imagesAltSemantics{},
		&imagesSizing{},
		// backlinks
		&backlinksDomainRating{},
		&backlinksProfileSummary{},
	}
}

// BuildCheckRegistry wires everything at boot and fails the process on any
// inconsistency (the cron-registry posture — a broken graph must never
// serve):
//   - duplicate CheckIDs
//   - a Category() not present in the active spec
//   - a Requires() kind with no producing collector
//   - a score-feeding check missing a spec allocation, or an allocation for
//     a nonexistent check
//   - per-category score-feeding allocations not summing to 100
//   - audit.scoring.findingsOnly naming an unknown CheckID
//   - a KindLLMJudgment check not implementing LLMCheck
func BuildCheckRegistry(sp spec.Spec, collectorList []collectors.Collector, vals config.AuditValues) (*Registry, error) {
	list := staticChecks()
	produced := collectors.ProducedKinds(collectorList)

	byID := map[core.CheckID]Check{}
	for _, c := range list {
		if _, dup := byID[c.ID()]; dup {
			return nil, fmt.Errorf("audit checks: duplicate check id %q", c.ID())
		}
		byID[c.ID()] = c

		if !sp.HasCategory(c.Category()) {
			return nil, fmt.Errorf("audit checks: check %q category %q not in spec %s", c.ID(), c.Category(), sp.Version)
		}
		for _, k := range c.Requires() {
			if !produced[k] {
				return nil, fmt.Errorf("audit checks: check %q requires kind %q with no producing collector", c.ID(), k)
			}
		}
		if c.Kind() == KindLLMJudgment {
			if _, ok := c.(LLMCheck); !ok {
				return nil, fmt.Errorf("audit checks: check %q is KindLLMJudgment but does not implement LLMCheck", c.ID())
			}
		}
	}

	// Every allocation must reference a registered check.
	for id := range sp.Allocations {
		if _, ok := byID[id]; !ok {
			return nil, fmt.Errorf("audit checks: spec %s allocates points to nonexistent check %q", sp.Version, id)
		}
	}

	// Resolve the values findings-only demotions (decision 12).
	var findingsOnly []core.CheckID
	demoted := map[core.CheckID]bool{}
	for _, raw := range vals.Scoring.FindingsOnly {
		id := core.CheckID(raw)
		if _, ok := byID[id]; !ok {
			return nil, fmt.Errorf("audit checks: audit.scoring.findingsOnly names unknown check %q", raw)
		}
		if !demoted[id] {
			demoted[id] = true
			findingsOnly = append(findingsOnly, id)
		}
	}

	// Per-category allocation sums: every check that CAN feed a score (has an
	// allocation) must land its category at exactly 100. Intrinsically
	// findings-only checks (no allocation) are exempt; values demotions
	// deliberately do NOT change the sum — the engine renormalizes at run
	// time so a demotion stays a values-only change.
	categorySums := map[core.CategoryID]int{}
	for _, c := range list {
		alloc, ok := sp.Allocations[c.ID()]
		if !ok {
			continue
		}
		categorySums[c.Category()] += alloc
	}
	for _, cat := range sp.Categories {
		if sum, ok := categorySums[cat.ID]; ok && sum != 100 {
			return nil, fmt.Errorf("audit checks: category %q allocations sum to %d, want 100", cat.ID, sum)
		}
	}

	return &Registry{checks: list, byID: byID, findingsOnly: findingsOnly}, nil
}
