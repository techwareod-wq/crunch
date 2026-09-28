package collectors

import (
	"fmt"

	"github.com/atharva-ng/crunch/internal/audit/core"
	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/models"
)

// BuildCollectorRegistry wires the static collector list and fails the boot
// on duplicate IDs or duplicate produced kinds (cron-registry flavour:
// definitions in code, misconfig dies at startup, not at 3 AM). Values feed
// the collectors whose applicability is a config switch (mentions).
//
// The crawl collector is registered like the rest so its Produces()
// participates in boot validation, but it is EXECUTED by the dedicated
// AUDIT_CRAWL stage, not the generic collect fan-out — FanOut() excludes it.
func BuildCollectorRegistry(vals config.AuditValues) ([]Collector, error) {
	list := []Collector{
		&crawlCollector{},
		&htmlDeepCollector{},
		&psiCollector{},
		&authorityCollector{},
		&serpCollector{},
		&mentionsCollector{enabled: vals.Mentions.Enabled},
	}

	seenIDs := map[core.CollectorID]bool{}
	seenKinds := map[core.Kind]core.CollectorID{}
	for _, c := range list {
		if seenIDs[c.ID()] {
			return nil, fmt.Errorf("audit collectors: duplicate collector id %q", c.ID())
		}
		seenIDs[c.ID()] = true
		for _, k := range c.Produces() {
			if owner, dup := seenKinds[k]; dup {
				return nil, fmt.Errorf("audit collectors: kind %q produced by both %q and %q", k, owner, c.ID())
			}
			seenKinds[k] = c.ID()
		}
	}
	return list, nil
}

// ProducedKinds returns the set of artifact kinds any registered collector
// can produce — the check registry validates Requires() against it.
func ProducedKinds(list []Collector) map[core.Kind]bool {
	out := map[core.Kind]bool{}
	for _, c := range list {
		for _, k := range c.Produces() {
			out[k] = true
		}
	}
	return out
}

// FanOut returns the collectors the generic AUDIT_COLLECT fan-out should
// dispatch for a run: every applicable collector except crawl (which the
// AUDIT_CRAWL stage already ran).
func FanOut(list []Collector, run *models.AuditRun) []Collector {
	var out []Collector
	for _, c := range list {
		if c.ID() == CollectorCrawl {
			continue
		}
		if c.AppliesTo(run) {
			out = append(out, c)
		}
	}
	return out
}

// ByID resolves one collector (the generic AUDIT_COLLECT handler's lookup).
func ByID(list []Collector, id core.CollectorID) (Collector, bool) {
	for _, c := range list {
		if c.ID() == id {
			return c, true
		}
	}
	return nil, false
}
