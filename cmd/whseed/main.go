// Command whseed loads WarehouseHub's day-one data (D-043): the attribute tree
// and the industry rules. Missing keys are inserted; existing keys are never
// touched, so it is safe to re-run and never overwrites an admin's edits. It
// is the CLI twin of POST /v1/admin/attributes/seed. Pincodes (SR-04) will be
// added here as another flag.
//
// It defaults to a dry run; pass -apply to write. Running services pick up the
// new rulesVersion on their next cache refresh (values
// warehousehub.attributes.cacheRefreshSeconds). No recompute is dispatched:
// on a fresh environment there are no live warehouses yet.
//
// Connection details come from the same MONGO_URI / MONGO_DB_NAME the service
// uses (a .env file is loaded if present).
//
//	go run ./cmd/whseed -tree          # dry run: what would be created
//	go run ./cmd/whseed -tree -apply   # write
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/joho/godotenv"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/models"
	"github.com/atharva-ng/crunch/internal/modules/attributes"
	"github.com/atharva-ng/crunch/internal/warehousehub/domain"
)

func main() {
	apply := flag.Bool("apply", false, "write the changes (default is a dry run)")
	tree := flag.Bool("tree", false, "seed the attribute tree and industries")
	flag.Parse()
	if !*tree {
		fail("specify at least one action: -tree")
	}

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

	mode := "DRY RUN (no writes)"
	if *apply {
		mode = "APPLY (writing changes)"
	}
	fmt.Printf("whseed: %s  db=%s\n", mode, cfg.Database.Name)
	ctx := context.Background()

	if *tree {
		if *apply {
			if err := attributes.New(&config.AppContext{}).EnsureIndexes(ctx); err != nil {
				fail("ensure indexes: %v", err)
			}
		}
		rep, err := attributes.NewSeedService().Seed(ctx, domain.Actor{Email: "whseed"}, *apply)
		if err != nil {
			fail("seed tree: %v", err)
		}
		fmt.Printf("tree: create %d defs (%d exist), %d industries (%d exist); rulesVersion=%d\n",
			len(rep.CreatedDefs), len(rep.ExistingDefs), len(rep.CreatedIndustries), len(rep.ExistingIndustries), rep.RulesVersion)
		if len(rep.CreatedDefs) > 0 {
			fmt.Println("  defs:", strings.Join(rep.CreatedDefs, ", "))
		}
		if len(rep.CreatedIndustries) > 0 {
			fmt.Println("  industries:", strings.Join(rep.CreatedIndustries, ", "))
		}
		if !*apply && len(rep.CreatedDefs)+len(rep.CreatedIndustries) > 0 {
			fmt.Println("re-run with -apply to write these changes")
		}
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "whseed: "+format+"\n", args...)
	os.Exit(1)
}
