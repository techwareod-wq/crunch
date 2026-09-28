// Command rolesmigrate is the RBAC bootstrap / recovery tool. It seeds the role catalog, promotes explicitly named
// emails to superusers, and backfills legacy user docs missing a role.
//
// Lockout recovery has two hatches:
//   - -seed-admins -emails <csv> promotes the named emails to superuser. It
//     $sets only role/timestamps; it deliberately does NOT touch per-user
//     overrides. With the legacy email allowlist deleted (cutover step 3),
//     this is THE bootstrap path for the first superuser.
//   - -clear-grants -email <addr> wipes a user's extra_grants/extra_revokes.
//     This is the fix for a legacy/hand-crafted admin.access revoke — a lockout
//     that survives a re-seed — so recovery never needs a raw DB edit. (Post-S1
//     the API can no longer create such a revoke, so this is for legacy state.)
//
// Re-runnable by design: every action is idempotent, so a second apply reports
// zero writes (the cutover dry-run gate). It defaults to a dry run; pass -apply
// to write. Migration writes bypass the adminActions audit (they run outside
// the middleware) — intentional; the audit trail starts at the API surface.
// This tool prints a durable summary of every write so the seed is
// reconstructable from ops logs.
//
// Connection details come from the same MONGO_URI / MONGO_DB_NAME the service
// uses (a .env file is loaded if present).
//
//	go run ./cmd/rolesmigrate -seed-roles              # dry run: what roles would be seeded
//	go run ./cmd/rolesmigrate -seed-roles -apply       # write the 3 system roles
//	go run ./cmd/rolesmigrate -seed-admins -emails a@b.com,c@d.com -apply   # promote to superusers
//	go run ./cmd/rolesmigrate -backfill -apply         # default legacy null roles to "user"
//	go run ./cmd/rolesmigrate -clear-grants -email a@b.com -apply   # wipe a user's overrides (lockout recovery)
//	go run ./cmd/rolesmigrate -seed-roles -seed-admins -emails a@b.com -backfill -apply -v   # everything, verbose
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/joho/godotenv"

	"github.com/atharva-ng/crunch/internal/authz"
	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/models"
)

func main() {
	apply := flag.Bool("apply", false, "write the changes (default is a dry run)")
	verbose := flag.Bool("v", false, "list every affected doc (not just the summary)")
	seedRoles := flag.Bool("seed-roles", false, "idempotently upsert the 3 system roles (user/admin/superuser)")
	seedAdmins := flag.Bool("seed-admins", false, "promote the -emails list to role=superuser")
	backfill := flag.Bool("backfill", false, "default any user with an empty role to \"user\"")
	clearGrants := flag.Bool("clear-grants", false, "wipe extra_grants/extra_revokes for -email (lockout recovery)")
	email := flag.String("email", "", "target email for -clear-grants")
	emails := flag.String("emails", "", "CSV of emails for -seed-admins")
	flag.Parse()

	if !*seedRoles && !*seedAdmins && !*backfill && !*clearGrants {
		fail("specify at least one action: -seed-roles, -seed-admins, -backfill, and/or -clear-grants")
	}
	if *clearGrants && *email == "" {
		fail("-clear-grants requires -email <address>")
	}
	adminEmails := splitCSV(*emails)
	if *seedAdmins && len(adminEmails) == 0 {
		fail("-seed-admins requires -emails <a@b.com,c@d.com>")
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

	dryRun := !*apply
	mode := "DRY RUN (no writes)"
	if *apply {
		mode = "APPLY (writing changes)"
	}
	fmt.Printf("rolesmigrate: %s  db=%s\n", mode, cfg.Database.Name)

	ctx := context.Background()
	pending := false

	if *seedRoles {
		pending = runSeedRoles(ctx, dryRun) || pending
	}
	if *seedAdmins {
		pending = runSeedAdmins(ctx, adminEmails, dryRun, *verbose) || pending
	}
	if *backfill {
		pending = runBackfill(ctx, dryRun) || pending
	}
	if *clearGrants {
		pending = runClearGrants(ctx, *email, dryRun) || pending
	}

	if dryRun && pending {
		fmt.Println("re-run with -apply to write these changes")
	}
}

// runSeedRoles upserts the 3 system roles. Returns whether writes are pending.
func runSeedRoles(ctx context.Context, dryRun bool) bool {
	report, err := models.SeedRoles(ctx, authz.DefaultRoles(), dryRun)
	if err != nil {
		fail("seed roles: %v", err)
	}
	fmt.Printf("roles: created=%d updated=%d unchanged=%d\n", report.Created, report.Updated, report.Unchanged)
	return report.Created > 0 || report.Updated > 0
}

// runSeedAdmins promotes the -emails list to superusers. Returns whether
// writes are pending. Unmatched emails (no user doc yet) are always listed so
// the operator knows to re-run after they first sign in.
func runSeedAdmins(ctx context.Context, emails []string, dryRun, verbose bool) bool {
	report, err := models.SeedSuperusersByEmail(ctx, emails, dryRun)
	if err != nil {
		fail("seed admins: %v", err)
	}
	fmt.Printf("superusers: promoted=%d already=%d unmatched=%d (updated=%d)\n",
		len(report.Promoted), len(report.AlreadySuperuser), len(report.Unmatched), report.Updated)
	if verbose {
		for _, e := range report.Promoted {
			fmt.Printf("  promote: %s\n", e)
		}
		for _, e := range report.AlreadySuperuser {
			fmt.Printf("  already superuser: %s\n", e)
		}
	}
	// Unmatched are load-bearing (a would-be superuser who can't be promoted):
	// always surface them, verbose or not.
	for _, e := range report.Unmatched {
		fmt.Fprintf(os.Stderr, "  UNMATCHED (no user doc — re-run after first login): %s\n", e)
	}
	return len(report.Promoted) > 0
}

// runBackfill defaults empty roles to "user". Returns whether writes are pending.
func runBackfill(ctx context.Context, dryRun bool) bool {
	report, err := models.BackfillRoles(ctx, dryRun)
	if err != nil {
		fail("backfill roles: %v", err)
	}
	fmt.Printf("backfill: missing_role=%d backfilled=%d\n", report.MissingRole, report.Backfilled)
	return report.MissingRole > 0
}

// runClearGrants wipes one user's per-user overrides (lockout recovery).
// Returns whether writes are pending. An unmatched email is surfaced on stderr
// (load-bearing: the operator expected a user doc to fix).
func runClearGrants(ctx context.Context, email string, dryRun bool) bool {
	report, err := models.ClearUserOverrides(ctx, email, dryRun)
	if err != nil {
		fail("clear grants: %v", err)
	}
	if !report.Matched {
		fmt.Fprintf(os.Stderr, "clear-grants: no active user doc for %s — nothing to clear\n", email)
		return false
	}
	fmt.Printf("clear-grants: %s grants=%d revokes=%d cleared=%t\n",
		email, report.Grants, report.Revokes, report.Cleared)
	return report.Grants > 0 || report.Revokes > 0
}

// splitCSV splits a comma-separated flag value into trimmed, non-empty entries.
func splitCSV(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "rolesmigrate: "+format+"\n", args...)
	os.Exit(1)
}
