package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/atharva-ng/crunch/cmd/service/controllers/admin"
	analyticsctl "github.com/atharva-ng/crunch/cmd/service/controllers/analytics"
	auditctl "github.com/atharva-ng/crunch/cmd/service/controllers/audit"
	"github.com/atharva-ng/crunch/cmd/service/controllers/company"
	"github.com/atharva-ng/crunch/cmd/service/controllers/contentbridge"
	"github.com/atharva-ng/crunch/cmd/service/controllers/healthcheck"
	"github.com/atharva-ng/crunch/cmd/service/controllers/onboarding"
	"github.com/atharva-ng/crunch/cmd/service/controllers/payments"
	"github.com/atharva-ng/crunch/cmd/service/controllers/scheduledarticles"
	"github.com/atharva-ng/crunch/cmd/service/controllers/seoblog"
	"github.com/atharva-ng/crunch/cmd/service/controllers/siteintelligence"
	"github.com/atharva-ng/crunch/cmd/service/controllers/stylereplication"
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

	// Pin the accepted Host headers (production: api.useindexly.com) so requests
	// sent directly to the server's IP are rejected. Empty list = unrestricted.
	middleware.SetAllowedHosts(appCtx.Config.Server.AllowedHosts)

	// Plan catalog for the entitlement middlewares (boot-loaded by the
	// providers).
	middleware.SetPlansCache(appCtx.PlansCache)

	// Role catalog for the admin authorization gate (boot-loaded by the
	// providers; mirrors SetPlansCache).
	middleware.SetRolesCache(appCtx.RolesCache)

	// Company-surface master switch (values.yaml company.enabled): OFF answers
	// every /v1/company/* route with a JSON 404 masquerade, CORS intact. The
	// data layer underneath keeps running — see CompanyValues.Enabled.
	middleware.SetCompanyFeatureEnabled(appCtx.Config.Values.Company.Enabled)

	// Analytics-surface master switch (values.yaml gsc.enabled): OFF answers
	// every /v1/analytics/* route with a JSON 404 masquerade, CORS intact. The
	// ingest layer is gated separately by the cron.jobs.analytics_* flags.
	middleware.SetAnalyticsFeatureEnabled(appCtx.Config.Values.GSC.Enabled)

	// Style-replication-surface master switch (values.yaml
	// styleReplication.enabled): OFF answers every /v1/style-replication/*
	// route with a JSON 404 masquerade, CORS intact.
	middleware.SetStyleReplicationFeatureEnabled(appCtx.Config.Values.StyleReplication.Enabled)

	// Audit-surface master switch (values.yaml audit.enabled): OFF answers
	// every /v1/audit/* route — tenant AND public lead routes — with a 404
	// masquerade, CORS intact.
	middleware.SetAuditFeatureEnabled(appCtx.Config.Values.Audit.Enabled)

	loadAppAPIs(appCtx)

	middleware.RegisterAll(nil)

	// Build async handlers after all services are wired.
	primaryHandler := providers.BuildAsyncHandler(appCtx)
	secondaryHandler := providers.BuildSecondaryAsyncHandler(appCtx)

	// Cron scheduler: resolves who is due per registered job and enqueues onto
	// the same queues the handlers above consume. Built after the services
	// (resolvers use them); a misconfigured job set is fatal at boot, not
	// silently at 08:30.
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

	// Periodic plan-catalog refresh: bounds staleness for processes that
	// didn't serve an admin plan write (rolling deploys run two).
	cacheRefresh := time.Duration(appCtx.Config.Values.Admin.CacheRefreshSeconds) * time.Second
	appCtx.PlansCache.StartRefresh(ctx, cacheRefresh)

	// Periodic role-catalog refresh: same rationale as the plans cache — bounds
	// staleness for a process that didn't serve an admin role write.
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

func loadAppAPIs(appCtx *config.AppContext) {
	healthcheck.Handle()
	users.Handle(appCtx)
	webhooks.Handle(appCtx)
	onboarding.Handle(appCtx)
	siteintelligence.Handle(appCtx)
	seoblog.Handle(appCtx)
	scheduledarticles.Handle(appCtx)
	payments.Handle(appCtx)
	contentbridge.Handle(appCtx)
	company.Handle(appCtx)
	analyticsctl.Handle(appCtx)
	stylereplication.Handle(appCtx)
	auditctl.Handle(appCtx)
	admin.Handle(appCtx)
}
