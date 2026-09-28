package service

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/models"
	cge "github.com/atharva-ng/crunch/internal/services/contentGenerationEngine"
	cgeDto "github.com/atharva-ng/crunch/internal/services/contentGenerationEngine/dto"
	"github.com/atharva-ng/crunch/internal/services/contentGenerationEngine/prompts"
	commonutils "github.com/atharva-ng/crunch/internal/utils"
)

func (s *contentGenerationEngineService) HandleOutlineGeneration(ctx context.Context, userId string, payload cge.CGEStandardPayload) error {
	masterContextID := payload.WebEntityMasterContextID

	mc, kw, err := s.loadMasterContextAndKeyword(ctx, masterContextID)
	if err != nil {
		return fmt.Errorf("outline generation: %w", err)
	}

	promptData := buildOutlinePromptData(mc, kw)

	dataPrompt, err := s.LLM.Utils.ConstructPrompt(prompts.OutlineGenerationData, promptData)
	if err != nil {
		return fmt.Errorf("outline generation: construct prompt: %w", err)
	}

	resp, err := s.LLM.Anthropic.Prompt(ctx, dto.PromptRequest{
		Messages: []dto.Message{
			{Role: "user", Content: prompts.OutlineGenerationInstructions, Cache: true},
			{Role: "user", Content: dataPrompt},
		},
		Model:     dto.AnthropicSonnet46,
		MaxTokens: s.LLM.DefaultMaxTokens,
	})
	if err != nil {
		return fmt.Errorf("outline generation: LLM call: %w", err)
	}

	cleaned, err := s.LLM.Utils.CleanLLMResponse(resp.Content)
	if err != nil {
		return fmt.Errorf("outline generation: clean response: %w", err)
	}

	statusProcessing := models.CGEOutlineGenerated
	if err := s.store.UpdateWebEntityMasterContext(ctx, masterContextID, models.WEMCUpdateReq{
		Outline: &models.CGEOutline{
			RawJSON: cleaned,
			URLSlug: extractOutlineSlug(cleaned, s.values.SlugMaxLen),
		},
		Status: &statusProcessing,
	}); err != nil {
		return fmt.Errorf("outline generation: save outline: %w", err)
	}

	return s.dispatcher.Dispatch(ctx, string(cge.ProcessCGEArticleGeneration), userId, cge.CGEStandardPayload{
		WebEntityMasterContextID: masterContextID,
	})
}

func buildOutlinePromptData(mc *models.WebEntityMasterContext, kw *models.Keyword) cgeDto.OutlineGenerationPrompt {
	p := cgeDto.OutlineGenerationPrompt{
		Keyword:          kw.Keyword,
		ArticleType:      mc.ArticleType,
		ProposedTitle:    mc.ProposedTitle,
		TargetWordCount:  mc.TargetWordCount,
		Funnel:           string(kw.Funnel),
		StructurePattern: mc.StructurePattern,
		ToneProfile:      mc.ToneProfile,
	}

	if len(mc.SecondaryKeywords) > 0 {
		p.SecondaryKeywords = strings.Join(mc.SecondaryKeywords, ", ")
	}

	if bc := mc.BusinessContext; bc != nil {
		p.BusinessName = commonutils.Deref(bc.BusinessName)
		p.ProductType = commonutils.Deref(bc.ProductType)
		p.KeyDifferentiator = commonutils.Deref(bc.KeyDifferentiator)
		p.BrandVoice = commonutils.Deref(bc.BrandVoiceSignals)
		if len(bc.KeyFeatures) > 0 {
			p.KeyFeatures = strings.Join(bc.KeyFeatures, ", ")
		}
		if bc.ICPSignals != nil {
			if len(bc.ICPSignals.Roles) > 0 {
				p.ICPRole = strings.Join(bc.ICPSignals.Roles, ", ")
			}
			if len(bc.ICPSignals.PainPoints) > 0 {
				p.ICPPainPoints = strings.Join(bc.ICPSignals.PainPoints, ", ")
			}
			if len(bc.ICPSignals.Industries) > 0 {
				p.ICPIndustries = strings.Join(bc.ICPSignals.Industries, ", ")
			}
			if bc.ICPSignals.CompanySize != nil {
				p.ICPCompanySize = *bc.ICPSignals.CompanySize
			}
		}
	}

	if ga := mc.SerpGapAnalysis; ga != nil {
		p.DifferentiatingAngle = ga.DifferentiatingAngle
		p.FeaturedSnippetOpp = ga.FeaturedSnippetOpportunity
		if len(ga.GapsIdentified) > 0 {
			gapsJSON, _ := json.Marshal(ga.GapsIdentified)
			p.GapsIdentified = string(gapsJSON)
		}
		if len(ga.CoveredByAll) > 0 {
			covJSON, _ := json.Marshal(ga.CoveredByAll)
			p.CoveredByAll = string(covJSON)
		}
	}

	if sd := mc.SerpData; sd != nil {
		p.AverageWordCount = sd.AverageWordCount
		p.FeaturedSnippetPresent = sd.FeaturedSnippetPresent
		if len(sd.PAAQuestions) > 0 {
			paaJSON, _ := json.Marshal(sd.PAAQuestions)
			p.PAAQuestions = string(paaJSON)
		}
	}

	if tr := mc.TopicResearch; tr != nil {
		if tr.RecentNews != nil {
			p.TopicResearchNews = tr.RecentNews.Insight
			p.TopicResearchNewsSource = tr.RecentNews.Source
			p.TopicResearchNewsSourceName = tr.RecentNews.SourceName
		}
		if tr.ExpertOpinion != nil {
			p.TopicResearchExpert = tr.ExpertOpinion.Insight
			p.TopicResearchExpertSource = tr.ExpertOpinion.Source
			p.TopicResearchExpertSourceName = tr.ExpertOpinion.SourceName
		}
		if tr.CommonMistakes != nil {
			p.TopicResearchMistakes = tr.CommonMistakes.Insight
			p.TopicResearchMistakesSource = tr.CommonMistakes.Source
			p.TopicResearchMistakesSourceName = tr.CommonMistakes.SourceName
		}
	}

	if yt := mc.YouTubeInsights; yt != nil && len(yt.Insights) > 0 {
		ytJSON, _ := json.Marshal(yt.Insights)
		p.YouTubeInsights = string(ytJSON)
	}

	return p
}

// slugStopWords are dropped from the slug so the URL leads with the primary
// keyword and stays clean/short (mirrors the SEO rule in the outline prompt).
var slugStopWords = map[string]bool{
	"a": true, "an": true, "and": true, "the": true, "for": true,
	"of": true, "to": true, "in": true, "on": true, "or": true,
	"with": true, "your": true, "is": true, "are": true, "at": true,
	"by": true, "from": true,
}

// slugWordBoundary matches any run of characters that isn't part of a word, so
// spaces, underscores, punctuation and existing hyphens all split words apart.
var slugWordBoundary = regexp.MustCompile(`[^a-z0-9]+`)

// extractOutlineSlug picks the url_slug out of the raw outline JSON and
// normalises it so it is safe to publish regardless of how the model formatted
// it: lowercase, stop words removed, non-alphanumeric runs collapsed to single
// hyphens, and capped at maxLen (values.contentGeneration.slugMaxLen) without
// leaving a dangling word or hyphen. Returns "" when the outline has no usable
// slug.
func extractOutlineSlug(rawJSON string, maxLen int) string {
	var parsed struct {
		URLSlug string `json:"url_slug"`
	}
	if err := json.Unmarshal([]byte(rawJSON), &parsed); err != nil {
		return ""
	}
	return normalizeSlug(parsed.URLSlug, maxLen)
}

func normalizeSlug(raw string, maxLen int) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return ""
	}

	// Split on any run of non-alphanumeric characters so spaces, underscores
	// and existing hyphens all become word boundaries, then drop stop words.
	words := slugWordBoundary.Split(s, -1)
	kept := make([]string, 0, len(words))
	for _, w := range words {
		if w == "" || slugStopWords[w] {
			continue
		}
		kept = append(kept, w)
	}
	// If stripping stop words left nothing (e.g. the slug was all stop words),
	// fall back to the non-empty words so we still emit something usable.
	if len(kept) == 0 {
		for _, w := range words {
			if w != "" {
				kept = append(kept, w)
			}
		}
	}

	slug := strings.Join(kept, "-")
	if len(slug) <= maxLen {
		return slug
	}

	// Trim to the length cap on a word boundary so we never cut a word in half
	// or leave a trailing hyphen.
	truncated := slug[:maxLen]
	if idx := strings.LastIndex(truncated, "-"); idx > 0 {
		truncated = truncated[:idx]
	}
	return strings.Trim(truncated, "-")
}
