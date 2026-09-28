package service

import (
	"strings"
	"testing"

	"github.com/atharva-ng/crunch/internal/models"
	llmutil "github.com/atharva-ng/crunch/internal/providers/impl/llm"
	cgeDto "github.com/atharva-ng/crunch/internal/services/contentGenerationEngine/dto"
	"github.com/atharva-ng/crunch/internal/services/contentGenerationEngine/prompts"
)

// Render/omit contract for the learned style artifacts: every consumer's
// template block is conditional, so a master context without a profile renders
// no trace of the feature, and one with a profile renders the artifact text.

func renderTemplate(t *testing.T, tmpl string, data any) string {
	t.Helper()
	out, err := llmutil.NewLlmUtils().ConstructPrompt(tmpl, data)
	if err != nil {
		t.Fatalf("render template: %v", err)
	}
	return out
}

const (
	testTone      = "TONE-PROFILE-SENTINEL voice: dry, first person plural."
	testStructure = "STRUCTURE-SENTINEL hooks: blunt claim first."
)

func styledMC() *models.WebEntityMasterContext {
	return &models.WebEntityMasterContext{
		ArticleType:      "guide",
		ProposedTitle:    "T",
		ToneProfile:      testTone,
		StructurePattern: testStructure,
	}
}

func plainMC() *models.WebEntityMasterContext {
	return &models.WebEntityMasterContext{ArticleType: "guide", ProposedTitle: "T"}
}

func testKeyword() *models.Keyword {
	return &models.Keyword{Keyword: "test keyword"}
}

func TestOutlinePromptRendersAndOmitsStyleBlocks(t *testing.T) {
	with := renderTemplate(t, prompts.OutlineGenerationData, buildOutlinePromptData(styledMC(), testKeyword()))
	if !strings.Contains(with, testStructure) || !strings.Contains(with, testTone) {
		t.Fatalf("outline data prompt missing style artifacts:\n%s", with)
	}

	without := renderTemplate(t, prompts.OutlineGenerationData, buildOutlinePromptData(plainMC(), testKeyword()))
	if strings.Contains(without, "Structure pattern") || strings.Contains(without, "Heading voice hint") {
		t.Fatalf("outline data prompt leaked style blocks without a profile:\n%s", without)
	}
}

func TestArticlePromptLayersStyleProfileAboveBrandVoice(t *testing.T) {
	with := renderTemplate(t, prompts.ArticleGenerationData, buildArticlePromptData(styledMC(), testKeyword()))
	styleIdx := strings.Index(with, "=== STYLE PROFILE")
	brandIdx := strings.Index(with, "=== BRAND VOICE ===")
	if styleIdx == -1 || !strings.Contains(with, testTone) {
		t.Fatalf("article data prompt missing the STYLE PROFILE block:\n%s", with)
	}
	if brandIdx == -1 || styleIdx > brandIdx {
		t.Fatalf("STYLE PROFILE must layer above BRAND VOICE (style=%d brand=%d)", styleIdx, brandIdx)
	}

	without := renderTemplate(t, prompts.ArticleGenerationData, buildArticlePromptData(plainMC(), testKeyword()))
	if strings.Contains(without, "STYLE PROFILE") {
		t.Fatalf("article data prompt leaked the STYLE PROFILE block without a profile:\n%s", without)
	}
	if !strings.Contains(without, "=== BRAND VOICE ===") {
		t.Fatalf("article data prompt lost the BRAND VOICE section:\n%s", without)
	}
}

func TestSectionRegenPromptRendersAndOmitsTone(t *testing.T) {
	sa := &models.ScheduledArticle{Title: "T"}

	with := renderTemplate(t, prompts.SectionRegenerationData,
		buildSectionRegenPromptData(sa, styledMC(), testKeyword(), "passage", ""))
	if !strings.Contains(with, testTone) {
		t.Fatalf("section regen prompt missing the tone profile:\n%s", with)
	}

	without := renderTemplate(t, prompts.SectionRegenerationData,
		buildSectionRegenPromptData(sa, plainMC(), testKeyword(), "passage", ""))
	if strings.Contains(without, "Style profile") {
		t.Fatalf("section regen prompt leaked the style block without a profile:\n%s", without)
	}
}

func TestMetaAssetsPromptRendersAndOmitsTone(t *testing.T) {
	with := renderTemplate(t, prompts.MetaAssetsGeneration, buildMetaAssetsPromptData(styledMC(), testKeyword()))
	if !strings.Contains(with, testTone) {
		t.Fatalf("meta assets prompt missing the tone profile:\n%s", with)
	}

	without := renderTemplate(t, prompts.MetaAssetsGeneration, buildMetaAssetsPromptData(plainMC(), testKeyword()))
	if strings.Contains(without, "Style profile") {
		t.Fatalf("meta assets prompt leaked the style block without a profile:\n%s", without)
	}
}

func TestCustomImageTemplatesRenderLearnedStyle(t *testing.T) {
	learned := "- LEARNED-IMAGE-RULES pastel palette."
	data := cgeDto.ImagePromptGenerationPrompt{Keyword: "kw", ImageStyle: learned}

	thumb := renderTemplate(t, prompts.CustomThumbnailImagePrompt, data)
	if !strings.Contains(thumb, learned) || !strings.Contains(thumb, "Do not create an infographic.") {
		t.Fatalf("custom thumbnail template must render the learned rules + safety lines:\n%s", thumb)
	}
	mid := renderTemplate(t, prompts.CustomMidArticleImagePrompt, data)
	if !strings.Contains(mid, learned) || !strings.Contains(mid, "no messy infographic panels") {
		t.Fatalf("custom mid-article template must render the learned rules + safety lines:\n%s", mid)
	}
}
