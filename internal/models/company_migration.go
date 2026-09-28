package models

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

// CompanyMigrationReport summarizes one MigrateCompanyTenancy run (tenancy
// plan §8 — small on purpose: only SEO users exist in prod).
type CompanyMigrationReport struct {
	Users                    int64    // active users seen
	PersonalCreated          int64    // personal companies minted (or would be)
	PersonalExisting         int64    // users who already had one
	SkippedNoEmail           []string // user ids with no email — hook can't run (D22 assumed fixed)
	WebEntities              int64    // total webEntity docs
	WebEntitiesStamped       int64    // stamped with company_id this run (or would be)
	WebEntitiesAlready       int64    // already carried company_id
	WebEntitiesUnresolvable  []string // webEntity ids whose owner has no personal company
	WebEntitiesMissingUserID []string // webEntity ids with neither company_id nor user_id (anomaly)
}

// MigrateCompanyTenancy is the §8 backfill: (1) every active user gets a
// personal company + claimed owner membership; (2) every webEntity without a
// company_id gets its owner's personal company. Idempotence is index-backed
// (D20): re-runs and races with the signup hook are dup-key no-ops.
func MigrateCompanyTenancy(ctx context.Context, dryRun bool) (*CompanyMigrationReport, error) {
	report := &CompanyMigrationReport{}

	// Pass 1 — personal companies for every active user.
	cur, err := Collection(usersCollection).Find(ctx, activeFilter(bson.M{}))
	if err != nil {
		return report, fmt.Errorf("list users: %w", err)
	}
	personalByUser := map[primitive.ObjectID]primitive.ObjectID{}
	var users []User
	if err := cur.All(ctx, &users); err != nil {
		return report, fmt.Errorf("read users: %w", err)
	}
	for i := range users {
		u := &users[i]
		report.Users++
		if u.Email == "" {
			report.SkippedNoEmail = append(report.SkippedNoEmail, u.ID.Hex())
			continue
		}
		found, existing, err := FindPersonalCompanyByOwner(ctx, u.ID)
		if err != nil {
			return report, fmt.Errorf("find personal company for %s: %w", u.ID.Hex(), err)
		}
		if found {
			report.PersonalExisting++
			personalByUser[u.ID] = existing.ID
			if !dryRun {
				// Heal a missing owner membership even on existing companies.
				if _, err := EnsurePersonalCompany(ctx, u.ID, u.Email, u.Name); err != nil {
					return report, fmt.Errorf("ensure personal company for %s: %w", u.ID.Hex(), err)
				}
			}
			continue
		}
		report.PersonalCreated++
		if dryRun {
			continue
		}
		company, err := EnsurePersonalCompany(ctx, u.ID, u.Email, u.Name)
		if err != nil {
			return report, fmt.Errorf("ensure personal company for %s: %w", u.ID.Hex(), err)
		}
		personalByUser[u.ID] = company.ID
	}

	// Pass 2 — stamp company_id on webEntity docs that predate the field.
	total, err := Collection(webEntityCollection).CountDocuments(ctx, bson.M{})
	if err != nil {
		return report, fmt.Errorf("count webEntity: %w", err)
	}
	report.WebEntities = total

	ecur, err := Collection(webEntityCollection).Find(ctx, bson.M{"company_id": bson.M{"$exists": false}})
	if err != nil {
		return report, fmt.Errorf("list unstamped webEntity: %w", err)
	}
	var entities []WebEntity
	if err := ecur.All(ctx, &entities); err != nil {
		return report, fmt.Errorf("read unstamped webEntity: %w", err)
	}
	report.WebEntitiesAlready = total - int64(len(entities))
	for i := range entities {
		e := &entities[i]
		if e.UserID.IsZero() {
			report.WebEntitiesMissingUserID = append(report.WebEntitiesMissingUserID, e.ID.Hex())
			continue
		}
		companyID, ok := personalByUser[e.UserID]
		if !ok {
			// Owner not seen this run (deactivated, dry-run create, or no
			// email) — re-resolve; dry-run counts a pending create as
			// resolvable.
			found, company, err := FindPersonalCompanyByOwner(ctx, e.UserID)
			switch {
			case err != nil:
				return report, fmt.Errorf("find personal company for entity %s: %w", e.ID.Hex(), err)
			case found:
				companyID = company.ID
			case dryRun:
				// Pass 1 would have minted it on apply — count as stampable
				// unless the owner was skipped for having no email.
				skipped := false
				for _, id := range report.SkippedNoEmail {
					if id == e.UserID.Hex() {
						skipped = true
						break
					}
				}
				if skipped {
					report.WebEntitiesUnresolvable = append(report.WebEntitiesUnresolvable, e.ID.Hex())
					continue
				}
				report.WebEntitiesStamped++
				continue
			default:
				report.WebEntitiesUnresolvable = append(report.WebEntitiesUnresolvable, e.ID.Hex())
				continue
			}
		}
		report.WebEntitiesStamped++
		if dryRun {
			continue
		}
		if err := SetWebEntityCompanyID(ctx, e.ID, companyID); err != nil {
			return report, fmt.Errorf("stamp webEntity %s: %w", e.ID.Hex(), err)
		}
	}
	return report, nil
}

// CompanyTenancyVerifyReport summarizes the §8 verify pass.
type CompanyTenancyVerifyReport struct {
	WebEntitiesMissingCompany int64
	UsersWithoutPersonal      []string // active emailed users with no personal company
	LegacyUserIndexPresent    bool     // webEntity user_id_1 still exists
}

// VerifyCompanyTenancy checks the migration invariants: every webEntity has a
// company_id, and every active emailed user has a personal company (exactly
// one is index-enforced, D20).
func VerifyCompanyTenancy(ctx context.Context) (*CompanyTenancyVerifyReport, error) {
	report := &CompanyTenancyVerifyReport{}

	missing, err := Collection(webEntityCollection).CountDocuments(ctx, bson.M{"company_id": bson.M{"$exists": false}})
	if err != nil {
		return report, fmt.Errorf("count unstamped webEntity: %w", err)
	}
	report.WebEntitiesMissingCompany = missing

	cur, err := Collection(usersCollection).Find(ctx, activeFilter(bson.M{fieldEmail: bson.M{"$nin": bson.A{"", nil}}}))
	if err != nil {
		return report, fmt.Errorf("list users: %w", err)
	}
	var users []User
	if err := cur.All(ctx, &users); err != nil {
		return report, fmt.Errorf("read users: %w", err)
	}
	for i := range users {
		found, _, err := FindPersonalCompanyByOwner(ctx, users[i].ID)
		if err != nil {
			return report, err
		}
		if !found {
			report.UsersWithoutPersonal = append(report.UsersWithoutPersonal, users[i].Email)
		}
	}

	names, err := listIndexNames(ctx, Collection(webEntityCollection))
	if err != nil {
		return report, fmt.Errorf("list webEntity indexes: %w", err)
	}
	report.LegacyUserIndexPresent = names[legacyWebEntityUserIndex]
	return report, nil
}

// legacyWebEntityUserIndex is the pre-restructure plain-unique {user_id}
// index on webEntity (Mongo's auto-generated name).
const legacyWebEntityUserIndex = "user_id_1"

// listIndexNames returns the set of index names on a collection.
func listIndexNames(ctx context.Context, coll *mongo.Collection) (map[string]bool, error) {
	cur, err := coll.Indexes().List(ctx)
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for cur.Next(ctx) {
		var spec bson.M
		if err := cur.Decode(&spec); err != nil {
			return nil, err
		}
		if name, ok := spec["name"].(string); ok {
			names[name] = true
		}
	}
	return names, cur.Err()
}

// DropWebEntityUserIndex is the LAST-phase index drop (§8.3 — separate flag,
// later deploy): removes the legacy unique {user_id} so a user can own
// several companies' entities. Refuses while any webEntity is unstamped.
func DropWebEntityUserIndex(ctx context.Context, dryRun bool) (bool, error) {
	verify, err := VerifyCompanyTenancy(ctx)
	if err != nil {
		return false, err
	}
	if verify.WebEntitiesMissingCompany > 0 {
		return false, fmt.Errorf("refusing to drop %s: %d webEntity docs still have no company_id",
			legacyWebEntityUserIndex, verify.WebEntitiesMissingCompany)
	}
	if !verify.LegacyUserIndexPresent {
		return false, nil
	}
	if dryRun {
		return true, nil
	}
	if _, err := Collection(webEntityCollection).Indexes().DropOne(ctx, legacyWebEntityUserIndex); err != nil {
		var cmdErr mongo.CommandError
		if !errors.As(err, &cmdErr) || cmdErr.Name != "IndexNotFound" {
			return false, fmt.Errorf("drop %s: %w", legacyWebEntityUserIndex, err)
		}
	}
	return true, nil
}
