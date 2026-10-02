// Package analytics is the WarehouseHub analytics module: search event logging, the daily rollup cron and the dashboards (spec 07).
//
// Scaffold (PL-10): routes, jobs and cleaners land with the AN-xx tasks.
package analytics

import (
	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/modules"
)

// Module is the analytics feature.
type Module struct {
	modules.Base
	appCtx *config.AppContext
}

var _ modules.Module = (*Module)(nil)

// New builds the module. appCtx is read at call time, never here.
func New(appCtx *config.AppContext) *Module {
	return &Module{appCtx: appCtx}
}

func (m *Module) Name() string { return "analytics" }
