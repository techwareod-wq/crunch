package service

import (
	"context"
	"errors"
	"testing"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/models"
	cge "github.com/atharva-ng/crunch/internal/services/contentGenerationEngine"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// wecWithTotal builds a clustering-done WebEntityContext whose scheduling total
// is set, the state markSchedulingDoneIfComplete sees when the last title lands.
func wecWithTotal(wecID, weID primitive.ObjectID, mode, upgradeState string, total int) *models.WebEntityContext {
	return &models.WebEntityContext{
		ID:           wecID,
		WebEntityID:  weID,
		Status:       models.SIEStatusClusteringDone,
		SIEMode:      mode,
		UpgradeState: upgradeState,
		ProcessMetadata: models.ProcessMetadata{
			SchedulingMetadata: models.SchedulingMetadata{Total: total},
		},
	}
}

// On the final title of the initial onboarding schedule, the CAS winner must
// dispatch exactly one CGE_ORCHESTRATE for the earliest article — for BOTH
// trial and full onboarding, which converge on this path.
func TestMarkSchedulingDone_DispatchesFirstArticle(t *testing.T) {
	for _, mode := range []string{models.SIEModeTrial, models.SIEModeFull} {
		t.Run(mode, func(t *testing.T) {
			wecID := primitive.NewObjectID()
			weID := primitive.NewObjectID()
			userID := primitive.NewObjectID()
			saID := primitive.NewObjectID()
			kwID := primitive.NewObjectID()
			linking := true

			first := &models.ScheduledArticle{
				ID:                     saID,
				UserID:                 userID,
				KeywordID:              kwID,
				WebEntityContextID:     wecID,
				Title:                  "First Article Title",
				ArticleType:            models.ArticleTypeHowToGuide,
				AdditionalInstructions: "be concise",
				InternalLinkingEnabled: &linking,
			}

			var casFrom []int
			var casTo int
			st := &seMockStore{
				getWEC: func(ctx context.Context, id string) (bool, *models.WebEntityContext, error) {
					return true, wecWithTotal(wecID, weID, mode, "", 3), nil
				},
				countTitledForWEC: func(ctx context.Context, id string) (int64, error) { return 3, nil },
				tryAdvanceStatus: func(ctx context.Context, id string, from []int, to int) (bool, error) {
					casFrom, casTo = from, to
					return true, nil
				},
				getFirstScheduled: func(ctx context.Context, id string) (bool, *models.ScheduledArticle, error) {
					return true, first, nil
				},
			}
			d := &mockDispatcher{}
			svc := &schedulingEngineService{store: st, dispatcher: d, values: config.SchedulingValues{Weeks: 4, UpgradeCadence: 15}, trial: trialValues()}

			if err := svc.markSchedulingDoneIfComplete(context.Background(), wecID.Hex()); err != nil {
				t.Fatalf("markSchedulingDoneIfComplete: %v", err)
			}

			// CAS must be the ClusteringDone → SchedulingDone transition.
			if len(casFrom) != 1 || casFrom[0] != models.SIEStatusClusteringDone || casTo != models.SIEStatusSchedulingDone {
				t.Errorf("CAS = from %v to %d, want from [%d] to %d", casFrom, casTo,
					models.SIEStatusClusteringDone, models.SIEStatusSchedulingDone)
			}
			if len(d.dispatched) != 1 {
				t.Fatalf("dispatched %d events, want 1 (%v)", len(d.dispatched), d.dispatched)
			}
			if d.dispatched[0] != string(cge.ProcessCGEOrchestrate) {
				t.Errorf("dispatched %q, want %q", d.dispatched[0], cge.ProcessCGEOrchestrate)
			}
			if d.userIDs[0] != userID.Hex() {
				t.Errorf("dispatched for user %q, want %q", d.userIDs[0], userID.Hex())
			}
			p, ok := d.payloads[0].(cge.CGEOrchestratePayload)
			if !ok {
				t.Fatalf("payload type = %T, want CGEOrchestratePayload", d.payloads[0])
			}
			if p.ScheduledArticleID != saID.Hex() || p.KeywordID != kwID.Hex() || p.WebEntityContextID != wecID.Hex() {
				t.Errorf("payload ids = {sa:%s kw:%s wec:%s}, want {%s %s %s}",
					p.ScheduledArticleID, p.KeywordID, p.WebEntityContextID, saID.Hex(), kwID.Hex(), wecID.Hex())
			}
			if p.ArticleType != string(models.ArticleTypeHowToGuide) || p.ProposedTitle != "First Article Title" || p.AdditionalInstructions != "be concise" {
				t.Errorf("payload content = {type:%q title:%q instr:%q}", p.ArticleType, p.ProposedTitle, p.AdditionalInstructions)
			}
			if p.InternalLinkingEnabled == nil || *p.InternalLinkingEnabled != true {
				t.Errorf("payload InternalLinkingEnabled = %v, want true", p.InternalLinkingEnabled)
			}
		})
	}
}

// The trial→paid upgrade re-run restores SchedulingDone through this same path
// (upgrade_state == complete), but its articles already exist — it must NOT
// re-dispatch a generation. getFirstScheduled is left unset so any attempt to
// read the first article would panic the test.
func TestMarkSchedulingDone_SkipsDispatchOnUpgradeRestore(t *testing.T) {
	wecID := primitive.NewObjectID()
	weID := primitive.NewObjectID()

	st := &seMockStore{
		getWEC: func(ctx context.Context, id string) (bool, *models.WebEntityContext, error) {
			return true, wecWithTotal(wecID, weID, models.SIEModeFull, models.WECUpgradeStateComplete, 60), nil
		},
		countTitledForWEC: func(ctx context.Context, id string) (int64, error) { return 60, nil },
		tryAdvanceStatus: func(ctx context.Context, id string, from []int, to int) (bool, error) {
			return true, nil
		},
	}
	d := &mockDispatcher{}
	svc := &schedulingEngineService{store: st, dispatcher: d, values: config.SchedulingValues{Weeks: 4, UpgradeCadence: 15}}

	if err := svc.markSchedulingDoneIfComplete(context.Background(), wecID.Hex()); err != nil {
		t.Fatalf("markSchedulingDoneIfComplete: %v", err)
	}
	if len(d.dispatched) != 0 {
		t.Errorf("upgrade restore dispatched %d events, want 0", len(d.dispatched))
	}
}

// Below the titled-count target: no transition, no dispatch. tryAdvanceStatus
// and getFirstScheduled are unset so either being reached panics the test.
func TestMarkSchedulingDone_NoDispatchWhenIncomplete(t *testing.T) {
	wecID := primitive.NewObjectID()
	weID := primitive.NewObjectID()
	st := &seMockStore{
		getWEC: func(ctx context.Context, id string) (bool, *models.WebEntityContext, error) {
			return true, wecWithTotal(wecID, weID, models.SIEModeTrial, "", 5), nil
		},
		countTitledForWEC: func(ctx context.Context, id string) (int64, error) { return 4, nil },
	}
	d := &mockDispatcher{}
	svc := &schedulingEngineService{store: st, dispatcher: d, values: config.SchedulingValues{Weeks: 4, UpgradeCadence: 15}}

	if err := svc.markSchedulingDoneIfComplete(context.Background(), wecID.Hex()); err != nil {
		t.Fatalf("markSchedulingDoneIfComplete: %v", err)
	}
	if len(d.dispatched) != 0 {
		t.Errorf("incomplete schedule dispatched %d events, want 0", len(d.dispatched))
	}
}

// A concurrent late-arriver that loses the CAS must not dispatch — the winner
// already owns the kickoff.
func TestMarkSchedulingDone_NoDispatchWhenCASLost(t *testing.T) {
	wecID := primitive.NewObjectID()
	weID := primitive.NewObjectID()
	st := &seMockStore{
		getWEC: func(ctx context.Context, id string) (bool, *models.WebEntityContext, error) {
			return true, wecWithTotal(wecID, weID, models.SIEModeTrial, "", 5), nil
		},
		countTitledForWEC: func(ctx context.Context, id string) (int64, error) { return 5, nil },
		tryAdvanceStatus: func(ctx context.Context, id string, from []int, to int) (bool, error) {
			return false, nil // someone else won the transition
		},
	}
	d := &mockDispatcher{}
	svc := &schedulingEngineService{store: st, dispatcher: d, values: config.SchedulingValues{Weeks: 4, UpgradeCadence: 15}}

	if err := svc.markSchedulingDoneIfComplete(context.Background(), wecID.Hex()); err != nil {
		t.Fatalf("markSchedulingDoneIfComplete: %v", err)
	}
	if len(d.dispatched) != 0 {
		t.Errorf("CAS loser dispatched %d events, want 0", len(d.dispatched))
	}
}

// Already at SchedulingDone (common redelivery): early return before any count
// or CAS. countTitledForWEC/tryAdvanceStatus are unset → reaching them panics.
func TestMarkSchedulingDone_EarlyReturnWhenAlreadyDone(t *testing.T) {
	wecID := primitive.NewObjectID()
	weID := primitive.NewObjectID()
	st := &seMockStore{
		getWEC: func(ctx context.Context, id string) (bool, *models.WebEntityContext, error) {
			wec := wecWithTotal(wecID, weID, models.SIEModeTrial, "", 5)
			wec.Status = models.SIEStatusSchedulingDone
			return true, wec, nil
		},
	}
	d := &mockDispatcher{}
	svc := &schedulingEngineService{store: st, dispatcher: d, values: config.SchedulingValues{Weeks: 4, UpgradeCadence: 15}}

	if err := svc.markSchedulingDoneIfComplete(context.Background(), wecID.Hex()); err != nil {
		t.Fatalf("markSchedulingDoneIfComplete: %v", err)
	}
	if len(d.dispatched) != 0 {
		t.Errorf("already-done redelivery dispatched %d events, want 0", len(d.dispatched))
	}
}

// The kickoff is best-effort: the WEC has already been advanced (pipeline state
// committed), so a dispatch failure is swallowed, not surfaced as a handler
// error that would pointlessly re-run the title LLM without re-dispatching.
func TestMarkSchedulingDone_DispatchFailureIsSwallowed(t *testing.T) {
	wecID := primitive.NewObjectID()
	weID := primitive.NewObjectID()
	userID := primitive.NewObjectID()
	st := &seMockStore{
		getWEC: func(ctx context.Context, id string) (bool, *models.WebEntityContext, error) {
			return true, wecWithTotal(wecID, weID, models.SIEModeTrial, "", 5), nil
		},
		countTitledForWEC: func(ctx context.Context, id string) (int64, error) { return 5, nil },
		tryAdvanceStatus: func(ctx context.Context, id string, from []int, to int) (bool, error) {
			return true, nil
		},
		getFirstScheduled: func(ctx context.Context, id string) (bool, *models.ScheduledArticle, error) {
			return true, &models.ScheduledArticle{ID: primitive.NewObjectID(), UserID: userID, WebEntityContextID: wecID, KeywordID: primitive.NewObjectID()}, nil
		},
	}
	d := &mockDispatcher{err: errors.New("queue unavailable")}
	svc := &schedulingEngineService{store: st, dispatcher: d, values: config.SchedulingValues{Weeks: 4, UpgradeCadence: 15}}

	if err := svc.markSchedulingDoneIfComplete(context.Background(), wecID.Hex()); err != nil {
		t.Fatalf("dispatch failure must be swallowed, got err: %v", err)
	}
}
