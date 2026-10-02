// Package enquiries is the WarehouseHub enquiries module: the enquiry API, the staff inbox, enquiry emails and the data cleaner (spec 06).
//
// Scaffold (PL-10): routes, jobs and cleaners land with the EN-xx tasks.
package enquiries

import (
	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/modules"
)

// Module is the enquiries feature.
type Module struct {
	modules.Base
	appCtx *config.AppContext
}

var _ modules.Module = (*Module)(nil)

// New builds the module. appCtx is read at call time, never here.
func New(appCtx *config.AppContext) *Module {
	return &Module{appCtx: appCtx}
}

func (m *Module) Name() string { return "enquiries" }
