package models

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Membership lifecycle (D3/D13): invited (seat occupied, accept pending) →
// claimed (explicit accept through Clerk login) → archived (removed member —
// never deleted, reclaimable on re-invite).
const (
	CompanyMembershipStatusInvited  = "invited"
	CompanyMembershipStatusClaimed  = "claimed"
	CompanyMembershipStatusArchived = "archived"
)

// CompanyMembership is the M:N join — one doc per (user-or-invited-email,
// company). It absorbs the old embedded CompanySeat lifecycle: a seat IS a
// membership doc. Claiming is an explicit accept step, never a silent
// auto-join (D3): the doc carries a hashed one-time accept token, and the
// claim requires token + authenticated Clerk session + verified matching
// email (D19).
type CompanyMembership struct {
	ID        primitive.ObjectID `bson:"_id,omitempty"`
	CompanyID primitive.ObjectID `bson:"company_id"`
	Email     string             `bson:"email"` // lowercased; set at invite
	// UserID is nil until the invited email logs in and claims.
	UserID *primitive.ObjectID `bson:"user_id,omitempty"`
	Role   string              `bson:"role"`   // "user_admin" | "user_user" (D10)
	Status string              `bson:"status"` // invited | claimed | archived
	// VoiceID is carried over from CompanySeat — the member's voice roster row.
	VoiceID         *primitive.ObjectID `bson:"voice_id,omitempty"`
	InvitedByUserID primitive.ObjectID  `bson:"invited_by_user_id,omitempty"`
	InvitedAt       time.Time           `bson:"invited_at"`
	// AcceptTokenHash is the hashed one-time accept token (D19); rotated on
	// re-invite. An expired token means the admin re-sends — the seat stays
	// occupied.
	AcceptTokenHash      string     `bson:"accept_token_hash,omitempty"`
	AcceptTokenExpiresAt *time.Time `bson:"accept_token_expires_at,omitempty"`
	ClaimedAt            *time.Time `bson:"claimed_at,omitempty"`
	ArchivedAt           *time.Time `bson:"archived_at,omitempty"`
	Ver                  int64      `bson:"ver"`
}

// NormalizeCompanySeatEmail is the membership-matching key: lowercased,
// trimmed. Normalized on BOTH sides of every email comparison (D19).
func NormalizeCompanySeatEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// EnsureCompanyMembershipIndexes creates:
//   - unique {company_id, email} — one membership per email per company
//   - {email, status} — pending-invite surfacing: one indexed plural Find (a
//     user invited to N companies must see all N)
//   - {user_id, status} — "my companies" for the FE switcher and the active-
//     company middleware validation read
func EnsureCompanyMembershipIndexes(ctx context.Context) error {
	indexes := []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "company_id", Value: 1}, {Key: fieldEmail, Value: 1}},
			Options: options.Index().SetUnique(true),
		},
		{Keys: bson.D{{Key: fieldEmail, Value: 1}, {Key: fieldStatus, Value: 1}}},
		{Keys: bson.D{{Key: fieldUserID, Value: 1}, {Key: fieldStatus, Value: 1}}},
		// Sparse-unique accept-token lookup (claimed docs carry no token, so
		// the sparse index skips them) — the accept endpoint resolves the
		// membership from the token alone.
		{
			Keys:    bson.D{{Key: "accept_token_hash", Value: 1}},
			Options: options.Index().SetUnique(true).SetSparse(true),
		},
	}
	if _, err := Collection(companyMembershipCollection).Indexes().CreateMany(ctx, indexes); err != nil {
		return fmt.Errorf("ensure companyMembership indexes: %w", err)
	}
	return nil
}

// InsertCompanyMembership inserts an invite-status membership. The caller has
// already reserved the seat (ReserveCompanySeat, D18) and rolls the counter
// back if this insert fails. Duplicate-key = the email already has a
// membership in this company (the caller re-reads and re-invites instead).
func InsertCompanyMembership(ctx context.Context, m *CompanyMembership) error {
	m.Email = NormalizeCompanySeatEmail(m.Email)
	if m.Email == "" {
		return fmt.Errorf("insert company membership: empty email")
	}
	if m.InvitedAt.IsZero() {
		m.InvitedAt = time.Now().UTC()
	}
	if m.Status == "" {
		m.Status = CompanyMembershipStatusInvited
	}
	if m.Ver == 0 {
		m.Ver = 1
	}
	id, err := InsertOne(ctx, companyMembershipCollection, m)
	if err != nil {
		return err
	}
	m.ID = id
	return nil
}

// IsDuplicateCompanyMembership reports whether err is the unique
// (company_id, email) violation.
func IsDuplicateCompanyMembership(err error) bool {
	return mongo.IsDuplicateKeyError(err)
}

// FindCompanyMembershipByID loads one membership doc.
func FindCompanyMembershipByID(ctx context.Context, id primitive.ObjectID) (bool, *CompanyMembership, error) {
	var m CompanyMembership
	found, err := FindOne(ctx, companyMembershipCollection, bson.M{fieldID: id}, &m)
	if err != nil || !found {
		return false, nil, err
	}
	return true, &m, nil
}

// FindCompanyMembershipByEmail resolves the (company, email) membership —
// the unique-index key.
func FindCompanyMembershipByEmail(ctx context.Context, companyID primitive.ObjectID, email string) (bool, *CompanyMembership, error) {
	var m CompanyMembership
	found, err := FindOne(ctx, companyMembershipCollection, bson.M{
		"company_id": companyID,
		fieldEmail:   NormalizeCompanySeatEmail(email),
	}, &m)
	if err != nil || !found {
		return false, nil, err
	}
	return true, &m, nil
}

// FindCompanyMembershipByAcceptTokenHash resolves an INVITED membership from
// its live accept token (the accept endpoint's lookup — sparse-unique index).
// Expiry is checked by the claim write, not here; the peek only needs to know
// which invite the token names.
func FindCompanyMembershipByAcceptTokenHash(ctx context.Context, tokenHash string) (bool, *CompanyMembership, error) {
	if tokenHash == "" {
		return false, nil, nil
	}
	var m CompanyMembership
	found, err := FindOne(ctx, companyMembershipCollection, bson.M{
		"accept_token_hash": tokenHash,
		fieldStatus:         CompanyMembershipStatusInvited,
	}, &m)
	if err != nil || !found {
		return false, nil, err
	}
	return true, &m, nil
}

// FindClaimedMembership validates that user holds a live claimed membership
// in company — the active-company middleware's one indexed read (§4, fails
// closed at the caller).
func FindClaimedMembership(ctx context.Context, userID, companyID primitive.ObjectID) (bool, *CompanyMembership, error) {
	var m CompanyMembership
	found, err := FindOne(ctx, companyMembershipCollection, bson.M{
		fieldUserID:  userID,
		"company_id": companyID,
		fieldStatus:  CompanyMembershipStatusClaimed,
	}, &m)
	if err != nil || !found {
		return false, nil, err
	}
	return true, &m, nil
}

// ListPendingInvitesByEmail surfaces every pending invite for an email — a
// PLURAL Find on the {email, status} index (a user invited to N companies
// must see all N, never a FindOne's arbitrary pick). Feeds the dashboard
// pending-invites nudge and the accept screen; zero hits = zero extra work
// on the common path. The claim itself is the explicit accept endpoint.
func ListPendingInvitesByEmail(ctx context.Context, email string) ([]CompanyMembership, error) {
	cur, err := Collection(companyMembershipCollection).Find(ctx, bson.M{
		fieldEmail:  NormalizeCompanySeatEmail(email),
		fieldStatus: CompanyMembershipStatusInvited,
	})
	if err != nil {
		return nil, err
	}
	var out []CompanyMembership
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ListClaimedMembershipsByUser returns the user's claimed memberships — the
// FE switcher's "my companies" and the chat swap command's source of truth.
func ListClaimedMembershipsByUser(ctx context.Context, userID primitive.ObjectID) ([]CompanyMembership, error) {
	cur, err := Collection(companyMembershipCollection).Find(ctx, bson.M{
		fieldUserID: userID,
		fieldStatus: CompanyMembershipStatusClaimed,
	})
	if err != nil {
		return nil, err
	}
	var out []CompanyMembership
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ListClaimedMembershipCompanyIDs returns the IDs of every company the user
// holds a CLAIMED membership in — the company half of the entitlement
// recompute derivation (invited/archived memberships grant nothing).
func ListClaimedMembershipCompanyIDs(ctx context.Context, userID primitive.ObjectID) ([]primitive.ObjectID, error) {
	memberships, err := ListClaimedMembershipsByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	ids := make([]primitive.ObjectID, 0, len(memberships))
	for i := range memberships {
		ids = append(ids, memberships[i].CompanyID)
	}
	return ids, nil
}

// ListClaimedMembershipUserIDs returns the user IDs of every claimed member of
// a company — the webhook fan-out set when the company's subscription changes
// (bounded: claimed members ≤ purchased seats, plus the owner).
func ListClaimedMembershipUserIDs(ctx context.Context, companyID primitive.ObjectID) ([]primitive.ObjectID, error) {
	cur, err := Collection(companyMembershipCollection).Find(ctx, bson.M{
		"company_id": companyID,
		fieldStatus:  CompanyMembershipStatusClaimed,
	})
	if err != nil {
		return nil, err
	}
	var memberships []CompanyMembership
	if err := cur.All(ctx, &memberships); err != nil {
		return nil, err
	}
	ids := make([]primitive.ObjectID, 0, len(memberships))
	for i := range memberships {
		if memberships[i].UserID != nil {
			ids = append(ids, *memberships[i].UserID)
		}
	}
	return ids, nil
}

// ListCompanyMemberships returns every membership doc for a company (all
// statuses — the dashboard member list shows invited/claimed/archived).
func ListCompanyMemberships(ctx context.Context, companyID primitive.ObjectID) ([]CompanyMembership, error) {
	cur, err := Collection(companyMembershipCollection).Find(ctx,
		bson.M{"company_id": companyID},
		options.Find().SetSort(bson.D{{Key: "invited_at", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var out []CompanyMembership
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ClaimCompanyMembership is the accept write (D19): CAS per doc — set
// user_id/claimed_at, status invited→claimed, ONLY where status is still
// invited, the token hash matches, and the token is unexpired. Each
// membership flips exactly once under racing accepts; claimed=false covers
// wrong/expired/reused tokens uniformly (the caller distinguishes by
// re-reading when it needs better copy).
func ClaimCompanyMembership(ctx context.Context, membershipID, userID primitive.ObjectID, tokenHash string) (bool, error) {
	if tokenHash == "" {
		return false, fmt.Errorf("claim company membership: empty token hash")
	}
	now := time.Now().UTC()
	res, err := Collection(companyMembershipCollection).UpdateOne(ctx,
		bson.M{
			fieldID:                   membershipID,
			fieldStatus:               CompanyMembershipStatusInvited,
			"accept_token_hash":       tokenHash,
			"accept_token_expires_at": bson.M{"$gt": now},
		},
		bson.M{
			"$set": bson.M{
				fieldUserID:  userID,
				fieldStatus:  CompanyMembershipStatusClaimed,
				"claimed_at": now,
			},
			"$unset": bson.M{
				"accept_token_hash":       "",
				"accept_token_expires_at": "",
			},
			"$inc": bson.M{"ver": 1},
		},
	)
	if err != nil {
		return false, fmt.Errorf("claim company membership %s: %w", membershipID.Hex(), err)
	}
	return res.ModifiedCount > 0, nil
}

// RotateCompanyMembershipAcceptToken re-arms the accept token on an invited
// OR archived membership (re-send, and the D13 re-invite path: an archived
// doc flips back to invited, keeping its identity so the member's entity
// reconnects on claim). Refuses claimed docs — there is nothing to accept.
func RotateCompanyMembershipAcceptToken(ctx context.Context, membershipID primitive.ObjectID, tokenHash string, expiresAt time.Time, invitedBy primitive.ObjectID) error {
	if tokenHash == "" {
		return fmt.Errorf("rotate accept token: empty token hash")
	}
	now := time.Now().UTC()
	res, err := Collection(companyMembershipCollection).UpdateOne(ctx,
		bson.M{
			fieldID:     membershipID,
			fieldStatus: bson.M{"$in": bson.A{CompanyMembershipStatusInvited, CompanyMembershipStatusArchived}},
		},
		bson.M{
			"$set": bson.M{
				fieldStatus:               CompanyMembershipStatusInvited,
				"accept_token_hash":       tokenHash,
				"accept_token_expires_at": expiresAt.UTC(),
				"invited_by_user_id":      invitedBy,
				"invited_at":              now,
			},
			"$unset": bson.M{"archived_at": ""},
			"$inc":   bson.M{"ver": 1},
		},
	)
	if err != nil {
		return fmt.Errorf("rotate accept token %s: %w", membershipID.Hex(), err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("rotate accept token %s: membership not invitable", membershipID.Hex())
	}
	return nil
}

// ArchiveCompanyMembership archives a membership (D13 — never deleted; the
// member's entity/sessions stay, gated off by the claimed-only resolver).
// The caller enforces the D21 owner guard (the owner's membership is never
// archivable) and decrements the seat counter on success. archived=false
// means the doc was not in an archivable state (already archived, or
// missing).
func ArchiveCompanyMembership(ctx context.Context, membershipID primitive.ObjectID) (bool, error) {
	now := time.Now().UTC()
	res, err := Collection(companyMembershipCollection).UpdateOne(ctx,
		bson.M{
			fieldID:     membershipID,
			fieldStatus: bson.M{"$in": bson.A{CompanyMembershipStatusInvited, CompanyMembershipStatusClaimed}},
		},
		bson.M{
			"$set": bson.M{
				fieldStatus:   CompanyMembershipStatusArchived,
				"archived_at": now,
			},
			"$unset": bson.M{
				"accept_token_hash":       "",
				"accept_token_expires_at": "",
			},
			"$inc": bson.M{"ver": 1},
		},
	)
	if err != nil {
		return false, fmt.Errorf("archive company membership %s: %w", membershipID.Hex(), err)
	}
	return res.ModifiedCount > 0, nil
}

// SetCompanyMembershipRole changes the member's company role key. The caller
// enforces the D21 owner floor (the owner can never sit below user_admin).
func SetCompanyMembershipRole(ctx context.Context, membershipID primitive.ObjectID, role string) error {
	return UpdateOne(ctx, companyMembershipCollection,
		bson.M{fieldID: membershipID},
		bson.M{"$set": bson.M{"role": role}, "$inc": bson.M{"ver": 1}},
	)
}

// SetCompanyMembershipVoice links the membership to its voice roster row.
func SetCompanyMembershipVoice(ctx context.Context, membershipID, voiceID primitive.ObjectID) error {
	return UpdateOne(ctx, companyMembershipCollection,
		bson.M{fieldID: membershipID},
		bson.M{"$set": bson.M{fieldVoiceID: voiceID}, "$inc": bson.M{"ver": 1}},
	)
}

// ensureOwnerMembership upserts the owner's claimed user_admin membership —
// EnsurePersonalCompany's second half, keyed on the unique (company, email)
// index so re-runs and races are no-ops.
func ensureOwnerMembership(ctx context.Context, company *Company, userID primitive.ObjectID, email string) error {
	norm := NormalizeCompanySeatEmail(email)
	if norm == "" {
		return fmt.Errorf("ensure owner membership %s: empty email", company.ID.Hex())
	}
	now := time.Now().UTC()
	_, err := Collection(companyMembershipCollection).UpdateOne(ctx,
		bson.M{"company_id": company.ID, fieldEmail: norm},
		bson.M{
			"$setOnInsert": bson.M{
				fieldUserID:          userID,
				"role":               CompanyRoleKeyAdmin,
				fieldStatus:          CompanyMembershipStatusClaimed,
				"invited_by_user_id": userID,
				"invited_at":         now,
				"claimed_at":         now,
				"ver":                int64(1),
			},
		},
		options.Update().SetUpsert(true),
	)
	if err != nil {
		return fmt.Errorf("ensure owner membership %s: %w", company.ID.Hex(), err)
	}
	return nil
}
