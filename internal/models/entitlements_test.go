package models

import (
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// The DB-bound halves of the recompute (winner query, CAS retry loop) need an
// integration harness this repo doesn't have; what's pinned down here is the
// part that guards correctness: the exact filter/update documents. A wrong
// dotted path silently clobbers admin comp/override state, and a wrong CAS
// filter turns the guarded write into an unconditional one.

func TestBuildEntitlementProjectionUpdate_WinnerSetsDottedPathsOnly(t *testing.T) {
	winner := &Subscription{
		Status:      SubStatusTrialing,
		PriceID:     "pri_123",
		ValidTill:   time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		LastEventAt: time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC),
	}
	update := buildEntitlementProjectionUpdate(AppIDIndexly, winner)

	set, ok := update["$set"].(bson.M)
	if !ok {
		t.Fatalf("winner update must carry $set, got %v", update)
	}
	want := map[string]any{
		"entitlements.indexly.status":        SubStatusTrialing,
		"entitlements.indexly.price_id":      "pri_123",
		"entitlements.indexly.valid_till":    winner.ValidTill,
		"entitlements.indexly.last_event_at": winner.LastEventAt,
	}
	if len(set) != len(want) {
		t.Errorf("$set has %d keys, want %d: %v", len(set), len(want), set)
	}
	for k, v := range want {
		if got, ok := set[k]; !ok {
			t.Errorf("$set missing dotted path %q", k)
		} else if tv, isTime := v.(time.Time); isTime {
			if !got.(time.Time).Equal(tv) {
				t.Errorf("$set[%q] = %v, want %v", k, got, tv)
			}
		} else if got != v {
			t.Errorf("$set[%q] = %v, want %v", k, got, v)
		}
	}
	if _, hasUnset := update["$unset"]; hasUnset {
		t.Error("winner update must not $unset anything")
	}
	inc := update["$inc"].(bson.M)
	if inc["entitlements.indexly.ver"] != 1 {
		t.Errorf("ver must be $inc'd by 1, got %v", inc)
	}
}

func TestBuildEntitlementProjectionUpdate_NoSubsUnsetsProjectionKeepsAdminFields(t *testing.T) {
	update := buildEntitlementProjectionUpdate(AppIDIndexly, nil)

	unset, ok := update["$unset"].(bson.M)
	if !ok {
		t.Fatalf("zero-subs update must carry $unset, got %v", update)
	}
	for _, k := range []string{
		"entitlements.indexly.status",
		"entitlements.indexly.price_id",
		"entitlements.indexly.valid_till",
		"entitlements.indexly.last_event_at",
	} {
		if _, ok := unset[k]; !ok {
			t.Errorf("$unset missing projection path %q", k)
		}
	}
	// Exactly the four projection fields — comp/grants/revokes stay.
	if len(unset) != 4 {
		t.Errorf("$unset must touch only the 4 projection fields, got %v", unset)
	}
	if _, hasSet := update["$set"]; hasSet {
		t.Error("zero-subs update must not $set anything")
	}
	if update["$inc"].(bson.M)["entitlements.indexly.ver"] != 1 {
		t.Error("zero-subs update must still $inc ver (CAS applies)")
	}
}

func TestEntitlementCASFilter(t *testing.T) {
	userID := primitive.NewObjectID()

	// Existing projection: exact ver equality.
	f := entitlementCASFilter(userID, AppIDIndexly, 7)
	if f["_id"] != userID {
		t.Errorf("filter _id = %v, want %v", f["_id"], userID)
	}
	if f["entitlements.indexly.ver"] != int64(7) {
		t.Errorf("ver filter = %v, want 7", f["entitlements.indexly.ver"])
	}

	// Absent projection (ver decodes 0): must match a missing ver — including
	// an entitlement entry an admin created with comp/overrides only — but
	// never a concurrent recompute's ver>=1.
	f = entitlementCASFilter(userID, AppIDIndexly, 0)
	in, ok := f["entitlements.indexly.ver"].(bson.M)
	if !ok {
		t.Fatalf("absent-ver filter must use $in, got %v", f)
	}
	vals, ok := in["$in"].(bson.A)
	if !ok || len(vals) != 2 || vals[0] != int64(0) || vals[1] != nil {
		t.Errorf("absent-ver filter must be $in [0, null], got %v", in)
	}
}

func TestEntitlementProjectionMatches(t *testing.T) {
	validTill := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	lastEvent := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	winner := &Subscription{Status: SubStatusActive, PriceID: "pri_1", ValidTill: validTill, LastEventAt: lastEvent}
	inSync := AppEntitlement{Status: SubStatusActive, PriceID: "pri_1", ValidTill: validTill, LastEventAt: lastEvent, Ver: 3}

	cases := []struct {
		name   string
		ent    AppEntitlement
		winner *Subscription
		want   bool
	}{
		{"in sync", inSync, winner, true},
		{"status drift", AppEntitlement{Status: SubStatusCanceled, PriceID: "pri_1", ValidTill: validTill, LastEventAt: lastEvent}, winner, false},
		{"valid_till drift", AppEntitlement{Status: SubStatusActive, PriceID: "pri_1", ValidTill: validTill.Add(time.Hour), LastEventAt: lastEvent}, winner, false},
		{"missing projection with winner", AppEntitlement{}, winner, false},
		{"no subs and no projection", AppEntitlement{}, nil, true},
		{"no subs but stale projection", AppEntitlement{Status: SubStatusActive, ValidTill: validTill}, nil, false},
		{"no subs, override-only projection ignored", AppEntitlement{Grants: []OverrideEntry{{Key: "article.generate"}}, Ver: 1}, nil, true},
		{"ver ignored (not part of drift)", AppEntitlement{Status: SubStatusActive, PriceID: "pri_1", ValidTill: validTill, LastEventAt: lastEvent, Ver: 99}, winner, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := entitlementProjectionMatches(tc.ent, tc.winner); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAppScopeFilter(t *testing.T) {
	// Indexly must tolerate docs that predate app_id stamping — a strict match
	// would 402 every legacy subscriber the moment the binary deploys.
	f, ok := appScopeFilter(AppIDIndexly).(bson.M)
	if !ok {
		t.Fatalf("indexly scope must be a $in document, got %T", appScopeFilter(AppIDIndexly))
	}
	vals := f["$in"].(bson.A)
	if len(vals) != 2 || vals[0] != AppIDIndexly || vals[1] != nil {
		t.Errorf("indexly scope must match [indexly, null], got %v", vals)
	}

	// Any other app matches strictly — missing app_id never means app2.
	if got := appScopeFilter("app2"); got != "app2" {
		t.Errorf("non-indexly scope must be strict equality, got %v", got)
	}
}

// pickEntitlementWinner is the pure half of the team-seat projection: the
// newest-valid_till rule across the user's own sub and their claimed
// companies' subs. The DB halves (claimed-membership lookup, per-company
// newest query) follow the same untestable-here pattern as the winner query.
func TestPickEntitlementWinner(t *testing.T) {
	at := func(day int) time.Time { return time.Date(2026, 9, day, 0, 0, 0, 0, time.UTC) }
	own := &Subscription{PriceID: "pri_own", ValidTill: at(10)}
	teamOld := Subscription{PriceID: "pri_team_old", ValidTill: at(5)}
	teamNew := Subscription{PriceID: "pri_team_new", ValidTill: at(20)}

	// Member with no personal sub gains the company sub on claim.
	if w := pickEntitlementWinner(nil, []Subscription{teamOld}); w == nil || w.PriceID != "pri_team_old" {
		t.Errorf("no own sub: company sub must win, got %+v", w)
	}
	// Revoked member with nothing left projects nothing.
	if w := pickEntitlementWinner(nil, nil); w != nil {
		t.Errorf("no subs at all must project nil, got %+v", w)
	}
	// Personal sub vs team sub: newest valid_till wins, both directions.
	if w := pickEntitlementWinner(own, []Subscription{teamNew}); w.PriceID != "pri_team_new" {
		t.Errorf("newer team sub must beat own, got %s", w.PriceID)
	}
	if w := pickEntitlementWinner(own, []Subscription{teamOld}); w.PriceID != "pri_own" {
		t.Errorf("newer own sub must beat team, got %s", w.PriceID)
	}
	// Tie keeps the own sub (stable attribution).
	tie := Subscription{PriceID: "pri_team_tie", ValidTill: at(10)}
	if w := pickEntitlementWinner(own, []Subscription{tie}); w.PriceID != "pri_own" {
		t.Errorf("tie must keep the own sub, got %s", w.PriceID)
	}
	// Member in two billed companies: newest of the candidates wins.
	if w := pickEntitlementWinner(nil, []Subscription{teamOld, teamNew}); w.PriceID != "pri_team_new" {
		t.Errorf("two companies: newest must win, got %s", w.PriceID)
	}
	if w := pickEntitlementWinner(nil, []Subscription{teamNew, teamOld}); w.PriceID != "pri_team_new" {
		t.Errorf("two companies (reversed order): newest must win, got %s", w.PriceID)
	}
}
