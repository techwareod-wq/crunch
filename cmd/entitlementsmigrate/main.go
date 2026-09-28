// Command entitlementsmigrate is the entitlements backfill / drift-repair tool
// (authorization-entitlements plan §9). It stamps app_id: "indexly" on legacy
// subscriptions, swaps the (user_id, valid_till) index for the app-aware one,
// and recomputes the users.entitlements.indexly projection wherever it has
// drifted from the subscriptions collection.
//
// Re-runnable by design: the cutover gate before the middleware swap is a
// dry run reporting ZERO pending writes.
//
// It defaults to a dry run: pass -apply to actually write. Connection details
// come from the same MONGO_URI / MONGO_DB_NAME the service uses (a .env file
// is loaded if present).
//
//	go run ./cmd/entitlementsmigrate            # dry run, prints what would change
//	go run ./cmd/entitlementsmigrate -apply     # writes the backfill
//	go run ./cmd/entitlementsmigrate -apply -v  # also lists every drifted user
//
// With -seed-plans it instead seeds the plans catalog (idempotent upsert by
// (app_id, tier); the seed file is a JSON array of plan docs in the models.Plan
// wire shape). The seed file is resolved the same way the service resolves its
// values.yaml — from the current ENVIRONMENT — because Paddle price_ids differ
// per Paddle account (integration→sandbox, production→production), so each
// environment carries its own price_ids:
//
//  1. -plans-file <path> flag, if set (explicit override, like VALUES_FILE).
//  2. values/<ENVIRONMENT>/plans.json — which must exist.
//
// Operator note: ENVIRONMENT must match the database you point MONGO_URI /
// MONGO_DB_NAME at, or you will seed one environment's price_ids into another
// environment's catalog. Also dry-run by default:
//
//	ENVIRONMENT=integration go run ./cmd/entitlementsmigrate -seed-plans          # validate + report (integration)
//	ENVIRONMENT=production  go run ./cmd/entitlementsmigrate -seed-plans -apply    # write (production)
//	go run ./cmd/entitlementsmigrate -seed-plans -plans-file custom.json -apply    # explicit seed file
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/joho/godotenv"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/models"
)

func main() {
	apply := flag.Bool("apply", false, "write the backfill (default is a dry run)")
	verbose := flag.Bool("v", false, "list every drifted user (not just the summary)")
	seedPlans := flag.Bool("seed-plans", false, "seed the plans catalog for the current ENVIRONMENT instead of running the backfill")
	plansFile := flag.String("plans-file", "", "explicit seed file path (overrides values/<ENVIRONMENT>/plans.json)")
	flag.Parse()

	_ = godotenv.Load()

	var cfg config.AppConfig
	cfg.LoadEnvConfig()
	if err := config.LoadValues(&cfg); err != nil {
		fail("load values: %v", err)
	}
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
	fmt.Printf("entitlementsmigrate: %s  db=%s\n", mode, cfg.Database.Name)

	if *seedPlans {
		path, err := resolvePlansFile(*plansFile, cfg.Env.Environment)
		if err != nil {
			fail("%v", err)
		}
		runSeed(path, dryRun)
		return
	}

	report, err := models.BackfillEntitlements(context.Background(), dryRun)
	if err != nil {
		fail("backfill failed: %v", err)
	}

	if *verbose {
		for _, id := range report.DriftedUserIDs {
			fmt.Printf("  drifted: %s\n", id)
		}
	}
	for _, e := range report.Errors {
		fmt.Fprintf(os.Stderr, "  error: %s\n", e)
	}

	fmt.Printf("missing_app_id=%d stamped=%d users_scanned=%d drifted=%d recomputed=%d errors=%d\n",
		report.MissingAppID, report.Stamped, report.UsersScanned, report.Drifted, report.Recomputed, len(report.Errors))
	if dryRun && (report.MissingAppID > 0 || report.Drifted > 0) {
		fmt.Println("re-run with -apply to write these changes")
	}
	if len(report.Errors) > 0 {
		os.Exit(1)
	}
}

// resolvePlansFile picks the seed file, mirroring config.LoadValues' resolution
// order for values.yaml: an explicit -plans-file wins; otherwise the file is
// taken from the current environment. An empty environment, or an environment
// with no seed file on disk, is a configuration error — there is no fallback,
// since seeding another environment's price_ids is exactly the mistake the
// per-environment split exists to prevent.
func resolvePlansFile(explicit, environment string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if environment == "" {
		return "", fmt.Errorf("cannot resolve seed file: ENVIRONMENT is empty")
	}
	path := filepath.Join("values", environment, "plans.json")
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("no seed file for environment %q (expected %s): %w", environment, path, err)
	}
	return path, nil
}

func runSeed(path string, dryRun bool) {
	fmt.Printf("seeding plans from %s\n", path)
	raw, err := os.ReadFile(path)
	if err != nil {
		fail("read seed file: %v", err)
	}
	var plans []models.Plan
	if err := json.Unmarshal(raw, &plans); err != nil {
		fail("parse seed file (expect a JSON array of plan docs): %v", err)
	}
	if len(plans) == 0 {
		fail("seed file contains no plans")
	}

	report, err := models.SeedPlans(context.Background(), plans, dryRun)
	if err != nil {
		fail("seed plans: %v", err)
	}
	fmt.Printf("plans: created=%d updated=%d unchanged=%d\n", report.Created, report.Updated, report.Unchanged)
	if dryRun && (report.Created > 0 || report.Updated > 0) {
		fmt.Println("re-run with -apply to write these changes")
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "entitlementsmigrate: "+format+"\n", args...)
	os.Exit(1)
}
