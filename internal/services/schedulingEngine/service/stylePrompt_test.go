package service

import (
	"strings"
	"testing"

	"github.com/atharva-ng/crunch/internal/models"
	llmutil "github.com/atharva-ng/crunch/internal/providers/impl/llm"
	seDto "github.com/atharva-ng/crunch/internal/services/schedulingEngine/dto"
	"github.com/atharva-ng/crunch/internal/services/schedulingEngine/prompts"
)

// Render/omit contract for the learned tone profile in the title prompts:
// the block is conditional, so entities without a profile render no trace.

const titleTestTone = "TONE-PROFILE-SENTINEL dry, first person plural."

func renderSEPrompt(t *testing.T, tmpl string, data any) string {
	t.Helper()
	out, err := llmutil.NewLlmUtils().ConstructPrompt(tmpl, data)
	if err != nil {
		t.Fatalf("render template: %v", err)
	}
	return out
}

func TestTitleTemplatesRenderAndOmitToneProfile(t *testing.T) {
	cases := []struct {
		name    string
		tmpl    string
		with    any
		without any
	}{
		{
			name:    "titleGeneration",
			tmpl:    prompts.TitleGeneration,
			with:    seDto.TitleGenerationPrompt{Keyword: "kw", ToneProfile: titleTestTone},
			without: seDto.TitleGenerationPrompt{Keyword: "kw"},
		},
		{
			name:    "titleSuggestions",
			tmpl:    prompts.TitleSuggestions,
			with:    seDto.TitleSuggestionsPrompt{Keyword: "kw", ToneProfile: titleTestTone},
			without: seDto.TitleSuggestionsPrompt{Keyword: "kw"},
		},
		{
			name:    "retitleForType",
			tmpl:    prompts.RetitleForType,
			with:    seDto.RetitlePrompt{Keyword: "kw", ToneProfile: titleTestTone},
			without: seDto.RetitlePrompt{Keyword: "kw"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			with := renderSEPrompt(t, tc.tmpl, tc.with)
			if !strings.Contains(with, titleTestTone) {
				t.Fatalf("%s missing the tone profile:\n%s", tc.name, with)
			}
			without := renderSEPrompt(t, tc.tmpl, tc.without)
			if strings.Contains(without, "Style profile") {
				t.Fatalf("%s leaked the style block without a profile:\n%s", tc.name, without)
			}
		})
	}
}

const titleTestPattern = "TITLE-PATTERN-SENTINEL Title Case, colon payloads, no hype."

func TestTitleTemplatesPreferTitlePatternOverTone(t *testing.T) {
	cases := []struct {
		name string
		tmpl string
		both any
	}{
		{"titleGeneration", prompts.TitleGeneration,
			seDto.TitleGenerationPrompt{Keyword: "kw", ToneProfile: titleTestTone, TitlePattern: titleTestPattern}},
		{"titleSuggestions", prompts.TitleSuggestions,
			seDto.TitleSuggestionsPrompt{Keyword: "kw", ToneProfile: titleTestTone, TitlePattern: titleTestPattern}},
		{"retitleForType", prompts.RetitleForType,
			seDto.RetitlePrompt{Keyword: "kw", ToneProfile: titleTestTone, TitlePattern: titleTestPattern}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := renderSEPrompt(t, tc.tmpl, tc.both)
			// The title pattern is the proper context for a title call — when
			// both artifacts exist, the pattern renders and the body-tone
			// fallback stays out.
			if !strings.Contains(out, titleTestPattern) {
				t.Fatalf("%s missing the title pattern:\n%s", tc.name, out)
			}
			if strings.Contains(out, titleTestTone) {
				t.Fatalf("%s rendered the tone fallback alongside the title pattern:\n%s", tc.name, out)
			}
		})
	}
}

func TestStyleTitlePatternNilSafety(t *testing.T) {
	if got := styleTitlePattern(nil); got != "" {
		t.Fatalf("styleTitlePattern(nil) = %q, want empty", got)
	}
	pattern := "learned pattern"
	we := &models.WebEntity{StyleReplication: &models.StyleReplication{TitlePattern: &pattern}}
	if got := styleTitlePattern(we); got != pattern {
		t.Fatalf("styleTitlePattern(with profile) = %q, want %q", got, pattern)
	}
}

func TestStyleToneNilSafety(t *testing.T) {
	if got := styleTone(nil); got != "" {
		t.Fatalf("styleTone(nil) = %q, want empty", got)
	}
	if got := styleTone(&models.WebEntity{}); got != "" {
		t.Fatalf("styleTone(no profile) = %q, want empty", got)
	}
	tone := "learned tone"
	we := &models.WebEntity{StyleReplication: &models.StyleReplication{ToneProfile: &tone}}
	if got := styleTone(we); got != tone {
		t.Fatalf("styleTone(with profile) = %q, want %q", got, tone)
	}
}
