// Command companymigrate is the company-tenancy backfill (tenancy plan §8):
// it mints a personal company + claimed owner membership for every existing
// user, stamps company_id onto every legacy webEntity, verifies the
// invariants, and — as a separate later-deploy step — drops the legacy unique
// {user_id} webEntity index.
//
// Re-runnable by design: idempotence is index-backed (the D20 partial-unique
// {owner_user_id} where kind:"personal" makes a re-run, or a race with the
// signup hook, a dup-key no-op). Defaults to a dry run; pass -apply to write.
//
//	go run ./cmd/companymigrate                          # dry run: what would be minted/stamped
//	go run ./cmd/companymigrate -apply                   # backfill personal companies + stamp webEntities
//	go run ./cmd/companymigrate -verify                  # invariant check (read-only)
//	go run ./cmd/companymigrate -drop-user-index -apply  # LAST phase: drop webEntity user_id_1
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/joho/godotenv"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/models"
)

func main() {
	apply := flag.Bool("apply", false, "write the changes (default is a dry run)")
	verify := flag.Bool("verify", false, "run the invariant check only (read-only)")
	dropUserIndex := flag.Bool("drop-user-index", false, "drop the legacy webEntity user_id_1 unique index (last phase)")
	verbose := flag.Bool("v", false, "list every affected doc (not just the summary)")
	flag.Parse()

	_ = godotenv.Load()

	var cfg config.AppConfig
	cfg.LoadEnvConfig()
	cfg.LoadDatabaseConfig()

	if cfg.Database.URL == "" {
		fail("MONGO_URI is not set")
	}
	if err := models.Connect(cfg.Database.URL, cfg.Database.Name); err != nil {
		fail("connect to mongo: %v", err)
	}

	dryRun := !*apply
	mode := "DRY RUN (no writes)"
	if *apply {
		mode = "APPLY (writing changes)"
	}
	fmt.Printf("companymigrate: %s  db=%s\n", mode, cfg.Database.Name)

	ctx := context.Background()

	if *verify {
		runVerify(ctx)
		return
	}

	if *dropUserIndex {
		pending, err := models.DropWebEntityUserIndex(ctx, dryRun)
		if err != nil {
			fail("drop user index: %v", err)
		}
		switch {
		case pending && dryRun:
			fmt.Println("drop-user-index: legacy index present — re-run with -apply to drop it")
		case pending:
			fmt.Println("drop-user-index: dropped")
		default:
			fmt.Println("drop-user-index: already absent — nothing to do")
		}
		return
	}

	if !dryRun {
		// The D20 index must exist before the mint pass so idempotence is
		// enforced, not hoped for.
		if err := models.EnsureCompanyIndexes(ctx); err != nil {
			fail("ensure company indexes: %v", err)
		}
		if err := models.EnsureCompanyMembershipIndexes(ctx); err != nil {
			fail("ensure companyMembership indexes: %v", err)
		}
		if err := models.EnsureWebEntityIndexes(ctx); err != nil {
			fail("ensure webEntity indexes: %v", err)
		}
	}

	report, err := models.MigrateCompanyTenancy(ctx, dryRun)
	if err != nil {
		fail("migrate: %v", err)
	}
	fmt.Printf("users: seen=%d personal_created=%d personal_existing=%d skipped_no_email=%d\n",
		report.Users, report.PersonalCreated, report.PersonalExisting, len(report.SkippedNoEmail))
	fmt.Printf("webEntities: total=%d stamped=%d already=%d unresolvable=%d missing_user_id=%d\n",
		report.WebEntities, report.WebEntitiesStamped, report.WebEntitiesAlready,
		len(report.WebEntitiesUnresolvable), len(report.WebEntitiesMissingUserID))
	if *verbose {
		for _, id := range report.SkippedNoEmail {
			fmt.Printf("  skipped (no email): user %s\n", id)
		}
	}
	// Anomalies are load-bearing — always surface them.
	for _, id := range report.SkippedNoEmail {
		fmt.Fprintf(os.Stderr, "  NO EMAIL (no personal company minted — fix the user doc first): user %s\n", id)
	}
	for _, id := range report.WebEntitiesUnresolvable {
		fmt.Fprintf(os.Stderr, "  UNRESOLVABLE (owner has no personal company): webEntity %s\n", id)
	}
	for _, id := range report.WebEntitiesMissingUserID {
		fmt.Fprintf(os.Stderr, "  ANOMALY (no user_id, no company_id): webEntity %s\n", id)
	}
	if dryRun && (report.PersonalCreated > 0 || report.WebEntitiesStamped > 0) {
		fmt.Println("re-run with -apply to write these changes")
	}
}

func runVerify(ctx context.Context) {
	report, err := models.VerifyCompanyTenancy(ctx)
	if err != nil {
		fail("verify: %v", err)
	}
	fmt.Printf("verify: webEntities_missing_company=%d users_without_personal=%d legacy_user_index_present=%v\n",
		report.WebEntitiesMissingCompany, len(report.UsersWithoutPersonal), report.LegacyUserIndexPresent)
	for _, email := range report.UsersWithoutPersonal {
		fmt.Fprintf(os.Stderr, "  MISSING personal company: %s\n", email)
	}
	if report.WebEntitiesMissingCompany == 0 && len(report.UsersWithoutPersonal) == 0 {
		fmt.Println("verify: all invariants hold")
		if report.LegacyUserIndexPresent {
			fmt.Println("verify: legacy user_id_1 index still present — drop with -drop-user-index -apply (later deploy)")
		}
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "companymigrate: "+format+"\n", args...)
	os.Exit(1)
}
