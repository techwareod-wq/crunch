package service

import (
	"testing"

	sieDto "github.com/atharva-ng/crunch/internal/services/siteIntelligenceEngine/dto"
)

// testSuggestionsKeep mirrors the configured Manual.SuggestionsKeep cap; the
// merge function takes the cap as an argument, so tests pass it explicitly.
const testSuggestionsKeep = 5

func sug(keyword string, volume int, cpc float64, source string) sieDto.ManualKeywordSuggestion {
	return sieDto.ManualKeywordSuggestion{Keyword: keyword, Volume: volume, CPC: cpc, Source: source}
}

func keywordsOf(in []sieDto.ManualKeywordSuggestion) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = s.Keyword
	}
	return out
}

func TestMergeManualKeywordSuggestions_FiltersAndSorts(t *testing.T) {
	seed := "AI accounts payable automation"
	in := []sieDto.ManualKeywordSuggestion{
		sug("ap automation software", 480, 9.0, manualSuggestionSourceKeywordSuggestions),
		sug("accounts payable automation", 880, 12.4, manualSuggestionSourceKeywordSuggestions),
		sug("invoice processing automation", 590, 10.1, manualSuggestionSourceKeywordSuggestions),
		sug("ai accounts payable automation", 320, 5.0, manualSuggestionSourceKeywordSuggestions), // exact seed (case-insensitive) → dropped
		sug("", 700, 4.0, manualSuggestionSourceKeywordSuggestions),                               // empty → dropped
		sug("zero volume keyword", 0, 3.0, manualSuggestionSourceKeywordSuggestions),              // volume <= 0 → dropped
	}

	got := mergeManualKeywordSuggestions(seed, in, testSuggestionsKeep)

	want := []string{
		"accounts payable automation",   // 880
		"invoice processing automation", // 590
		"ap automation software",        // 480
	}
	gotKeywords := keywordsOf(got)
	if len(gotKeywords) != len(want) {
		t.Fatalf("got %d suggestions %v, want %d %v", len(gotKeywords), gotKeywords, len(want), want)
	}
	for i := range want {
		if gotKeywords[i] != want[i] {
			t.Errorf("position %d = %q, want %q (full: %v)", i, gotKeywords[i], want[i], gotKeywords)
		}
	}
}

func TestMergeManualKeywordSuggestions_DedupePrefersKeywordSuggestions(t *testing.T) {
	in := []sieDto.ManualKeywordSuggestion{
		sug("Invoice Automation", 300, 2.0, manualSuggestionSourceRelatedKeywords),
		sug("invoice automation", 300, 2.0, manualSuggestionSourceKeywordSuggestions), // same keyword, different case + source
	}

	got := mergeManualKeywordSuggestions("seed", in, testSuggestionsKeep)

	if len(got) != 1 {
		t.Fatalf("expected case-insensitive dedupe to collapse to 1, got %d: %v", len(got), keywordsOf(got))
	}
	if got[0].Source != manualSuggestionSourceKeywordSuggestions {
		t.Errorf("dedupe kept source %q, want %q (keyword_suggestions wins)", got[0].Source, manualSuggestionSourceKeywordSuggestions)
	}
}

func TestMergeManualKeywordSuggestions_TieBreaksOnCPC(t *testing.T) {
	in := []sieDto.ManualKeywordSuggestion{
		sug("low cpc", 500, 1.0, manualSuggestionSourceKeywordSuggestions),
		sug("high cpc", 500, 9.0, manualSuggestionSourceKeywordSuggestions),
	}

	got := mergeManualKeywordSuggestions("seed", in, testSuggestionsKeep)

	if len(got) != 2 || got[0].Keyword != "high cpc" {
		t.Fatalf("equal volume should order by CPC desc, got %v", keywordsOf(got))
	}
}

func TestMergeManualKeywordSuggestions_CapsAtKeep(t *testing.T) {
	in := []sieDto.ManualKeywordSuggestion{
		sug("a", 600, 1.0, manualSuggestionSourceKeywordSuggestions),
		sug("b", 500, 1.0, manualSuggestionSourceKeywordSuggestions),
		sug("c", 400, 1.0, manualSuggestionSourceKeywordSuggestions),
		sug("d", 300, 1.0, manualSuggestionSourceKeywordSuggestions),
		sug("e", 200, 1.0, manualSuggestionSourceKeywordSuggestions),
		sug("f", 100, 1.0, manualSuggestionSourceKeywordSuggestions),
	}

	got := mergeManualKeywordSuggestions("seed", in, testSuggestionsKeep)

	if len(got) != testSuggestionsKeep {
		t.Fatalf("expected cap at %d, got %d: %v", testSuggestionsKeep, len(got), keywordsOf(got))
	}
	if got[len(got)-1].Keyword != "e" {
		t.Errorf("cap should keep the highest-volume %d, dropping lowest; last = %q want %q", testSuggestionsKeep, got[len(got)-1].Keyword, "e")
	}
}

func TestMergeManualKeywordSuggestions_Empty(t *testing.T) {
	if got := mergeManualKeywordSuggestions("seed", nil, testSuggestionsKeep); len(got) != 0 {
		t.Fatalf("nil input should yield empty result, got %v", keywordsOf(got))
	}
}
