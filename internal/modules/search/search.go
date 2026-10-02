// Package search is the WarehouseHub search module: structured search, facets, map and geo resolve (spec 04).
//
// Scaffold (PL-10): routes, jobs and cleaners land with the SR-xx tasks.
package search

import (
	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/modules"
)

// Module is the search feature.
type Module struct {
	modules.Base
	appCtx *config.AppContext
}

var _ modules.Module = (*Module)(nil)

// New builds the module. appCtx is read at call time, never here.
func New(appCtx *config.AppContext) *Module {
	return &Module{appCtx: appCtx}
}

func (m *Module) Name() string { return "search" }
