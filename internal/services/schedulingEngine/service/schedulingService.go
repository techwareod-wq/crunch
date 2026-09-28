package service

import (
	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	"github.com/atharva-ng/crunch/internal/services/schedulingEngine"
	seStore "github.com/atharva-ng/crunch/internal/services/schedulingEngine/store"
)

var _ schedulingEngine.SchedulingService = (*schedulingEngineService)(nil)

// schedulingEngineService is the concrete implementation backing
// schedulingEngine.SchedulingService. The engine has no DAG of its own —
// every transition is per-ScheduledArticle (Orchestrate fans out, then
// ArticleType chains 1:1 to Title), and DispatchContext lacks the
// article-level identity those messages need. Each handler dispatches its
// successor directly with a typed payload — clearer than threading a custom
// DispatchFunc through the generic pipeline.
type schedulingEngineService struct {
	store      seStore.Store
	LLM        *config.LLMProvider
	dispatcher interfaces.Dispatcher
	// values holds the scheduling tunables (rolling-window weeks) sourced from
	// values.scheduling.
	values config.SchedulingValues
	// trial holds the trial-mode overrides (scheduleWeeks, cadence, and the
	// post-upgrade cadence target) sourced from values.siteIntelligence.trial.
	trial config.SIETrialValues
}

func NewService(s seStore.Store, llm *config.LLMProvider, dispatcher interfaces.Dispatcher, values config.SchedulingValues, trial config.SIETrialValues) schedulingEngine.SchedulingService {
	return &schedulingEngineService{
		store:      s,
		LLM:        llm,
		dispatcher: dispatcher,
		values:     values,
		trial:      trial,
	}
}
