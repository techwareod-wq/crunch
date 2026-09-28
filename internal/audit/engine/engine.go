// Package engine is the deterministic scoring core: it runs the registered
// deterministic checks over the artifact bundle, aggregates category/overall
// scores per the run's spec snapshot, and code-buckets the action plan. Same
// artifacts + same snapshot ⇒ same score, everywhere except the two LLM
// calls (decision 17 — what the V2 drift/compare feature depends on). The
// engine iterates the spec and the registry, never a hardcoded check or
// category list.
package engine

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"

	"github.com/atharva-ng/crunch/internal/audit/artifacts"
	"github.com/atharva-ng/crunch/internal/audit/checks"
	"github.com/atharva-ng/crunch/internal/audit/core"
	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/util/log"
)

// RunDeterministic executes every KindDeterministic check whose required
// artifacts are present. Checks with missing requirements are recorded as
// skipped with an Info constraint (§8.3); a check that errors degrades the
// same way — one broken check must never wedge the report.
func RunDeterministic(ctx context.Context, bundle *artifacts.Bundle, registry *checks.Registry,
	run *models.AuditRun, values config.AuditValues) (outcomes []models.AuditCheckOutcome, constraints []core.Finding) {

	in := checks.Input{Bundle: bundle, Run: run, Values: values}
	for _, c := range registry.Deterministic() {
		if !bundle.HasAll(c.Requires()) {
			outcomes = append(outcomes, models.AuditCheckOutcome{
				CheckID:  string(c.ID()),
				Category: string(c.Category()),
				Skipped:  true,
			})
			constraints = append(constraints, skipConstraint(c.ID(), "its required data was not collected"))
			continue
		}
		result, err := c.Run(ctx, in)
		if err != nil {
			log.Error("audit engine: check failed, skipping", "check", c.ID(), "error", err)
			outcomes = append(outcomes, models.AuditCheckOutcome{
				CheckID:  string(c.ID()),
				Category: string(c.Category()),
				Skipped:  true,
			})
			constraints = append(constraints, skipConstraint(c.ID(), "the check errored during evaluation"))
			continue
		}
		outcomes = append(outcomes, OutcomeFromResult(c.ID(), c.Category(), result))
	}
	return outcomes, constraints
}

// OutcomeFromResult converts a CheckResult into its persisted outcome form.
func OutcomeFromResult(id core.CheckID, category core.CategoryID, result core.CheckResult) models.AuditCheckOutcome {
	out := models.AuditCheckOutcome{
		CheckID:  string(id),
		Category: string(category),
		Findings: result.Findings,
	}
	if result.Score != nil {
		out.HasScore = true
		out.Earned = result.Score.Earned
		out.Possible = result.Score.Possible
	}
	if len(result.Evidence) > 0 {
		out.Evidence = bson.M(result.Evidence)
	}
	return out
}

// EvidenceByCheck collects the persisted evidence maps keyed by CheckID —
// the decision-12 injection into the LLM rubric prompt.
func EvidenceByCheck(outcomes []models.AuditCheckOutcome) map[core.CheckID]map[string]any {
	out := map[core.CheckID]map[string]any{}
	for _, o := range outcomes {
		if len(o.Evidence) == 0 {
			continue
		}
		out[core.CheckID(o.CheckID)] = map[string]any(o.Evidence)
	}
	return out
}

func skipConstraint(id core.CheckID, why string) core.Finding {
	return core.Finding{
		CheckID:        id,
		Severity:       core.SeverityInfo,
		Title:          fmt.Sprintf("Check %s was not assessed", id),
		Detail:         fmt.Sprintf("%s was skipped because %s; its points renormalize away rather than counting against the site.", id, why),
		Recommendation: "None — this is a transparency note, not a defect.",
		Falsifiability: "A later audit with the data available assesses this check.",
	}
}
