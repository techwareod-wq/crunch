package main

import (
	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/modules"
	"github.com/atharva-ng/crunch/internal/modules/demo"
)

// enabledModules lists every feature module the service runs. Adding a
// feature = one line here (see internal/modules). Modules are built before the
// providers are injected, so they must read appCtx lazily.
func enabledModules(appCtx *config.AppContext) []modules.Module {
	return []modules.Module{
		demo.New(appCtx),
	}
}
