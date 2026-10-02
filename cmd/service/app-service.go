package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/atharva-ng/crunch/cmd/service/controllers/admin"
	"github.com/atharva-ng/crunch/cmd/service/controllers/healthcheck"
	"github.com/atharva-ng/crunch/cmd/service/controllers/users"
	"github.com/atharva-ng/crunch/cmd/service/controllers/webhooks"
	"github.com/atharva-ng/crunch/cmd/service/providers"
	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/middleware"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/modules"
	"github.com/atharva-ng/crunch/internal/util/log"
)

func main() {
	appCtx := &config.AppContext{}
	mods := enabledModules(appCtx)

	err := ProvideAppContext(appCtx, mods)
	if err != nil {
		log.Error("failed to add app context", "error", err)
		os.Exit(1)
	}

	// Lock CORS to the configured origins before any route is assembled.
	middleware.SetAllowedOrigins(appCtx.Config.Server.AllowedOrigins)

	// Pin the accepted Host headers (production: the public API host) so requests
	// sent directly to the server's IP are rejected. Empty list = unrestricted.
	middleware.SetAllowedHosts(appCtx.Config.Server.AllowedHosts)

	// Role catalog for the admin authorization gate (boot-loaded by the
	// providers).
	middleware.SetRolesCache(appCtx.RolesCache)

	// Staff invites (D-011): a JWT-auto-created user picks up a pending
	// invite's role on first sign-in (the Clerk webhook is the other path).
	middleware.SetUserCreatedHook(func(ctx context.Context, u *models.User) {
		if err := appCtx.InternalServices.StaffInvites.Apply(ctx, u, "jwt"); err != nil {
			log.Error("staff invite apply on JWT auto-create failed — the webhook path will retry", "user_id", u.ID.Hex(), "error", err)
		}
	})

	loadAppAPIs(appCtx, mods)

	middleware.RegisterAll(nil)

	// Build async handlers after all services are wired.
	primaryHandler := providers.BuildAsyncHandler(appCtx, mods)
	secondaryHandler := providers.BuildSecondaryAsyncHandler(appCtx, mods)

	// Cron scheduler: resolves who is due per registered job and enqueues onto
	// the same queues the handlers above consume. Built after the services
	// (resolvers use them); a misconfigured job set is fatal at boot, not
	// silently at the first scheduled tick.
	cronScheduler, err := providers.BuildCronScheduler(appCtx, mods)
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

	// Periodic role-catalog refresh: bounds staleness for a process that
	// didn't serve an admin role write (rolling deploys run two).
	cacheRefresh := time.Duration(appCtx.Config.Values.Admin.CacheRefreshSeconds) * time.Second
	appCtx.RolesCache.StartRefresh(ctx, cacheRefresh)

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

func loadAppAPIs(appCtx *config.AppContext, mods []modules.Module) {
	healthcheck.Handle()
	users.Handle(appCtx)
	webhooks.Handle(appCtx)
	admin.Handle(appCtx)
	modules.RegisterRoutes(appCtx, mods)
}
