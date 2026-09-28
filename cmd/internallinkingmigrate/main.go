// Command internallinkingmigrate backfills the internal-linking toggle onto
// documents that predate it: webEntity docs (the web-entity-wide default) and
// pre-generation scheduledArticle docs (the per-article override). Both are set
// to true, matching the on-by-default rule, so existing data renders the right
// toggle state and generates with internal linking on.
//
// It defaults to a dry run: pass -apply to actually write. Connection details
// come from the same MONGO_URI / MONGO_DB_NAME the service uses (a .env file is
// loaded if present).
//
//	go run ./cmd/internallinkingmigrate          # dry run, prints what would change
//	go run ./cmd/internallinkingmigrate -apply    # writes the backfill
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
	fmt.Printf("internallinkingmigrate: %s  db=%s\n", mode, cfg.Database.Name)

	we, sa, err := models.BackfillInternalLinkingDefaults(context.Background(), dryRun)
	if err != nil {
		fail("backfill failed: %v", err)
	}

	verb := "would set"
	if *apply {
		verb = "set"
	}
	fmt.Printf("%s internal_linking_enabled=true on web_entities=%d  scheduled_articles=%d\n", verb, we, sa)
	if dryRun && (we > 0 || sa > 0) {
		fmt.Println("re-run with -apply to write these changes")
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "internallinkingmigrate: "+format+"\n", args...)
	os.Exit(1)
}
