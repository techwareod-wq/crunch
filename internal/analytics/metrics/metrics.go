// Package metrics is the analytics read layer's metric-definition registry
// (LLD §5.1): named series/KPI definitions served by the generic
// /v1/analytics/series and /v1/analytics/kpis endpoints. A new chart or KPI is
// a new definition + its registration line — no endpoint changes.
package metrics

import (
	"context"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Kind is a metric's shape: per-day points or a single value with a
// period-over-period companion.
type Kind string

const (
	KindSeries Kind = "series"
	KindKPI    Kind = "kpi"
)

// Query is the resolved, clamped request one Build runs under. The controller
// resolves the entity, its WEC ids (keyword joins) and the date windows ONCE
// per request; PrevFrom/PrevTo are empty when no period-over-period companion
// applies (range=all).
type Query struct {
	WebEntityID primitive.ObjectID
	CompanyID   primitive.ObjectID
	WECIDs      []primitive.ObjectID
	From, To    string
	PrevFrom    string
	PrevTo      string
}

// SeriesPoint is one dated value.
type SeriesPoint struct {
	Date  string  `json:"date"`
	Value float64 `json:"value"`
}

// Result is the uniform metric envelope.
type Result struct {
	Unavailable bool          `json:"unavailable,omitempty"` // required source not connected
	Points      []SeriesPoint `json:"points,omitempty"`      // series
	Value       *float64      `json:"value,omitempty"`       // kpi
	PrevValue   *float64      `json:"prevValue,omitempty"`   // kpi period-over-period
}

// Def is one registered metric.
type Def struct {
	Name string
	// Sources lists the analytics sources this metric reads. The controller
	// returns {unavailable: true} without calling Build when a
	// connection-requiring source in this list isn't connected, and clamps the
	// request window with the FIRST listed source's clock.
	Sources []string
	Kind    Kind
	Build   func(ctx context.Context, q Query) (Result, error)
}

// registry is built once at package init, before any request goroutine can
// touch it — new metric = new Def in defs.go + its line in the list below.
// (Lazy population from Registry() raced: a second request could iterate the
// map while the first was still inserting.)
var registry = func() map[string]Def {
	m := map[string]Def{}
	for _, def := range []Def{
		clicksTrend, impressionsTrend, ctrTrend, positionTrend,
		articleClicksTrend, domainRatingTrend,
		clicksTotal, impressionsTotal, ctrAvg, positionAvg,
		articleClicksTotal, trafficValue, articleTrafficValue,
	} {
		m[def.Name] = def
	}
	return m
}()

// Registry returns the metric registry keyed by name.
func Registry() map[string]Def {
	return registry
}

// Names lists every registered metric name (400-response help text)..
func Names() []string {
	names := make([]string, 0, len(Registry()))
	for name := range Registry() {
		names = append(names, name)
	}
	return names
}
