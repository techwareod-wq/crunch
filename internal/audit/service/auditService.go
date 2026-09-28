// Package service is the audit engine's unexported implementation behind
// the audit.AuditService facade (the styleReplication shape). All boot
// validation — spec resolution, collector registry, check registry —
// happens in NewService, so a broken graph fails the process at startup.
package service

import (
	"fmt"

	"github.com/atharva-ng/crunch/internal/audit"
	"github.com/atharva-ng/crunch/internal/audit/checks"
	"github.com/atharva-ng/crunch/internal/audit/collectors"
	"github.com/atharva-ng/crunch/internal/audit/spec"
	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/locations"
	"github.com/atharva-ng/crunch/internal/pipeline"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
)

type auditService struct {
	llm        *config.LLMProvider
	dfs        interfaces.DataForSEO
	psi        interfaces.PageSpeedInsights
	dispatcher interfaces.Dispatcher
	pipeline   *pipeline.Pipeline

	values    config.AuditValues
	dfsValues config.DataForSEOValues
	locations *locations.Catalog

	activeSpec spec.Spec
	collectors []collectors.Collector
	registry   *checks.Registry

	// Re-check eligibility, computed once at boot from the registry.
	recheckable    []string
	recheckableSet map[string]bool
}

// NewService wires the audit engine. Any registry/spec inconsistency is a
// boot error — the process must not serve with a broken scoring graph.
func NewService(
	llm *config.LLMProvider,
	dfs interfaces.DataForSEO,
	psi interfaces.PageSpeedInsights,
	dispatcher interfaces.Dispatcher,
	values config.AuditValues,
	dfsValues config.DataForSEOValues,
	locationCatalog *locations.Catalog,
) (audit.AuditService, error) {
	activeSpec, err := spec.ByVersion(values.Scoring.SpecVersion)
	if err != nil {
		return nil, fmt.Errorf("audit service: %w", err)
	}
	collectorList, err := collectors.BuildCollectorRegistry(values)
	if err != nil {
		return nil, fmt.Errorf("audit service: %w", err)
	}
	registry, err := checks.BuildCheckRegistry(activeSpec, collectorList, values)
	if err != nil {
		return nil, fmt.Errorf("audit service: %w", err)
	}

	s := &auditService{
		llm:        llm,
		dfs:        dfs,
		psi:        psi,
		dispatcher: dispatcher,
		values:     values,
		dfsValues:  dfsValues,
		locations:  locationCatalog,
		activeSpec: activeSpec,
		collectors: collectorList,
		registry:   registry,
	}
	s.recheckable = buildRecheckableIDs(registry)
	s.recheckableSet = make(map[string]bool, len(s.recheckable))
	for _, id := range s.recheckable {
		s.recheckableSet[id] = true
	}
	s.pipeline = s.buildPipeline(dispatcher)
	return s, nil
}

// deps assembles the collector dependency bundle.
func (s *auditService) deps() collectors.Deps {
	return collectors.Deps{
		DFS:       s.dfs,
		PSI:       s.psi,
		Values:    s.values,
		DFSValues: s.dfsValues,
		Locations: s.locations,
	}
}
