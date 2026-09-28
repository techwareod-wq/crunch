// Package core is the audit engine's shared vocabulary — the leaf package
// every other audit package (and models) imports. It must never import
// anything audit-internal, and nothing from models/config, so the whole
// graph stays acyclic: core ← artifacts ← {collectors, checks} ← {spec,
// engine} ← service ← audit root.
package core

// CheckID identifies one audit metric, e.g. "technical.broken_links" or
// "content.llm_judgment".
type CheckID string

// CategoryID identifies one scored report category.
type CategoryID string

// The eight SpecV1 categories (scope §V1 build 6).
const (
	CategoryTechnical   CategoryID = "technical"
	CategoryContentEEAT CategoryID = "content_eeat"
	CategoryOnPage      CategoryID = "on_page"
	CategorySchema      CategoryID = "schema"
	CategoryPerformance CategoryID = "performance"
	CategoryAISearch    CategoryID = "ai_search"
	CategoryImages      CategoryID = "images"
	CategoryBacklinks   CategoryID = "backlinks"
)

// CollectorID identifies one data-acquisition strategy.
type CollectorID string

// Kind names an artifact type on the blackboard (see package artifacts).
type Kind string

// RunKind separates the two audit entry points.
type RunKind string

const (
	RunKindTenant RunKind = "tenant"
	RunKindLead   RunKind = "lead"
)

// Severity is the five-level finding scale (scope §5 — five levels is a
// fidelity must; FAQPage findings are always Info).
type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityHigh     Severity = "high"
	SeverityMedium   Severity = "medium"
	SeverityLow      Severity = "low"
	SeverityInfo     Severity = "info"
)

// SeverityRank orders severities for bucketing/sorting (critical first).
func SeverityRank(s Severity) int {
	switch s {
	case SeverityCritical:
		return 0
	case SeverityHigh:
		return 1
	case SeverityMedium:
		return 2
	case SeverityLow:
		return 3
	default:
		return 4
	}
}

// Finding is the atomic report unit. Falsifiability is mandatory IP from the
// reference: every recommendation carries a "how would we know this failed"
// check, so a reader can verify a fix landed instead of trusting us.
type Finding struct {
	CheckID        CheckID  `bson:"check_id"        json:"checkId"`
	Severity       Severity `bson:"severity"        json:"severity"`
	Title          string   `bson:"title"           json:"title"`
	Detail         string   `bson:"detail"          json:"detail"`
	Pages          []string `bson:"pages,omitempty" json:"pages,omitempty"` // affected URLs, capped by the emitting check
	Recommendation string   `bson:"recommendation"  json:"recommendation"`
	Falsifiability string   `bson:"falsifiability"  json:"falsifiability"`
	// Snippet is an optional exact artifact — a copy-pasteable code/config
	// block or replacement copy implementing the recommendation (rendered
	// monospace by the FE). Emitted only when the exact change is derivable;
	// never a paraphrase of Recommendation.
	Snippet string `bson:"snippet,omitempty" json:"snippet,omitempty"`
}

// Score is a check's contribution: the Earned/Possible ratio (∈ [0,1]) is
// applied to the spec's allocation for the check. Possible == 0 is a
// boot-caught bug, not a runtime state.
type Score struct {
	Earned   float64 `bson:"earned"   json:"earned"`
	Possible float64 `bson:"possible" json:"possible"`
}

// Ratio returns Earned/Possible clamped to [0,1].
func (s Score) Ratio() float64 {
	if s.Possible <= 0 {
		return 0
	}
	r := s.Earned / s.Possible
	if r < 0 {
		return 0
	}
	if r > 1 {
		return 1
	}
	return r
}

// CheckResult is what one check run produces. Score == nil means the check is
// intrinsically findings-only (e.g. content.parasite_markers). Evidence is
// structured data forwarded into the Content LLM rubric prompt (decision 12:
// deterministic scorer numbers feed the judgment call as hard evidence).
type CheckResult struct {
	Score    *Score
	Findings []Finding
	Evidence map[string]any
}
