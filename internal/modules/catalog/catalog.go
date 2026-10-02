// Package catalog is the WarehouseHub catalog module: warehouses, revisions, rent, media, geocoding and the public listing API (spec 03).
//
// Scaffold (PL-10): routes, jobs and cleaners land with the CA-xx tasks.
package catalog

import (
	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/modules"
)

// Module is the catalog feature.
type Module struct {
	modules.Base
	appCtx *config.AppContext
}

var _ modules.Module = (*Module)(nil)

// New builds the module. appCtx is read at call time, never here.
func New(appCtx *config.AppContext) *Module {
	return &Module{appCtx: appCtx}
}

func (m *Module) Name() string { return "catalog" }
