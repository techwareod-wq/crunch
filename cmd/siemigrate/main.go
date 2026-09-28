// Command siemigrate is the one-off data-repair / reconcile tool for the SIE
// pipeline idempotency remediation. It recomputes each WebEntityContext's funnel
// display counters from authoritative keyword state (correcting values the old
// non-idempotent $inc inflated past total) and reconciles status forward to the
// furthest stage its persisted artifacts prove it reached.
//
// It defaults to a dry run: pass -apply to actually write. Connection details
// come from the same MONGO_URI / MONGO_DB_NAME the service uses (a .env file is
// loaded if present).
//
//	go run ./cmd/siemigrate            # dry run, prints what would change
//	go run ./cmd/siemigrate -apply     # writes the repairs
//	go run ./cmd/siemigrate -apply -v  # also lists every changed WEC
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
	apply := flag.Bool("apply", false, "write the repairs (default is a dry run)")
	verbose := flag.Bool("v", false, "list every WEC that changed (not just the summary)")
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
	fmt.Printf("siemigrate: %s  db=%s\n", mode, cfg.Database.Name)

	reports, err := models.RepairWebEntityContexts(context.Background(), dryRun)
	if err != nil {
		fail("repair failed after %d WEC(s): %v", len(reports), err)
	}

	var counterChanges, statusChanges int
	for _, r := range reports {
		if r.CountersChanged {
			counterChanges++
		}
		if r.StatusChanged {
			statusChanges++
		}
		if *verbose && (r.CountersChanged || r.StatusChanged) {
			fmt.Printf("  %s: counters %d/%d/%d -> %d/%d/%d  status %d -> %d\n",
				r.WebEntityContextID,
				r.OldProcessed, r.OldFailed, r.OldTotal,
				r.NewProcessed, r.NewFailed, r.NewTotal,
				r.OldStatus, r.NewStatus)
		}
	}

	fmt.Printf("scanned=%d  counter_fixes=%d  status_fixes=%d\n",
		len(reports), counterChanges, statusChanges)
	if dryRun && (counterChanges > 0 || statusChanges > 0) {
		fmt.Println("re-run with -apply to write these changes")
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "siemigrate: "+format+"\n", args...)
	os.Exit(1)
}
