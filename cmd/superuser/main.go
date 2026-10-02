// Command superuser promotes signed-in users to superuser — the bootstrap for
// the first superuser and the recovery path if the panel is ever locked out.
// Superusers can't be created or changed through the API.
//
// The person must have signed in once (so a user doc exists); unmatched emails
// are reported. Defaults to a dry run; pass -apply to write. Uses the same
// MONGO_URI / MONGO_DB_NAME as the service (a .env file is loaded if present).
//
//	go run ./cmd/superuser -emails a@b.com,c@d.com          # dry run
//	go run ./cmd/superuser -emails a@b.com,c@d.com -apply   # write
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
)

func main() {
	apply := flag.Bool("apply", false, "write the changes (default is a dry run)")
	emailsCSV := flag.String("emails", "", "CSV of emails to promote")
	flag.Parse()

	var emails []string
	for _, e := range strings.Split(*emailsCSV, ",") {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			emails = append(emails, e)
		}
	}
	if len(emails) == 0 {
		fail("-emails <a@b.com,c@d.com> is required")
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
	fmt.Printf("superuser: %s  db=%s\n", mode, cfg.Database.Name)

	rep, err := models.PromoteSuperusers(context.Background(), emails, !*apply)
	if err != nil {
		fail("%v", err)
	}
	fmt.Printf("promote: %v\nalready superuser: %v\nnot found (sign in first, then re-run): %v\nwritten: %d\n",
		rep.Promoted, rep.AlreadySuperuser, rep.Unmatched, rep.Updated)
	if !*apply && len(rep.Promoted) > 0 {
		fmt.Println("re-run with -apply to write these changes")
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "superuser: "+format+"\n", args...)
	os.Exit(1)
}
