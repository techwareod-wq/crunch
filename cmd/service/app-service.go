package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/atharva-ng/crunch/cmd/service/controllers/admin"
	"github.com/atharva-ng/crunch/cmd/service/controllers/attributes"
	"github.com/atharva-ng/crunch/cmd/service/controllers/catalog"
	"github.com/atharva-ng/crunch/cmd/service/controllers/healthcheck"
	"github.com/atharva-ng/crunch/cmd/service/controllers/search"
	"github.com/atharva-ng/crunch/cmd/service/controllers/users"
	"github.com/atharva-ng/crunch/cmd/service/controllers/webhooks"
	"github.com/atharva-ng/crunch/cmd/service/providers"
	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/util/log"
)

func main() {
	appCtx := &config.AppContext{}

	err := ProvideAppContext(appCtx)
	if err != nil {
		log.Error("failed to add app context", "error", err)
		os.Exit(1)
	}

	// Lock CORS to the configured origins before any route is assembled.
	middleware.SetAllowedOrigins(appCtx.Config.Server.AllowedOrigins)

	// Pin the accepted Host headers (production: the public API host) so requests
	// sent directly to the server's IP are rejected. Empty list = unrestricted.
	middleware.SetAllowedHosts(appCtx.Config.Server.AllowedHosts)

	loadAppAPIs(appCtx)

	middleware.RegisterAll(nil)

	// Build async handlers after all services are wired.
	primaryHandler := providers.BuildAsyncHandler(appCtx)
	secondaryHandler := providers.BuildSecondaryAsyncHandler(appCtx)

	// Cron scheduler: resolves who is due per registered job and enqueues onto
	// the same queues the handlers above consume. Built after the services
	// (resolvers use them); a misconfigured job set is fatal at boot, not
	// silently at the first scheduled tick.
	cronScheduler, err := providers.BuildCronScheduler(appCtx)
	if err != nil {
		log.Error("failed to build cron scheduler", "error", err)
		os.Exit(1)
	}
	admin.SetCronScheduler(cronScheduler)

	// Root context cancelled on SIGINT/SIGTERM for graceful shutdown.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	appCtx.TokenTracker.Start()
	defer appCtx.TokenTracker.Stop()

	// The Warehouse root + rules snapshot must be loaded before the async
	// handlers start consuming (recompute evaluates with it).
	if err := appCtx.InternalServices.AttributeService.Boot(ctx); err != nil {
		log.Error("failed to boot attribute service", "error", err)
		os.Exit(1)
	}
	// Periodic rules refresh: bounds staleness on instances that didn't
	// serve a tree/industry write.
	refresh := time.Duration(appCtx.Config.Values.WarehouseHub.Attributes.CacheRefreshSeconds) * time.Second
	if refresh <= 0 {
		refresh = time.Minute
	}
	appCtx.InternalServices.AttributeService.StartRefresh(ctx, refresh)

	// Start primary async handler in background.
	go func() {
		if err := primaryHandler.Start(ctx); err != nil && ctx.Err() == nil {
			log.Error("primary async handler stopped unexpectedly", "error", err)
		}
	}()

	// Start secondary (LLM) async handler in background.
	go func() {
		if err := secondaryHandler.Start(ctx); err != nil && ctx.Err() == nil {
			log.Error("secondary async handler stopped unexpectedly", "error", err)
		}
	}()

	// Start the cron ticker; stops on SIGINT/SIGTERM like the rest.
	go cronScheduler.Start(ctx)

	addr := ":" + appCtx.Config.Server.Port
	log.Info("server starting", "addr", addr)

	srv := &http.Server{Addr: addr, Handler: middleware.HostGuard(http.DefaultServeMux)}

	// Shut down HTTP server when context is cancelled.
	go func() {
		<-ctx.Done()
		log.Info("shutting down HTTP server")
		_ = srv.Close()
	}()

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func loadAppAPIs(appCtx *config.AppContext) {
	healthcheck.Handle()
	users.Handle(appCtx)
	webhooks.Handle(appCtx)
	admin.Handle(appCtx)
	attributes.Handle(appCtx)
	catalog.Handle(appCtx)
	search.Handle(appCtx)
}
