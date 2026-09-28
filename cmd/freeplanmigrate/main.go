// Command freeplanmigrate is the one-time backfill for the free-plan model
// (trial never lapses by date; a monotonic lifetime article counter backs the
// 10-article cap). It initialises each web entity's
// lifetime_articles_generated from its live generation-started articles,
// marks those articles generation_counted, pushes every live card-less
// trial's valid_till to the far-future horizon, and recomputes the affected
// entitlement projections. Previously-expired trial users regain access,
// capped by the counter.
//
// Re-runnable by design ($max counter writes, idempotent flagging). Defaults
// to a dry run: pass -apply to write. Connection details come from the same
// MONGO_URI / MONGO_DB_NAME the service uses (a .env file is loaded if
// present).
//
//	go run ./cmd/freeplanmigrate           # dry run, prints what would change
//	go run ./cmd/freeplanmigrate -apply    # writes the backfill
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
	apply := flag.Bool("apply", false, "write the backfill (default is a dry run)")
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
	fmt.Printf("freeplanmigrate: %s  db=%s\n", mode, cfg.Database.Name)

	report, err := models.BackfillFreePlan(context.Background(), dryRun)
	if err != nil {
		fail("backfill failed: %v", err)
	}

	for _, e := range report.Errors {
		fmt.Fprintf(os.Stderr, "  error: %s\n", e)
	}
	fmt.Printf("entities_scanned=%d counters_init=%d articles_flagged=%d trials_extended=%d recomputed=%d errors=%d\n",
		report.EntitiesScanned, report.CountersInit, report.ArticlesFlagged,
		report.TrialsExtended, report.Recomputed, len(report.Errors))
	if dryRun && (report.CountersInit > 0 || report.ArticlesFlagged > 0 || report.TrialsExtended > 0) {
		fmt.Println("re-run with -apply to write these changes")
	}
	if len(report.Errors) > 0 {
		os.Exit(1)
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "freeplanmigrate: "+format+"\n", args...)
	os.Exit(1)
}
