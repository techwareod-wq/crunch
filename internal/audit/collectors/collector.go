// Package collectors holds the audit's data-acquisition strategies. Each
// collector fetches from one source, normalizes into artifact structs, and
// upserts artifact docs; collectors never score. A new data source is a new
// file + one registry line (extension recipe Tier C).
package collectors

import (
	"context"

	"github.com/atharva-ng/crunch/internal/audit/core"
	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/locations"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/providers/interfaces"
)

// Collector IDs.
const (
	CollectorCrawl     core.CollectorID = "crawl"
	CollectorHTMLDeep  core.CollectorID = "html_deep"
	CollectorPSI       core.CollectorID = "psi"
	CollectorAuthority core.CollectorID = "authority"
	CollectorSERP      core.CollectorID = "serp"
	CollectorMentions  core.CollectorID = "mentions"
)

// Deps carries the providers + values a collector may use. Constructor-
// injected by the service; collectors stay stateless.
type Deps struct {
	DFS       interfaces.DataForSEO
	PSI       interfaces.PageSpeedInsights
	Values    config.AuditValues
	DFSValues config.DataForSEOValues
	// Locations is the shared boot-time country catalog (market inference +
	// display names). Nil-safe: a missing catalog degrades to "no signal".
	Locations *locations.Catalog
}

// Collector is one data-acquisition strategy.
type Collector interface {
	ID() core.CollectorID
	// Produces declares the artifact kinds this collector writes — boot
	// validation proves every check's Requires() has a producer.
	Produces() []core.Kind
	// AppliesTo lets a collector opt out per run (e.g. the SERP collector
	// when the target has zero ranked keywords is discovered at collect time;
	// this hook covers what is knowable at fan-out time).
	AppliesTo(run *models.AuditRun) bool
	// Critical collectors fail the run; non-critical failures degrade to a
	// constraint (§8.3). V1: only "crawl" is critical.
	Critical() bool
	// Collect fetches, normalizes, and upserts artifact docs for the run.
	// Must be idempotent (SQS redelivery ⇒ re-upsert, same result).
	Collect(ctx context.Context, deps Deps, run *models.AuditRun) error
}
