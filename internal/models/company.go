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

// Company kinds. Every user gets a personal company minted at signup (the
// uniform tenancy root); team companies are created by the company-plan
// purchase flow.
const (
	CompanyKindPersonal = "personal"
	CompanyKindTeam     = "team"
)

// Company role keys stored on membership docs (D10). They live in the roles
// catalog like system roles but are resolved per-(user, company) by
// authz.EffectiveCompanyPermissions — never valid as a global user.Role (D17).
const (
	CompanyRoleKeyAdmin = "user_admin"
	CompanyRoleKeyUser  = "user_user"
)

// companyCASRetries bounds seat-counter compare-and-swap loops.
const companyCASRetries = 5

// ErrNoFreeCompanySeat: the company's purchased seats are all taken
// (invited + claimed memberships both occupy a seat until released).
var ErrNoFreeCompanySeat = fmt.Errorf("no free company seat")

// CompanyBilling carries the company plan's Paddle linkage and seat count.
type CompanyBilling struct {
	PaddleSubscriptionID string `bson:"paddle_subscription_id,omitempty"`
	SeatsPurchased       int    `bson:"seats_purchased"`
}

// Company is the tenancy root for both products (D1): ID-keyed, with domain
// as an optional unique-when-claimed attribute — never identity. A company
// owns one WebEntity (website-derived knowledge) and one company brain
// (user-specified knowledge); users relate to it through companyMembership
// docs (M:N). SeatsUsed is the CAS'd seat-occupancy gate (D18) — the
// membership count query is display/reconciliation only.
type Company struct {
	ID         primitive.ObjectID `bson:"_id,omitempty"`
	Kind       string             `bson:"kind"` // "personal" | "team"
	Name       string             `bson:"name,omitempty"`
	Domain     string             `bson:"domain,omitempty"`      // optional; unique WHEN set
	WebsiteURL string             `bson:"website_url,omitempty"` // learn source; asked at company-plan purchase (D14)
	// OwnerUserID holds the highest company role incl. payment access (D12);
	// ownership transfer is the only way out.
	OwnerUserID primitive.ObjectID `bson:"owner_user_id"`
	Billing     CompanyBilling     `bson:"billing"`
	// SeatsUsed is the D18 occupancy counter (invited + claimed) — THE seat
	// gate, bumped under Ver CAS before a membership insert.
	SeatsUsed int `bson:"seats_used"`
	// TrialedAt is set once on first trial — the company-side re-trial block
	// (D21; the owner-side mark lives on the user's trial bookkeeping).
	TrialedAt *time.Time `bson:"trialed_at,omitempty"`
	Ver       int64      `bson:"ver"`
	CreatedAt time.Time  `bson:"created_at"`
	UpdatedAt time.Time  `bson:"updated_at"`
}

// NormalizeCompanyDomain canonicalizes a claimed domain: lowercased, trimmed,
// scheme/path stripped. Empty in, empty out — domain is optional.
func NormalizeCompanyDomain(domain string) string {
	d := strings.ToLower(strings.TrimSpace(domain))
	d = strings.TrimPrefix(d, "https://")
	d = strings.TrimPrefix(d, "http://")
	d = strings.TrimPrefix(d, "www.")
	if i := strings.IndexAny(d, "/?#"); i >= 0 {
		d = d[:i]
	}
	return d
}

// EnsureCompanyIndexes creates the partial-unique keys of the tenancy root:
// {domain} over docs that claim one (D1 — first-come wins, disputes are a
// manual admin concern), and {owner_user_id} over kind:"personal" docs (D20 —
// the one-personal-company invariant is index-enforced, which makes the
// signup hook and cmd/companymigrate idempotent for free). Idempotent.
func EnsureCompanyIndexes(ctx context.Context) error {
	indexes := []mongo.IndexModel{
		{
			Keys: bson.D{{Key: "domain", Value: 1}},
			Options: options.Index().
				SetName("domain_claimed_unique").
				SetUnique(true).
				SetPartialFilterExpression(bson.M{"domain": bson.M{"$exists": true, "$type": "string", "$gt": ""}}),
		},
		{
			Keys: bson.D{{Key: "owner_user_id", Value: 1}},
			Options: options.Index().
				SetName("owner_personal_unique").
				SetUnique(true).
				SetPartialFilterExpression(bson.M{"kind": CompanyKindPersonal}),
		},
	}
	if _, err := Collection(companyCollection).Indexes().CreateMany(ctx, indexes); err != nil {
		return fmt.Errorf("ensure company indexes: %w", err)
	}
	return nil
}

// CreateCompany inserts the company doc. Duplicate-key = a claimed domain (or
// the owner's personal slot) is already taken — the caller re-reads the
// winner.
func CreateCompany(ctx context.Context, company *Company) error {
	now := time.Now().UTC()
	company.CreatedAt = now
	company.UpdatedAt = now
	if company.Ver == 0 {
		company.Ver = 1
	}
	id, err := InsertOne(ctx, companyCollection, company)
	if err != nil {
		return err
	}
	company.ID = id
	return nil
}

// IsDuplicateCompany reports whether err is one of the partial-unique
// violations (claimed domain, or second personal company for an owner).
func IsDuplicateCompany(err error) bool {
	return mongo.IsDuplicateKeyError(err)
}

// FindCompanyByID returns the company by its tenancy key.
func FindCompanyByID(ctx context.Context, id primitive.ObjectID) (bool, *Company, error) {
	var company Company
	found, err := FindOne(ctx, companyCollection, bson.M{fieldID: id}, &company)
	if err != nil || !found {
		return false, nil, err
	}
	return true, &company, nil
}

// FindCompanyByDomain resolves a claimed domain to its company. Domain is an
// attribute, never identity — this exists for claim/dispute checks only.
func FindCompanyByDomain(ctx context.Context, domain string) (bool, *Company, error) {
	norm := NormalizeCompanyDomain(domain)
	if norm == "" {
		return false, nil, nil
	}
	var company Company
	found, err := FindOne(ctx, companyCollection, bson.M{"domain": norm}, &company)
	if err != nil || !found {
		return false, nil, err
	}
	return true, &company, nil
}

// FindPersonalCompanyByOwner returns the owner's personal company (unique by
// the D20 index).
func FindPersonalCompanyByOwner(ctx context.Context, ownerUserID primitive.ObjectID) (bool, *Company, error) {
	var company Company
	found, err := FindOne(ctx, companyCollection, bson.M{
		"owner_user_id": ownerUserID,
		"kind":          CompanyKindPersonal,
	}, &company)
	if err != nil || !found {
		return false, nil, err
	}
	return true, &company, nil
}

// EnsurePersonalCompany mints the user's personal company + their claimed
// owner membership — the signup hook and the companymigrate backfill both
// funnel here. Idempotent end to end: the D20 partial-unique index turns a
// re-run (or a race between the JWT stub path and the Clerk webhook) into a
// dup-key re-read, and the membership upsert is keyed on (company, email).
func EnsurePersonalCompany(ctx context.Context, userID primitive.ObjectID, email, name string) (*Company, error) {
	if found, company, err := FindPersonalCompanyByOwner(ctx, userID); err != nil {
		return nil, err
	} else if found {
		if err := ensureOwnerMembership(ctx, company, userID, email); err != nil {
			return nil, err
		}
		return company, nil
	}

	company := &Company{
		Kind:        CompanyKindPersonal,
		Name:        name,
		OwnerUserID: userID,
	}
	if err := CreateCompany(ctx, company); err != nil {
		if IsDuplicateCompany(err) {
			found, winner, ferr := FindPersonalCompanyByOwner(ctx, userID)
			if ferr != nil {
				return nil, ferr
			}
			if !found {
				return nil, fmt.Errorf("ensure personal company %s: dup-key but no winner", userID.Hex())
			}
			company = winner
		} else {
			return nil, fmt.Errorf("ensure personal company %s: %w", userID.Hex(), err)
		}
	}
	if err := ensureOwnerMembership(ctx, company, userID, email); err != nil {
		return nil, err
	}
	return company, nil
}

// CreateTeamCompany mints a team company owned by ownerUserID plus the
// owner's claimed user_admin membership (EnsurePersonalCompany's second half
// reused). The company starts unbilled — SeatsPurchased 0 keeps the seat gate
// shut until the company-plan purchase (P7) stamps billing. WebsiteURL is
// stored as given; the checkout gate enforces D14 (required before purchase).
func CreateTeamCompany(ctx context.Context, ownerUserID primitive.ObjectID, email, name, websiteURL string) (*Company, error) {
	company := &Company{
		Kind:        CompanyKindTeam,
		Name:        name,
		WebsiteURL:  strings.TrimSpace(websiteURL),
		OwnerUserID: ownerUserID,
	}
	if err := CreateCompany(ctx, company); err != nil {
		return nil, fmt.Errorf("create team company: %w", err)
	}
	if err := ensureOwnerMembership(ctx, company, ownerUserID, email); err != nil {
		return nil, err
	}
	return company, nil
}

// SetCompanyProfile updates the editable profile fields (name, website URL).
// Empty strings are skipped — this is a patch, not a replace.
func SetCompanyProfile(ctx context.Context, companyID primitive.ObjectID, name, websiteURL string) error {
	set := bson.M{fieldUpdatedAt: time.Now().UTC()}
	if name != "" {
		set["name"] = name
	}
	if websiteURL != "" {
		set["website_url"] = websiteURL
	}
	return UpdateOne(ctx, companyCollection, bson.M{fieldID: companyID}, bson.M{"$set": set})
}

// ClaimCompanyDomain stamps the optional domain attribute. First-come wins on
// the partial unique index; the dup-key error surfaces for the caller to 409.
func ClaimCompanyDomain(ctx context.Context, companyID primitive.ObjectID, domain string) error {
	norm := NormalizeCompanyDomain(domain)
	if norm == "" {
		return fmt.Errorf("claim company domain: empty domain")
	}
	return UpdateOne(ctx, companyCollection,
		bson.M{fieldID: companyID},
		bson.M{"$set": bson.M{"domain": norm, fieldUpdatedAt: time.Now().UTC()}},
	)
}

// SetCompanyBilling updates the Paddle linkage / purchased-seat count (ops +
// webhook writes).
func SetCompanyBilling(ctx context.Context, companyID primitive.ObjectID, billing CompanyBilling) error {
	return UpdateOne(ctx, companyCollection,
		bson.M{fieldID: companyID},
		bson.M{"$set": bson.M{"billing": billing, fieldUpdatedAt: time.Now().UTC()}},
	)
}

// MarkCompanyTrialed stamps trialed_at once (D21). Idempotent — an already
// trialed company is a no-op, and the mark is never cleared (that is the
// point: transfer-then-retrial stays blocked).
func MarkCompanyTrialed(ctx context.Context, companyID primitive.ObjectID) error {
	now := time.Now().UTC()
	_, err := Collection(companyCollection).UpdateOne(ctx,
		bson.M{fieldID: companyID, "trialed_at": bson.M{"$exists": false}},
		bson.M{"$set": bson.M{"trialed_at": now, fieldUpdatedAt: now}},
	)
	if err != nil {
		return fmt.Errorf("mark company trialed %s: %w", companyID.Hex(), err)
	}
	return nil
}

// TransferCompanyOwnership swaps OwnerUserID under CAS. The caller enforces
// the D12/D17 guards (old owner only, target is a claimed user_admin member);
// this helper only guarantees the swap is race-free against itself.
func TransferCompanyOwnership(ctx context.Context, companyID, fromUserID, toUserID primitive.ObjectID) error {
	now := time.Now().UTC()
	res, err := Collection(companyCollection).UpdateOne(ctx,
		bson.M{fieldID: companyID, "owner_user_id": fromUserID},
		bson.M{
			"$set": bson.M{"owner_user_id": toUserID, fieldUpdatedAt: now},
			"$inc": bson.M{"ver": 1},
		},
	)
	if err != nil {
		return fmt.Errorf("transfer company ownership %s: %w", companyID.Hex(), err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("transfer company ownership %s: caller is not the owner", companyID.Hex())
	}
	return nil
}

// ReserveCompanySeat is the D18 seat gate: check-and-bump SeatsUsed under the
// company doc's Ver CAS, BEFORE the membership insert. Returns
// ErrNoFreeCompanySeat when occupancy has reached SeatsPurchased. A failed
// membership insert afterwards must roll back via ReleaseCompanySeatSlot.
func ReserveCompanySeat(ctx context.Context, companyID primitive.ObjectID) error {
	for attempt := 0; attempt < companyCASRetries; attempt++ {
		found, company, err := FindCompanyByID(ctx, companyID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("reserve company seat: no company %s", companyID.Hex())
		}
		if company.SeatsUsed >= company.Billing.SeatsPurchased {
			return ErrNoFreeCompanySeat
		}
		res, err := Collection(companyCollection).UpdateOne(ctx,
			bson.M{fieldID: companyID, "ver": company.Ver},
			bson.M{
				"$inc": bson.M{"seats_used": 1, "ver": 1},
				"$set": bson.M{fieldUpdatedAt: time.Now().UTC()},
			},
		)
		if err != nil {
			return fmt.Errorf("reserve company seat %s: %w", companyID.Hex(), err)
		}
		if res.MatchedCount > 0 {
			return nil
		}
		// Ver moved — re-read and retry so two concurrent invites can never
		// both pass a full company.
	}
	return fmt.Errorf("reserve company seat %s: CAS retries exhausted", companyID.Hex())
}

// ReleaseCompanySeatSlot decrements SeatsUsed (revoke/archive, or rollback of
// a failed invite insert). Floored at zero under the same CAS.
func ReleaseCompanySeatSlot(ctx context.Context, companyID primitive.ObjectID) error {
	for attempt := 0; attempt < companyCASRetries; attempt++ {
		found, company, err := FindCompanyByID(ctx, companyID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("release company seat: no company %s", companyID.Hex())
		}
		if company.SeatsUsed <= 0 {
			return nil
		}
		res, err := Collection(companyCollection).UpdateOne(ctx,
			bson.M{fieldID: companyID, "ver": company.Ver},
			bson.M{
				"$inc": bson.M{"seats_used": -1, "ver": 1},
				"$set": bson.M{fieldUpdatedAt: time.Now().UTC()},
			},
		)
		if err != nil {
			return fmt.Errorf("release company seat %s: %w", companyID.Hex(), err)
		}
		if res.MatchedCount > 0 {
			return nil
		}
	}
	return fmt.Errorf("release company seat %s: CAS retries exhausted", companyID.Hex())
}

// CountCompanySeatOccupancy is the reconciliation/display count (invited +
// claimed memberships) — NEVER the seat gate (a count-based gate oversells
// under concurrent invites; D18).
func CountCompanySeatOccupancy(ctx context.Context, companyID primitive.ObjectID) (int64, error) {
	return Collection(companyMembershipCollection).CountDocuments(ctx, bson.M{
		"company_id": companyID,
		fieldStatus:  bson.M{"$in": bson.A{CompanyMembershipStatusInvited, CompanyMembershipStatusClaimed}},
	})
}
