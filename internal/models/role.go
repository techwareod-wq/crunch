package models

import (
	"context"
	"fmt"
	"slices"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Role key constants for the seeded system roles. The strings are the single
// source of truth for role identity: user docs store Role == one of these keys
// (or a custom key created via the API). The permission *bundles* live in the
// authz package (which references authz.Permission constants); models only
// needs the keys for count queries and seed identity.
const (
	RoleKeyUser      = "user"
	RoleKeyEditor    = "editor"
	RoleKeyApprover  = "approver"
	RoleKeySuperuser = "superuser"
	// RoleKeyLegacyAdmin is crunch's retired `admin` role (replaced by
	// approver, D-013). Only RetireLegacyAdminRole references it.
	RoleKeyLegacyAdmin = "admin"
)

// Role is one entry of the DB-curated role catalog (RBAC plan §2.1). Roles
// carry a coarse rank (the assignment ceiling) AND a fine-grained permission
// set (what routes gate on). Permission *keys* are code-defined (authz); the
// role→perm *bundles* are data, so a new staff role needs no redeploy.
//
// JSON tags mirror the bson names so a role doc reads the same shape mongo
// stores.
type Role struct {
	ID          primitive.ObjectID `bson:"_id,omitempty"  json:"id,omitempty"`
	Key         string             `bson:"key"            json:"key"`         // unique: "superuser","admin","user"
	Rank        int                `bson:"rank"           json:"rank"`        // coarse ordering (assignment ceiling)
	Permissions []string           `bson:"permissions"    json:"permissions"` // fine-grained keys; "*" = all
	Description string             `bson:"description"    json:"description"`
	System      bool               `bson:"system"         json:"system"` // seeded built-in — protected from delete
	CreatedAt   time.Time          `bson:"created_at"     json:"created_at"`
	UpdatedAt   time.Time          `bson:"updated_at"     json:"updated_at"`
}

// EnsureRoleIndexes creates the unique index on key. Boot fails on error like
// the other Ensure helpers — without it two writes could race duplicate role
// docs, which would make RolesCache reject the whole catalog. Idempotent;
// mirrors EnsureUserIndexes / EnsurePlanIndexes.
func EnsureRoleIndexes(ctx context.Context) error {
	unique := true
	_, err := Collection(rolesCollection).Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "key", Value: 1}},
		Options: &options.IndexOptions{Unique: &unique},
	})
	if err != nil {
		return fmt.Errorf("ensure role indexes: %w", err)
	}
	return nil
}

// ListAllRoles returns every role doc (the RolesCache load + the admin catalog
// listing). Sorted by rank descending so the catalog reads top-down.
func ListAllRoles(ctx context.Context) ([]Role, error) {
	cur, err := Collection(rolesCollection).Find(ctx, bson.M{},
		options.Find().SetSort(bson.D{{Key: "rank", Value: -1}, {Key: "key", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var roles []Role
	if err := cur.All(ctx, &roles); err != nil {
		return nil, err
	}
	return roles, nil
}

// FindRoleByKey loads one role doc.
func FindRoleByKey(ctx context.Context, key string) (bool, *Role, error) {
	var role Role
	found, err := FindOne(ctx, rolesCollection, bson.M{"key": key}, &role)
	if err != nil || !found {
		return false, nil, err
	}
	return true, &role, nil
}

// CreateRole inserts a brand-new custom role. A duplicate key (unique index)
// returns ErrRoleKeyExists so the caller can 409 instead of surfacing a raw
// mongo error.
func CreateRole(ctx context.Context, r *Role) error {
	now := time.Now().UTC()
	r.CreatedAt = now
	r.UpdatedAt = now
	id, err := InsertOne(ctx, rolesCollection, r)
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return ErrRoleKeyExists
		}
		return err
	}
	r.ID = id
	return nil
}

// UpdateRole edits an existing role's rank/permissions/description under an
// optimistic-concurrency precondition on updated_at (mirrors SetUserOverrides).
// The key is immutable (identity), so it is only a filter, never a $set. A
// stale expected → ErrRoleConflict; a missing role → error.
func UpdateRole(ctx context.Context, r *Role, expected *time.Time) error {
	now := time.Now().UTC()
	filter := bson.M{"key": r.Key}
	if expected != nil {
		filter[fieldUpdatedAt] = expected.UTC()
	} else {
		filter[fieldUpdatedAt] = nil
	}
	res, err := Collection(rolesCollection).UpdateOne(ctx, filter, bson.M{"$set": bson.M{
		"rank":         r.Rank,
		"permissions":  r.Permissions,
		"description":  r.Description,
		fieldUpdatedAt: now,
	}})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		var existing Role
		found, ferr := FindOne(ctx, rolesCollection, bson.M{"key": r.Key}, &existing)
		if ferr != nil {
			return ferr
		}
		if !found {
			return fmt.Errorf("update role: role %q not found", r.Key)
		}
		return ErrRoleConflict
	}
	r.UpdatedAt = now
	return nil
}

// DeleteRoleByKey removes a role doc. Callers must enforce the "not a system
// role" and "no user holds it" guards first — this helper trusts its input.
func DeleteRoleByKey(ctx context.Context, key string) error {
	return DeleteOne(ctx, rolesCollection, bson.M{"key": key})
}

// CountUsersWithRole counts active (non-deactivated) users holding roleKey.
// Backs the role-deletion guard (refuse if any user still holds it) — a
// deactivated tombstone isn't a live holder, so activeFilter is applied.
func CountUsersWithRole(ctx context.Context, roleKey string) (int64, error) {
	return Collection(usersCollection).CountDocuments(ctx, activeFilter(bson.M{"role": roleKey}))
}

// CountActiveSuperusers counts active users whose role == superuser. Because
// the superuser-tier permissions (roles.write / "*") may live ONLY on the
// immutable superuser role (authz containment guard), the set of recovery
// holders is exactly this count — no permission-resolution query is needed
// (RBAC plan §7.6). Backs the last-holder lockout guard.
func CountActiveSuperusers(ctx context.Context) (int64, error) {
	return Collection(usersCollection).CountDocuments(ctx, activeFilter(bson.M{"role": RoleKeySuperuser}))
}

// RoleSeedReport summarizes one SeedRoles run (mirrors PlanSeedReport).
type RoleSeedReport struct {
	Created   int
	Updated   int
	Unchanged int
}

// SeedRoles idempotently upserts the system role catalog keyed by `key` — the
// cmd/rolesmigrate -seed-roles step. Docs identical to what's stored are
// skipped, so a second apply reports zero writes (the cutover dry-run gate).
// Mirrors SeedPlans.
func SeedRoles(ctx context.Context, incoming []Role, dryRun bool) (*RoleSeedReport, error) {
	existing, err := ListAllRoles(ctx)
	if err != nil {
		return nil, err
	}
	byKey := make(map[string]*Role, len(existing))
	for i := range existing {
		byKey[existing[i].Key] = &existing[i]
	}

	report := &RoleSeedReport{}
	if !dryRun {
		if err := EnsureRoleIndexes(ctx); err != nil {
			return nil, err
		}
	}
	now := time.Now().UTC()
	for i := range incoming {
		r := &incoming[i]
		cur, exists := byKey[r.Key]
		switch {
		case exists && roleSpecEqual(cur, r):
			report.Unchanged++
			continue
		case exists:
			report.Updated++
		default:
			report.Created++
		}
		if dryRun {
			continue
		}
		_, err := Collection(rolesCollection).UpdateOne(ctx,
			bson.M{"key": r.Key},
			bson.M{
				"$set": bson.M{
					"rank":         r.Rank,
					"permissions":  r.Permissions,
					"description":  r.Description,
					"system":       r.System,
					fieldUpdatedAt: now,
				},
				"$setOnInsert": bson.M{fieldCreatedAt: now},
			},
			options.Update().SetUpsert(true),
		)
		if err != nil {
			return report, fmt.Errorf("upsert role %s: %w", r.Key, err)
		}
	}
	return report, nil
}

// roleSpecEqual compares the seedable fields (everything but timestamps/id).
func roleSpecEqual(a, b *Role) bool {
	return a.Rank == b.Rank &&
		a.Description == b.Description &&
		a.System == b.System &&
		slices.Equal(a.Permissions, b.Permissions)
}

// SuperuserSeedReport summarizes one SeedSuperusersByEmail run.
type SuperuserSeedReport struct {
	Promoted         []string // emails resolved to an active user and promoted (or would be)
	AlreadySuperuser []string // already role=superuser — no write
	Unmatched        []string // no active user doc yet (promote after they first sign in)
	Updated          int      // docs actually written (apply only)
}

// SeedSuperusersByEmail promotes active users whose email is in emails to
// role=superuser — the cmd/rolesmigrate -seed-admins bootstrap. Idempotent.
// Migration-only: it bypasses the adminActions audit and the role_updated_at
// CAS (both live at the API surface). Emails with no user doc yet are reported
// unmatched so the operator re-runs after they first log in (RBAC plan §8).
func SeedSuperusersByEmail(ctx context.Context, emails []string, dryRun bool) (*SuperuserSeedReport, error) {
	report := &SuperuserSeedReport{}
	for _, email := range emails {
		found, user, err := FindUserByEmail(ctx, email)
		if err != nil {
			return report, fmt.Errorf("lookup %s: %w", email, err)
		}
		if !found {
			report.Unmatched = append(report.Unmatched, email)
			continue
		}
		if user.Role == RoleKeySuperuser {
			report.AlreadySuperuser = append(report.AlreadySuperuser, email)
			continue
		}
		report.Promoted = append(report.Promoted, email)
		if dryRun {
			continue
		}
		now := time.Now().UTC()
		if _, err := Collection(usersCollection).UpdateOne(ctx,
			activeFilter(bson.M{fieldID: user.ID}),
			bson.M{"$set": bson.M{"role": RoleKeySuperuser, "role_updated_at": now, fieldUpdatedAt: now}},
		); err != nil {
			return report, fmt.Errorf("promote %s: %w", email, err)
		}
		report.Updated++
	}
	return report, nil
}

// UserOverrideClearReport summarizes one ClearUserOverrides run.
type UserOverrideClearReport struct {
	Matched bool // an active user doc was found for the email
	Grants  int  // extra_grants entries that were (or would be) cleared
	Revokes int  // extra_revokes entries that were (or would be) cleared
	Cleared bool // a write happened (apply only, and only when there was something to clear)
}

// ClearUserOverrides wipes extra_grants / extra_revokes for one user — the
// cmd/rolesmigrate -clear-grants lockout-recovery hatch (RBAC plan §8). Since
// S1 removed the only API ingress for an admin.access revoke, this is for
// legacy or hand-crafted lockouts: a per-user override the -seed-admins re-seed
// intentionally does not touch. Idempotent (a user with no overrides is a
// no-op) and dry-run reports without writing. Migration-only: it bypasses the
// role_updated_at CAS and the adminActions audit, like the other seed helpers.
func ClearUserOverrides(ctx context.Context, email string, dryRun bool) (*UserOverrideClearReport, error) {
	report := &UserOverrideClearReport{}
	found, user, err := FindUserByEmail(ctx, email)
	if err != nil {
		return report, fmt.Errorf("lookup %s: %w", email, err)
	}
	if !found {
		return report, nil
	}
	report.Matched = true
	report.Grants = len(user.ExtraGrants)
	report.Revokes = len(user.ExtraRevokes)
	if report.Grants == 0 && report.Revokes == 0 {
		return report, nil // nothing to clear — idempotent no-op
	}
	if dryRun {
		return report, nil
	}
	now := time.Now().UTC()
	if _, err := Collection(usersCollection).UpdateOne(ctx,
		activeFilter(bson.M{fieldID: user.ID}),
		bson.M{
			"$unset": bson.M{"extra_grants": "", "extra_revokes": ""},
			"$set":   bson.M{fieldUpdatedAt: now},
		},
	); err != nil {
		return report, fmt.Errorf("clear overrides %s: %w", email, err)
	}
	report.Cleared = true
	return report, nil
}

// RoleBackfillReport summarizes one BackfillRoles run.
type RoleBackfillReport struct {
	MissingRole int64 // users with an empty/absent role
	Backfilled  int64 // set to the default role (apply only)
}

// BackfillRoles ensures every user has a non-empty role, defaulting legacy
// nulls to "user". New users already default via CreateUser; this catches rows
// that predate the field. Idempotent — a second apply reports zero writes.
func BackfillRoles(ctx context.Context, dryRun bool) (*RoleBackfillReport, error) {
	report := &RoleBackfillReport{}
	filter := bson.M{"$or": []bson.M{
		{"role": bson.M{"$exists": false}},
		{"role": ""},
		{"role": nil},
	}}
	count, err := Collection(usersCollection).CountDocuments(ctx, filter)
	if err != nil {
		return report, fmt.Errorf("count users missing role: %w", err)
	}
	report.MissingRole = count
	if !dryRun && count > 0 {
		res, err := Collection(usersCollection).UpdateMany(ctx, filter,
			bson.M{"$set": bson.M{"role": defaultUserRole, fieldUpdatedAt: time.Now().UTC()}})
		if err != nil {
			return report, fmt.Errorf("backfill roles: %w", err)
		}
		report.Backfilled = res.ModifiedCount
	}
	return report, nil
}

// LegacyAdminRetireReport summarizes one RetireLegacyAdminRole run.
type LegacyAdminRetireReport struct {
	AdminUsers  int64 // active users still holding role=admin
	Migrated    int64 // moved to approver (apply only)
	RoleDocLeft bool  // the admin role doc exists (before this run)
	RoleDeleted bool  // the admin role doc was deleted (apply only)
}

// RetireLegacyAdminRole moves every user holding crunch's retired `admin` role
// to `approver` and then deletes the `admin` role doc (D-013). The approver
// role must already be seeded — the caller runs -seed-roles first. Idempotent:
// a second apply reports zero writes. Migration-only: it bypasses the
// adminActions audit and the role_updated_at CAS, like the other seed helpers.
// Deactivated users are migrated too so a tombstone never points at a
// missing role.
func RetireLegacyAdminRole(ctx context.Context, dryRun bool) (*LegacyAdminRetireReport, error) {
	report := &LegacyAdminRetireReport{}
	filter := bson.M{"role": RoleKeyLegacyAdmin}
	count, err := Collection(usersCollection).CountDocuments(ctx, filter)
	if err != nil {
		return report, fmt.Errorf("count admin users: %w", err)
	}
	report.AdminUsers = count

	found, _, err := FindRoleByKey(ctx, RoleKeyLegacyAdmin)
	if err != nil {
		return report, fmt.Errorf("lookup admin role: %w", err)
	}
	report.RoleDocLeft = found
	if dryRun {
		return report, nil
	}

	if found, _, err := FindRoleByKey(ctx, RoleKeyApprover); err != nil || !found {
		if err == nil {
			err = fmt.Errorf("approver role is not seeded — run -seed-roles first")
		}
		return report, err
	}
	if count > 0 {
		now := time.Now().UTC()
		res, err := Collection(usersCollection).UpdateMany(ctx, filter,
			bson.M{"$set": bson.M{"role": RoleKeyApprover, "role_updated_at": now, fieldUpdatedAt: now}})
		if err != nil {
			return report, fmt.Errorf("migrate admin users: %w", err)
		}
		report.Migrated = res.ModifiedCount
	}
	if report.RoleDocLeft {
		if err := DeleteRoleByKey(ctx, RoleKeyLegacyAdmin); err != nil {
			return report, fmt.Errorf("delete admin role: %w", err)
		}
		report.RoleDeleted = true
	}
	return report, nil
}
