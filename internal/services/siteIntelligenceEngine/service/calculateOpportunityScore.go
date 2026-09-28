package service

import (
	"context"
	"fmt"
	"math"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/pipeline"
	sie "github.com/atharva-ng/crunch/internal/services/siteIntelligenceEngine"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

type pipelineState struct {
	ctx      context.Context
	metadata sie.SIEMetadata
	wec      *models.WebEntityContext
	keywords []models.Keyword
	// sieValues carries the full SIE config so stepFetchContext can resolve
	// the strategy-specific scoring once the web entity (and with it the
	// strategy) is in hand.
	sieValues config.SiteIntelligenceValues
	// scoring carries the opportunity-score weights/anchors — seeded with the
	// base values and re-resolved per strategy in stepFetchContext — so the
	// free scoring helpers can read them without reaching back into the
	// service.
	scoring config.ScoringValues
	// userDR is resolved in stepFetchContext (persisted domain rating, falling
	// back to values.siteIntelligence.defaultDomainRating) and feeds the
	// DR-adjusted difficulty score.
	userDR int
}

// resolveScoringInputs resolves the strategy-tilted scoring knobs and the
// user's domain rating (falling back to the configured default) for a web
// entity. Shared by the scoring stage, the manual-keyword path, and the
// post-processing dedupe's provisional scores.
func resolveScoringInputs(values config.SiteIntelligenceValues, webEntity *models.WebEntity) (config.ScoringValues, int) {
	userDR := values.DefaultDomainRating
	if webEntity.BusinessContext != nil && webEntity.BusinessContext.UserDomainRating > 0 {
		userDR = webEntity.BusinessContext.UserDomainRating
	}
	return values.ScoringForStrategy(webEntity.SEOStrategyOrDefault()), userDR
}

type scoringStep func(*pipelineState) error

func runScoringPipeline(state *pipelineState, steps []scoringStep) error {
	for _, step := range steps {
		if err := step(state); err != nil {
			return err
		}
	}
	return nil
}

func (s *seoBlogGeneratorSiteIntelligence) CalculateOpportunityScore(ctx context.Context, userId string, metadata sie.SIEMetadata) error {
	ok, wec, err := models.GetWebEntityContext(ctx, metadata.WebEntityContextID)
	if err != nil {
		return fmt.Errorf("failed to get web entity context: %w", err)
	}
	if !ok {
		return fmt.Errorf("web entity context not found: %s", metadata.WebEntityContextID)
	}

	// Entry guard (P2): scoring (or a later stage) already done — drop the
	// redelivery/duplicate so it can't re-dispatch Clustering.
	if wec.EffectiveStatus() >= models.SIEStatusOpportunityScoreCalculated {
		return nil
	}

	state := &pipelineState{
		ctx:       ctx,
		metadata:  metadata,
		sieValues: s.values,
		scoring:   s.values.Scoring,
	}

	steps := []scoringStep{
		stepFetchContext,
		stepScoreKeywords,
		stepPersistResults,
	}

	if err := runScoringPipeline(state, steps); err != nil {
		return err
	}

	// Advance + dispatch-once (P3): OpportunityScore has no distinct …Started
	// value, so FunnelClassificationDone → OpportunityScoreCalculated is the single
	// atomic claim+complete. The scoring above is a pure idempotent $set, so
	// re-running it before losing the CAS is harmless. SIEStatusError is in the
	// from-set so a retry whose prior attempt errored can still advance (the entry
	// guard above already rejects a genuinely later stage). Only the winner
	// dispatches Clustering.
	claimed, err := models.TryAdvanceStatus(ctx, metadata.WebEntityContextID,
		[]int{models.SIEStatusFunnelClassificationDone, models.SIEStatusError},
		models.SIEStatusOpportunityScoreCalculated)
	if err != nil {
		return fmt.Errorf("failed to advance to opportunity score calculated: %w", err)
	}
	if !claimed {
		return nil
	}

	return s.pipeline.DispatchNext(ctx, sie.ProcessSIEOpportunityScore, pipeline.DispatchContext{
		UserID:             userId,
		WebEntityID:        metadata.WebEntityID,
		WebEntityContextID: metadata.WebEntityContextID,
	})
}

func stepFetchContext(s *pipelineState) error {
	ok, wec, err := models.GetWebEntityContext(s.ctx, s.metadata.WebEntityContextID)
	if err != nil {
		return fmt.Errorf("stepFetchContext: %w", err)
	}
	if !ok {
		return fmt.Errorf("stepFetchContext: web entity context not found: %s", s.metadata.WebEntityContextID)
	}
	s.wec = wec

	keywords, err := models.GetKeywordsForWEC(s.ctx, s.metadata.WebEntityContextID)
	if err != nil {
		return fmt.Errorf("stepFetchContext: load keywords: %w", err)
	}
	s.keywords = keywords

	// Resolve the user's domain rating (persisted by onboarding Step 3,
	// GetCompetitorInfo, with a configured-default fallback so scoring never
	// blocks on a missing value) and the strategy-tilted scoring knobs
	// (early_footholds weighs difficulty over volume with a lower volume
	// anchor); the same strategy already bounded the raw candidate set via the
	// DataForSEO filters.
	isWebEntity, webEntity, err := models.FindWebEntityByID(s.ctx, s.metadata.WebEntityID)
	if err != nil {
		return fmt.Errorf("stepFetchContext: find web entity: %w", err)
	}
	if !isWebEntity {
		return fmt.Errorf("stepFetchContext: web entity not found: %s", s.metadata.WebEntityID)
	}
	s.scoring, s.userDR = resolveScoringInputs(s.sieValues, webEntity)
	return nil
}

func stepScoreKeywords(s *pipelineState) error {
	for i := range s.keywords {
		kw := &s.keywords[i]
		kw.OpportunityScore = computeOpportunityScore(s.scoring, kw.Volume, kw.KeywordDifficulty, kw.Funnel, s.userDR)
	}
	return nil
}

func stepPersistResults(s *pipelineState) error {
	updates := make(map[primitive.ObjectID]float64, len(s.keywords))
	for _, kw := range s.keywords {
		updates[kw.ID] = kw.OpportunityScore
	}
	return models.UpdateKeywordOpportunityScores(s.ctx, updates)
}

func getVolumeScore(sc config.ScoringValues, volume int) float64 {
	v := math.Max(float64(volume), sc.VolumeScoreFloor)
	score := math.Log10(v) / math.Log10(sc.VolumeLogAnchor)
	return math.Min(score, sc.VolumeScoreCap)
}

func getIntentScore(sc config.ScoringValues, funnel models.FunnelStage) float64 {
	switch funnel {
	case models.FunnelBOFU:
		return sc.IntentScoreBOFU
	case models.FunnelMOFU:
		return sc.IntentScoreMOFU
	case models.FunnelTOFU:
		return sc.IntentScoreTOFU
	default:
		return sc.IntentScoreDefault
	}
}

// getDifficultyScore converts keyword difficulty to a [floor, cap] sub-score,
// discounting KD by the user's domain-rating headroom over the anchor:
// adjustedKD = kd − (userDR − anchor) × slope. The cap matters because a
// strong domain can push adjustedKD negative. Unknown difficulty (kd < 0)
// stays a neutral constant — without this guard it would clamp to the cap and
// rank unknown-KD keywords as maximally easy.
func getDifficultyScore(sc config.ScoringValues, kd, userDR int) float64 {
	if kd < 0 {
		return sc.DifficultyScoreNull
	}
	adjustedKD := float64(kd) - (float64(userDR)-sc.DifficultyDRAnchor)*sc.DifficultyDRSlope
	score := (sc.DifficultyKDScale - adjustedKD) / sc.DifficultyKDScale
	return math.Min(math.Max(score, sc.DifficultyScoreFloor), sc.DifficultyScoreCap)
}

// computeOpportunityScore scores a single keyword: a weighted sum of the
// volume/difficulty/intent sub-scores scaled to [0, scale], then a concave
// gamma curve (final = scale × (raw/scale)^gamma) that lifts low/mid scores
// while keeping the endpoints fixed. Shared by the bulk pipeline
// (stepScoreKeywords) and the manual-keyword path.
func computeOpportunityScore(sc config.ScoringValues, volume, keywordDifficulty int, funnel models.FunnelStage, userDR int) float64 {
	v := getVolumeScore(sc, volume)
	d := getDifficultyScore(sc, keywordDifficulty, userDR)
	f := getIntentScore(sc, funnel)
	raw := (sc.WeightVolume*v + sc.WeightDifficulty*d + sc.WeightFunnel*f) * sc.OpportunityScale
	curved := sc.OpportunityScale * math.Pow(raw/sc.OpportunityScale, sc.OpportunityGamma)
	return math.Round(curved*sc.ScorePrecision) / sc.ScorePrecision
}
