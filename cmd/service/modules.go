package main

import (
	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/modules"
	"github.com/atharva-ng/crunch/internal/modules/aisearch"
	"github.com/atharva-ng/crunch/internal/modules/analytics"
	"github.com/atharva-ng/crunch/internal/modules/attributes"
	"github.com/atharva-ng/crunch/internal/modules/catalog"
	"github.com/atharva-ng/crunch/internal/modules/enquiries"
	"github.com/atharva-ng/crunch/internal/modules/mail"
	"github.com/atharva-ng/crunch/internal/modules/search"
)

// enabledModules lists every feature module the service runs. Adding a
// feature = one line here (see internal/modules). Modules are built before the
// providers are injected, so they must read appCtx lazily. Cross-module
// dependencies (domain interfaces) are wired here too.
func enabledModules(appCtx *config.AppContext) []modules.Module {
	return []modules.Module{
		mail.New(appCtx), // platform: mail.send job (D-103)

		// WarehouseHub (D-004): one module per service.
		attributes.New(appCtx),
		catalog.New(appCtx),
		search.New(appCtx),
		aisearch.New(appCtx),
		enquiries.New(appCtx),
		analytics.New(appCtx),
	}
}
