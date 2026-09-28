package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/atharva-ng/crunch/internal/dto"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/pipeline"
	cge "github.com/atharva-ng/crunch/internal/services/contentGenerationEngine"
	cgeDto "github.com/atharva-ng/crunch/internal/services/contentGenerationEngine/dto"
	"github.com/atharva-ng/crunch/internal/services/contentGenerationEngine/prompts"
	commonutils "github.com/atharva-ng/crunch/internal/utils"
)

func (s *contentGenerationEngineService) HandleArticleGeneration(ctx context.Context, userId string, payload cge.CGEStandardPayload) error {
	masterContextID := payload.WebEntityMasterContextID

	mc, kw, err := s.loadMasterContextAndKeyword(ctx, masterContextID)
	if err != nil {
		return fmt.Errorf("article generation: %w", err)
	}

	promptData := buildArticlePromptData(mc, kw)

	dataPrompt, err := s.LLM.Utils.ConstructPrompt(prompts.ArticleGenerationData, promptData)
	if err != nil {
		return fmt.Errorf("article generation: construct prompt: %w", err)
	}

	resp, err := s.LLM.Anthropic.Prompt(ctx, dto.PromptRequest{
		Messages: []dto.Message{
			{Role: "user", Content: prompts.ArticleGenerationInstructions, Cache: true},
			{Role: "user", Content: dataPrompt},
		},
		Model:     dto.AnthropicSonnet46,
		MaxTokens: s.values.ArticleMaxTokens,
	})
	if err != nil {
		return fmt.Errorf("article generation: LLM call: %w", err)
	}

	// Store article markdown (still contains image placeholders)
	articleContent := resp.Content
	wordCount := models.CountArticleWords(articleContent)
	if err := s.store.UpdateWebEntityMasterContext(ctx, masterContextID, models.WEMCUpdateReq{
		ArticleContent: &articleContent,
		WordCount:      &wordCount,
	}); err != nil {
		return fmt.Errorf("article generation: save article: %w", err)
	}

	// Initialize images array with empty entries for fan-out positional updates
	if err := s.store.InitCGEImages(ctx, masterContextID); err != nil {
		return fmt.Errorf("article generation: init images: %w", err)
	}

	dc := pipeline.DispatchContext{
		UserID:             userId,
		WebEntityContextID: masterContextID,
	}
	return s.pipeline.DispatchNext(ctx, cge.ProcessCGEArticleGeneration, dc)
}

func buildArticlePromptData(mc *models.WebEntityMasterContext, kw *models.Keyword) cgeDto.ArticleGenerationPrompt {
	p := cgeDto.ArticleGenerationPrompt{
		Keyword:                kw.Keyword,
		ArticleType:            mc.ArticleType,
		ProposedTitle:          mc.ProposedTitle,
		TargetWordCount:        mc.TargetWordCount,
		Funnel:                 string(kw.Funnel),
		AdditionalInstructions: mc.AdditionalInstructions,
		ToneProfile:            mc.ToneProfile,
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

	if mc.Outline != nil {
		p.FullOutlineJSON = mc.Outline.RawJSON
	}

	return p
}
