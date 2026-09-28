// Package modules defines how a feature plugs into crunch. A feature is one
// Module: it contributes async handlers, cron jobs, HTTP routes and a slice of
// the account-deletion cascade, and the platform (queue, cron, admin, RBAC)
// wires them in at boot. Adding a feature means writing a Module and listing
// it in cmd/service/modules.go — no platform package changes.
//
// internal/modules/demo is the reference implementation; copy its shape.
package modules

import (
	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/cron"
	"github.com/atharva-ng/crunch/internal/pipeline"
	"github.com/atharva-ng/crunch/internal/services/accountService"
	asynchandler "github.com/atharva-ng/crunch/internal/services/asyncHandler"
)

// Module is one feature's contribution to the app. Modules are constructed
// with the *config.AppContext before its providers and services are injected,
// so they must read AppContext fields at call time (in handlers, resolvers and
// routes), never cache them in the constructor.
type Module interface {
	// Name identifies the module in logs and the deletion report.
	Name() string
	// RegisterHandlers binds the module's async process handlers.
	RegisterHandlers(reg asynchandler.Registry)
	// LLMProcessTypes lists the process types routed to the secondary queue,
	// whose consumption is gated by the LLM token budget.
	LLMProcessTypes() []pipeline.ProcessType
	// VisibilityOverrides maps slow process types to a per-message SQS
	// visibility timeout (seconds), so they aren't redelivered mid-run.
	VisibilityOverrides() map[pipeline.ProcessType]int32
	// CronJobs returns the module's scheduled triggers. Each is dark until
	// enabled under values cron.jobs.<name>.
	CronJobs() []cron.Job
	// RegisterRoutes registers the module's HTTP routes.
	RegisterRoutes(appCtx *config.AppContext)
	// DataCleaners returns the module's slice of the account-deletion cascade.
	DataCleaners() []accountService.DataCleaner
}

// Base is a no-op Module to embed, so a module only implements what it uses.
type Base struct{}

func (Base) RegisterHandlers(asynchandler.Registry)              {}
func (Base) LLMProcessTypes() []pipeline.ProcessType             { return nil }
func (Base) VisibilityOverrides() map[pipeline.ProcessType]int32 { return nil }
func (Base) CronJobs() []cron.Job                                { return nil }
func (Base) RegisterRoutes(*config.AppContext)                   {}
func (Base) DataCleaners() []accountService.DataCleaner          { return nil }

// BuildRegistry collects every module's async handlers into one registry.
func BuildRegistry(mods []Module) asynchandler.Registry {
	reg := asynchandler.NewRegistry()
	for _, m := range mods {
		m.RegisterHandlers(reg)
	}
	return reg
}

// LLMProcessTypes collects every module's secondary-queue process types.
func LLMProcessTypes(mods []Module) []pipeline.ProcessType {
	var out []pipeline.ProcessType
	for _, m := range mods {
		out = append(out, m.LLMProcessTypes()...)
	}
	return out
}

// VisibilityOverrides merges every module's per-type visibility timeouts.
func VisibilityOverrides(mods []Module) map[pipeline.ProcessType]int32 {
	out := map[pipeline.ProcessType]int32{}
	for _, m := range mods {
		for pt, secs := range m.VisibilityOverrides() {
			out[pt] = secs
		}
	}
	return out
}

// CronJobs collects every module's job definitions.
func CronJobs(mods []Module) []cron.Job {
	var out []cron.Job
	for _, m := range mods {
		out = append(out, m.CronJobs()...)
	}
	return out
}

// DataCleaners collects every module's deletion-cascade cleaners, in module
// order.
func DataCleaners(mods []Module) []accountService.DataCleaner {
	var out []accountService.DataCleaner
	for _, m := range mods {
		out = append(out, m.DataCleaners()...)
	}
	return out
}

// RegisterRoutes registers every module's HTTP routes.
func RegisterRoutes(appCtx *config.AppContext, mods []Module) {
	for _, m := range mods {
		m.RegisterRoutes(appCtx)
	}
}
