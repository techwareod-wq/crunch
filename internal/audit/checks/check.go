// Package checks holds the audit's metric strategies. Every metric —
// deterministic threshold check or LLM judgment — is one interchangeable
// strategy behind the Check interface; the engine doesn't know what any
// check does. Adding a metric = one new file + one registry line (extension
// recipe Tier A).
package checks

import (
	"context"

	"github.com/atharva-ng/crunch/internal/audit/artifacts"
	"github.com/atharva-ng/crunch/internal/audit/core"
	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/models"
)

// CheckKind selects which pipeline stage runs the check.
type CheckKind int

const (
	// KindDeterministic checks run in AUDIT_SCORE.
	KindDeterministic CheckKind = iota
	// KindLLMJudgment checks run in AUDIT_JUDGE_CONTENT via the LLMCheck
	// template methods.
	KindLLMJudgment
)

// Input is everything a check may read. Run and Values are read-only.
type Input struct {
	Bundle *artifacts.Bundle
	Run    *models.AuditRun
	Values config.AuditValues
}

// Check is one audit metric.
type Check interface {
	ID() core.CheckID
	Category() core.CategoryID
	Kind() CheckKind
	// Requires declares artifact dependencies — validated at boot against
	// the collector registry; missing at runtime ⇒ the engine skips the
	// check and emits an Info constraint.
	Requires() []core.Kind
	Run(ctx context.Context, in Input) (core.CheckResult, error)
}

// LLMCheck is the Template Method for judgment checks: the judge stage owns
// the actual Anthropic call (model selection, retries, token tracking); the
// check owns only prompt construction and response parsing. Evidence from
// deterministic sibling checks in the same category is injected (decision
// 12: parasite markers and content-scorer numbers feed the rubric as hard
// evidence).
type LLMCheck interface {
	Check
	BuildPrompt(in Input, evidence map[core.CheckID]map[string]any) (string, error)
	ParseResult(raw string) (core.CheckResult, error)
}
