package service

import (
	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/pipeline"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	"github.com/atharva-ng/crunch/internal/services/contentGenerationEngine"
	cgeStore "github.com/atharva-ng/crunch/internal/services/contentGenerationEngine/store"
)

var _ contentGenerationEngine.ContentGenerationService = (*contentGenerationEngineService)(nil)

type contentGenerationEngineService struct {
	store      cgeStore.Store
	LLM        *config.LLMProvider
	dataForSEO interfaces.DataForSEO
	tavily     interfaces.Tavily
	youtube    interfaces.YouTube
	imageGen   interfaces.ImageGenerator
	dispatcher interfaces.Dispatcher
	s3         interfaces.S3
	s3Bucket   string
	pipeline   *pipeline.Pipeline
	// values holds the per-stage token/char caps and image aspect ratio
	// sourced from values.contentGeneration.
	values config.ContentGenerationValues
}

func NewService(
	s cgeStore.Store,
	llm *config.LLMProvider,
	dataForSEO interfaces.DataForSEO,
	tavily interfaces.Tavily,
	youtube interfaces.YouTube,
	imageGen interfaces.ImageGenerator,
	dispatcher interfaces.Dispatcher,
	s3 interfaces.S3,
	s3Bucket string,
	values config.ContentGenerationValues,
) contentGenerationEngine.ContentGenerationService {
	p := buildPipeline(dispatcher)
	return &contentGenerationEngineService{
		store:      s,
		LLM:        llm,
		dataForSEO: dataForSEO,
		tavily:     tavily,
		youtube:    youtube,
		imageGen:   imageGen,
		dispatcher: dispatcher,
		s3:         s3,
		s3Bucket:   s3Bucket,
		pipeline:   p,
		values:     values,
	}
}
