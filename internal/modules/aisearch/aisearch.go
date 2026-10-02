// Package aisearch is the WarehouseHub aisearch module: natural-language search to filters, embeddings and the vector fallback (spec 05).
//
// Scaffold (PL-10): routes, jobs and cleaners land with the AI-xx tasks.
package aisearch

import (
	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/modules"
)

// Module is the aisearch feature.
type Module struct {
	modules.Base
	appCtx *config.AppContext
}

var _ modules.Module = (*Module)(nil)

// New builds the module. appCtx is read at call time, never here.
func New(appCtx *config.AppContext) *Module {
	return &Module{appCtx: appCtx}
}

func (m *Module) Name() string { return "aisearch" }
