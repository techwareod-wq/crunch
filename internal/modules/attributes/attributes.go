// Package attributes is the WarehouseHub attributes module: attribute definitions and industries CRUD, seed, and the recompute jobs (spec 02).
//
// Scaffold (PL-10): routes, jobs and cleaners land with the AT-xx tasks.
package attributes

import (
	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/modules"
)

// Module is the attributes feature.
type Module struct {
	modules.Base
	appCtx *config.AppContext
}

var _ modules.Module = (*Module)(nil)

// New builds the module. appCtx is read at call time, never here.
func New(appCtx *config.AppContext) *Module {
	return &Module{appCtx: appCtx}
}

func (m *Module) Name() string { return "attributes" }
