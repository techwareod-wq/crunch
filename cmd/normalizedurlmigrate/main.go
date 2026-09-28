// Command normalizedurlmigrate is the one-time backfill for
// publish.normalized_url on webEntityMasterContext (GSC analytics plan Step
// 5): every master context published before the stamp existed gets its
// canonical article URL written, so GSC page rows can be matched to articles
// from day one. New publishes stamp themselves via SetCGEPublishState.
//
// Re-runnable by design (already-stamped docs never match). Defaults to a dry
// run: pass -apply to write. Connection details come from the same MONGO_URI /
// MONGO_DB_NAME the service uses (a .env file is loaded if present).
//
//	go run ./cmd/normalizedurlmigrate           # dry run, prints what would change
//	go run ./cmd/normalizedurlmigrate -apply    # writes the backfill
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
	fmt.Printf("normalizedurlmigrate: %s  db=%s\n", mode, cfg.Database.Name)

	report, err := models.BackfillPublishNormalizedURL(context.Background(), dryRun)
	if err != nil {
		fail("backfill failed: %v", err)
	}

	for _, e := range report.Errors {
		fmt.Fprintf(os.Stderr, "  error: %s\n", e)
	}
	fmt.Printf("scanned=%d stamped=%d skipped=%d errors=%d\n",
		report.Scanned, report.Stamped, report.Skipped, len(report.Errors))
	if dryRun && report.Stamped > 0 {
		fmt.Println("re-run with -apply to write these changes")
	}
	if len(report.Errors) > 0 {
		os.Exit(1)
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "normalizedurlmigrate: "+format+"\n", args...)
	os.Exit(1)
}
