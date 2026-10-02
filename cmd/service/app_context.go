package main

import (
	"context"
	"fmt"

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

	log.Info("connecting to MongoDB", "db", appCtx.Config.Database.Name)
	if err := models.Connect(appCtx.Config.Database.URL, appCtx.Config.Database.Name); err != nil {
		return err
	}
	for _, ensure := range []func(context.Context) error{
		models.EnsureUserIndexes,
		models.EnsureAdminActionIndexes,
		models.EnsureCronClaimIndexes,
		models.EnsureCronRunIndexes,
		models.EnsureChangeLogIndexes,
		models.EnsureAttributeNodeIndexes,
		models.EnsureIndustryIndexes,
		models.EnsureWarehouseIndexes,
		models.EnsureWarehouseRevisionIndexes,
		models.EnsureWarehouseMediaIndexes,
		models.EnsureWarehouseRentIndexes,
		models.EnsureWarehouseSearchIndexes,
		models.EnsureGeocodeCacheIndexes,
		models.EnsurePincodeIndexes,
		models.EnsureEnquiryIndexes,
		models.EnsureSearchEventIndexes,
		models.EnsureSearchDailyIndexes,
	} {
		if err := ensure(context.Background()); err != nil {
			return err
		}
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
