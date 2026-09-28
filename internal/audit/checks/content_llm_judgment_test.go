package checks

import (
	"strings"
	"testing"

	"github.com/atharva-ng/crunch/internal/audit/artifacts"
	"github.com/atharva-ng/crunch/internal/audit/core"
)

func judgmentInput(t *testing.T) Input {
	t.Helper()
	return testInput(t, map[core.Kind]any{
		artifacts.KindHTMLDeep: &artifacts.HTMLDeepArtifact{Pages: []artifacts.DeepPage{{
			URL: "https://example.com/post", Fetched: true, Title: "A Post",
			ContentExcerpt: "Some real body text about the topic with substance.",
		}}},
	})
}

// TestBuildPrompt_ContractInvariants asserts the ported IP survives in the
// rendered prompt: evidence injection, the locked E-E-A-T sub-weights, and
// the negative/falsifiability corpus.
func TestBuildPrompt_ContractInvariants(t *testing.T) {
	check := contentLLMJudgment{}
	evidence := map[core.CheckID]map[string]any{
		"content.filler_ai_patterns": {"pages": map[string]any{"https://example.com/post": map[string]any{"fillerPhrases": 7}}},
	}
	prompt, err := check.BuildPrompt(judgmentInput(t), evidence)
	if err != nil {
		t.Fatalf("BuildPrompt: %v", err)
	}

	for _, want := range []string{
		// Evidence injection (decision 12).
		"fillerPhrases", "content.filler_ai_patterns",
		// Locked sub-weights: Trust 30 / Expertise 25 / Authority 25 /
		// Experience 20 — never an equal split.
		"weight 30", "weight 25", "weight 20",
		"trust×0.30 + expertise×0.25 + authority×0.25 + experience×0.20",
		// Negative corpus.
		"no \"FID\"", "INP", "CWV 2.0", "llms.txt has zero proven ranking weight",
		"134–167 words",
		// Heuristics honesty + mandatory falsifiability.
		"heuristics", "falsifiability",
		// Page material present.
		"https://example.com/post",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt is missing locked content %q", want)
		}
	}
}

func TestBuildPrompt_NoFetchedPages(t *testing.T) {
	in := testInput(t, map[core.Kind]any{
		artifacts.KindHTMLDeep: &artifacts.HTMLDeepArtifact{Pages: []artifacts.DeepPage{{
			URL: "https://example.com/blocked", Fetched: false, BlockedReason: "blocked",
		}}},
	})
	if _, err := (contentLLMJudgment{}).BuildPrompt(in, nil); err == nil {
		t.Fatal("BuildPrompt with zero fetched pages must error (judge stage soft-fails it)")
	}
}

func TestParseResult(t *testing.T) {
	check := contentLLMJudgment{}

	result, err := check.ParseResult(`{
		"score": 62,
		"subScores": {"trust": 70, "expertise": 60, "authority": 55, "experience": 60},
		"findings": [
			{"severity": "high", "title": "No original data", "detail": "d", "recommendation": "r", "falsifiability": "f"},
			{"severity": "made_up", "title": "Weird severity", "detail": "d", "recommendation": "r", "falsifiability": ""},
			{"severity": "low", "title": "", "detail": "dropped — no title"}
		]
	}`)
	if err != nil {
		t.Fatalf("ParseResult: %v", err)
	}
	if result.Score == nil || result.Score.Earned != 62 {
		t.Errorf("score = %+v, want earned 62", result.Score)
	}
	if len(result.Findings) != 2 {
		t.Fatalf("findings = %d, want 2 (empty-title dropped)", len(result.Findings))
	}
	if result.Findings[1].Severity != core.SeverityInfo {
		t.Errorf("invented severity must degrade to info, got %s", result.Findings[1].Severity)
	}
	if result.Findings[1].Falsifiability == "" {
		t.Error("missing falsifiability must receive the default, never stay empty")
	}

	// Malformed / out-of-range payloads fail loudly (the judge stage then
	// soft-fails the whole check).
	if _, err := check.ParseResult(`not json`); err == nil {
		t.Error("malformed JSON must error")
	}
	if _, err := check.ParseResult(`{"score": 250, "findings": []}`); err == nil {
		t.Error("out-of-range score must error")
	}
}
