package service

import (
	"strings"
	"testing"

	cgeDto "github.com/atharva-ng/crunch/internal/services/contentGenerationEngine/dto"
	"github.com/atharva-ng/crunch/internal/services/contentGenerationEngine/prompts"
)

func TestExtractFirstParagraph(t *testing.T) {
	content := strings.Join([]string{
		"# Article Title",
		"",
		"{{IMAGE_THUMBNAIL}}",
		"",
		"This is the article introduction that should be returned.",
		"",
		"## A Section",
		"More body text.",
	}, "\n")

	got := extractFirstParagraph(content)
	want := "This is the article introduction that should be returned."
	if got != want {
		t.Fatalf("extractFirstParagraph() = %q, want %q", got, want)
	}
}

func TestExtractMidArticleSection_UsesH2AfterPlaceholder(t *testing.T) {
	content := strings.Join([]string{
		"## Earlier section",
		"This earlier section should be ignored.",
		"",
		"{{IMAGE_MID_ARTICLE}}",
		"## Benchmarks that show weak ad creative",
		"Hook rate below 20% usually means the opening does not stop the scroll.",
		"",
		"## Next section",
		"Unrelated content.",
	}, "\n")

	title, body := extractMidArticleSection(content, 1500)
	if title != "Benchmarks that show weak ad creative" {
		t.Fatalf("sectionTitle = %q, want %q", title, "Benchmarks that show weak ad creative")
	}
	if !strings.Contains(body, "Hook rate below 20%") {
		t.Fatalf("sectionContent = %q, want it to contain the section paragraph", body)
	}
	if strings.Contains(body, "earlier section") {
		t.Fatalf("sectionContent = %q, should not include content before the placeholder", body)
	}
}

func TestExtractMidArticleSection_IncludesBulletsAndMetrics(t *testing.T) {
	content := strings.Join([]string{
		"{{IMAGE_MID_ARTICLE}}",
		"## Benchmarks that show weak ad creative",
		"Hook rate below 20% usually means the opening does not stop the scroll.",
		"- Hold rate below 40% means the story loses viewers early.",
		"- Frequency above 2.5 can signal fatigue.",
		"",
		"## Next section",
		"Should not appear.",
	}, "\n")

	_, body := extractMidArticleSection(content, 1500)
	for _, want := range []string{
		"Hook rate below 20%",
		"Hold rate below 40%",
		"Frequency above 2.5",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("sectionContent = %q, want it to contain %q", body, want)
		}
	}
}

func TestExtractMidArticleSection_StopsAtNextH2(t *testing.T) {
	content := strings.Join([]string{
		"{{IMAGE_MID_ARTICLE}}",
		"## Target section",
		"Body of the target section.",
		"## Next section",
		"This belongs to the next section and must be excluded.",
	}, "\n")

	_, body := extractMidArticleSection(content, 1500)
	if !strings.Contains(body, "Body of the target section.") {
		t.Fatalf("sectionContent = %q, want it to contain the target body", body)
	}
	if strings.Contains(body, "next section") {
		t.Fatalf("sectionContent = %q, should stop before the next H2", body)
	}
}

func TestExtractMidArticleSection_NoPlaceholder(t *testing.T) {
	content := "## A section\nSome body text.\n"
	title, body := extractMidArticleSection(content, 1500)
	if title != "" || body != "" {
		t.Fatalf("extractMidArticleSection() = (%q, %q), want empty strings when no placeholder", title, body)
	}
}

func TestBuildImageAltText_UnderLimit(t *testing.T) {
	longTitle := strings.Repeat("very long section heading ", 20)
	cases := []struct {
		name     string
		p        cgeDto.ImagePromptGenerationPrompt
		position string
	}{
		{
			name:     "thumbnail with long H1",
			p:        cgeDto.ImagePromptGenerationPrompt{H1: longTitle},
			position: "thumbnail",
		},
		{
			name:     "mid-article with long section title",
			p:        cgeDto.ImagePromptGenerationPrompt{SectionTitle: longTitle},
			position: "mid-article",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			alt := buildImageAltText(tc.p, tc.position, 125)
			if alt == "" {
				t.Fatalf("buildImageAltText() returned empty alt text")
			}
			if len(alt) > 125 {
				t.Fatalf("buildImageAltText() = %d chars, want <= 125", len(alt))
			}
		})
	}
}

func TestBuildImageAltText_Fallbacks(t *testing.T) {
	if got := buildImageAltText(cgeDto.ImagePromptGenerationPrompt{}, "thumbnail", 125); got != "Article thumbnail illustration" {
		t.Fatalf("thumbnail fallback = %q", got)
	}
	if got := buildImageAltText(cgeDto.ImagePromptGenerationPrompt{}, "mid-article", 125); got != "Article section illustration" {
		t.Fatalf("mid-article fallback = %q", got)
	}
}

func TestImagePromptTemplateForPosition(t *testing.T) {
	if got := imagePromptTemplateForPosition("thumbnail", "", "", ""); !strings.Contains(got, "thumbnail") {
		t.Fatalf("thumbnail template did not resolve to the thumbnail prompt")
	}
	if got := imagePromptTemplateForPosition("mid-article", "", "", ""); !strings.Contains(got, "mid-article") {
		t.Fatalf("mid-article template did not resolve to the mid-article prompt")
	}
	// Unknown positions fall back to the mid-article template.
	if imagePromptTemplateForPosition("weird", "", "", "") != imagePromptTemplateForPosition("mid-article", "", "", "") {
		t.Fatalf("unknown position did not fall back to mid-article template")
	}
}

func TestImagePromptTemplateForPosition_StyleSelectsThumbnailTemplate(t *testing.T) {
	// The thumbnail position picks the template for the resolved style.
	editorial := imagePromptTemplateForPosition("thumbnail", "editorial", "", "")
	blueprint := imagePromptTemplateForPosition("thumbnail", "blueprint", "", "")
	if editorial == blueprint {
		t.Fatalf("editorial and blueprint should resolve to different thumbnail templates")
	}
	if editorial != prompts.ThumbnailImagePrompt {
		t.Fatalf("editorial style did not resolve to the editorial thumbnail template")
	}
	if blueprint != prompts.ThumbnailImagePromptBlueprint {
		t.Fatalf("blueprint style did not resolve to the blueprint thumbnail template")
	}
	// Empty / unknown style falls back to the default thumbnail template.
	if got := imagePromptTemplateForPosition("thumbnail", "nope", "", ""); got != prompts.ThumbnailImagePrompt {
		t.Fatalf("unknown style did not fall back to the default thumbnail template")
	}
}

func TestImagePromptTemplateForPosition_MidArticleIgnoresThumbnailStyle(t *testing.T) {
	// Mid-article never keys on the thumbnail-style ID or the thumbnail's
	// learned prompt — only its own learned prompt.
	base := imagePromptTemplateForPosition("mid-article", "", "", "")
	if got := imagePromptTemplateForPosition("mid-article", "blueprint", "- thumb rules", ""); got != base {
		t.Fatalf("mid-article template must not depend on thumbnail styling")
	}
}

func TestImagePromptTemplateForPosition_LearnedStyle(t *testing.T) {
	thumbLearned := "- Pastel thumbnail palette."
	midLearned := "- Flat mid-article diagrams."
	// Each position keys on ITS OWN learned prompt.
	if got := imagePromptTemplateForPosition("thumbnail", "", thumbLearned, midLearned); got != prompts.CustomThumbnailImagePrompt {
		t.Fatalf("learned thumbnail style did not select the custom thumbnail template")
	}
	if got := imagePromptTemplateForPosition("mid-article", "", thumbLearned, midLearned); got != prompts.CustomMidArticleImagePrompt {
		t.Fatalf("learned mid-article style did not select the custom mid-article template")
	}
	// One position learned, the other not → only the learned one goes custom.
	if got := imagePromptTemplateForPosition("thumbnail", "editorial", "", midLearned); got != prompts.ThumbnailImagePrompt {
		t.Fatalf("thumbnail without a learned prompt must keep the catalog template")
	}
	if got := imagePromptTemplateForPosition("mid-article", "", thumbLearned, ""); got != prompts.MidArticleImagePrompt {
		t.Fatalf("mid-article without a learned prompt must keep the static template")
	}
	// An explicit catalog pick wins the thumbnail slot over the learned style.
	if got := imagePromptTemplateForPosition("thumbnail", "blueprint", thumbLearned, midLearned); got != prompts.ThumbnailImagePromptBlueprint {
		t.Fatalf("explicit pick did not beat the learned style for the thumbnail")
	}
	// The custom templates render the learned rules into the Style-rules slot
	// and keep the fixed safety lines.
	if !strings.Contains(prompts.CustomThumbnailImagePrompt, "{{.ImageStyle}}") ||
		!strings.Contains(prompts.CustomMidArticleImagePrompt, "{{.ImageStyle}}") {
		t.Fatalf("custom templates must render {{.ImageStyle}}")
	}
	// Thumbnails stay strictly text-free; mid-article mirrors the catalog
	// template's contract — readable text/labels allowed, slop banned.
	if !strings.Contains(prompts.CustomThumbnailImagePrompt, "No text, no labels, no logos") ||
		!strings.Contains(prompts.CustomThumbnailImagePrompt, "Do not create an infographic.") {
		t.Fatalf("custom thumbnail template lost its fixed safety lines")
	}
	if !strings.Contains(prompts.CustomMidArticleImagePrompt, "no messy infographic panels") ||
		strings.Contains(prompts.CustomMidArticleImagePrompt, "No text, no labels, no logos") {
		t.Fatalf("custom mid-article template must allow readable text and keep the AI-slop guard")
	}
}
