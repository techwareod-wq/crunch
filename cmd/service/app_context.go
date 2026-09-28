package main

import (
	"context"
	"fmt"
	"time"

	"github.com/clerk/clerk-sdk-go/v2"

	"github.com/atharva-ng/crunch/cmd/service/providers"
	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/util/log"
)

func ProvideAppContext(appCtx *config.AppContext) error {
	if err := config.LoadConfigFromEnv(&appCtx.Config); err != nil {
		return err
	}

	// Wire the Clerk secret into the SDK's default backend so JWKS lookups
	// and clerkuser.Get() calls authenticate. Without this, every outbound
	// Clerk API request fails with "authorization_header_format_invalid".
	if appCtx.Config.Clerk.SecretKey == "" {
		return fmt.Errorf("CLERK_SECRET_KEY is not set")
	}
	clerk.SetKey(appCtx.Config.Clerk.SecretKey)

	if err := validatePaddleConfig(appCtx); err != nil {
		return err
	}

	log.Info("connecting to MongoDB", "db", appCtx.Config.Database.Name)
	if err := models.Connect(appCtx.Config.Database.URL, appCtx.Config.Database.Name); err != nil {
		return err
	}
	if err := models.EnsureUserIndexes(context.Background()); err != nil {
		return err
	}
	if err := models.EnsureKeywordIndexes(context.Background()); err != nil {
		return err
	}
	if err := models.EnsureScheduledArticleIndexes(context.Background()); err != nil {
		return err
	}
	if err := models.EnsureWebEntityContextIndexes(context.Background()); err != nil {
		return err
	}
	if err := models.EnsureWebEntityMasterContextIndexes(context.Background()); err != nil {
		return err
	}
	if err := models.EnsureWebEntityIndexes(context.Background()); err != nil {
		return err
	}
	if err := models.EnsureStyleReplicationRunIndexes(context.Background()); err != nil {
		return err
	}
	if err := models.EnsurePaymentIndexes(context.Background()); err != nil {
		return err
	}
	if err := models.EnsurePlanIndexes(context.Background()); err != nil {
		return err
	}
	if err := models.EnsureRoleIndexes(context.Background()); err != nil {
		return err
	}
	if err := models.EnsureAdminActionIndexes(context.Background()); err != nil {
		return err
	}
	if err := models.EnsureDeletedSubscriptionIndexes(context.Background()); err != nil {
		return err
	}
	if err := models.EnsureCronClaimIndexes(context.Background()); err != nil {
		return err
	}
	if err := models.EnsureCronRunIndexes(context.Background()); err != nil {
		return err
	}
	if err := models.EnsureCompanyIndexes(context.Background()); err != nil {
		return err
	}
	if err := models.EnsureCompanyMembershipIndexes(context.Background()); err != nil {
		return err
	}
	if err := models.EnsureAnalyticsRawIndexes(context.Background()); err != nil {
		return err
	}
	if err := models.EnsureAnalyticsFactIndexes(context.Background()); err != nil {
		return err
	}
	if err := models.EnsureAuditRunIndexes(context.Background()); err != nil {
		return err
	}
	auditArtifactTTL := time.Duration(appCtx.Config.Values.Audit.ArtifactTTLDays) * 24 * time.Hour
	if auditArtifactTTL <= 0 {
		auditArtifactTTL = 7 * 24 * time.Hour
	}
	if err := models.EnsureAuditArtifactIndexes(context.Background(), auditArtifactTTL); err != nil {
		return err
	}
	if err := models.EnsureAuditDomainLedgerIndexes(context.Background()); err != nil {
		return err
	}
	if err := models.EnsureAuditRecheckIndexes(context.Background()); err != nil {
		return err
	}
	log.Info("MongoDB connected")

	if err := providers.InjectDefaultProviders(appCtx); err != nil {
		return err
	}
	if err := providers.InjectDefaultServices(appCtx); err != nil {
		return err
	}

	return nil
}

func validatePaddleConfig(appCtx *config.AppContext) error {
	p := appCtx.Config.Paddle
	// Paddle credentials are required in every environment — missing keys are
	// fatal at startup rather than a deferred failure on the first payment call.
	// (Sandbox vs. live is derived separately from ENVIRONMENT; see
	// EnvConfig.PaddleEnvironment.)
	if p.APIKey == "" || p.WebhookSecret == "" || p.ProductID == "" {
		return fmt.Errorf("PADDLE_API_KEY, PADDLE_WEBHOOK_SECRET and PADDLE_PRODUCT_ID must be set")
	}
	return nil
}
