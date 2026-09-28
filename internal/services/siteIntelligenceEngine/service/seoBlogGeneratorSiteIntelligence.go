package service

import (
	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/pipeline"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	"github.com/atharva-ng/crunch/internal/services/siteIntelligenceEngine"
	sieStore "github.com/atharva-ng/crunch/internal/services/siteIntelligenceEngine/store"
)

var _ siteIntelligenceEngine.SiteIntelligenceService = (*seoBlogGeneratorSiteIntelligence)(nil)

type seoBlogGeneratorSiteIntelligence struct {
	store      sieStore.Store
	LLM        *config.LLMProvider
	dataForSEO interfaces.DataForSEO
	dispatcher interfaces.Dispatcher
	pipeline   *pipeline.Pipeline
	// values holds the SIE tunables (keyword limits, scoring weights, manual
	// caps) sourced from values.siteIntelligence.
	values config.SiteIntelligenceValues
}

func NewService(s sieStore.Store, llm *config.LLMProvider, dataForSEO interfaces.DataForSEO, dispatcher interfaces.Dispatcher, values config.SiteIntelligenceValues) siteIntelligenceEngine.SiteIntelligenceService {
	p := buildPipeline(dispatcher, values.FunnelClassificationChunkSize)
	return &seoBlogGeneratorSiteIntelligence{store: s, LLM: llm, dataForSEO: dataForSEO, dispatcher: dispatcher, pipeline: p, values: values}
}
