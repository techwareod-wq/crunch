package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/models"
	se "github.com/atharva-ng/crunch/internal/services/schedulingEngine"
	"github.com/atharva-ng/crunch/internal/services/schedulingEngine/utils"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// dayUTC returns 00:00 UTC of today shifted by offsetDays.
func dayUTC(offsetDays int) time.Time {
	now := time.Now().UTC()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, offsetDays)
}

func fullModeWEC(wecID, weID primitive.ObjectID, sched models.SchedulingMetadata) *models.WebEntityContext {
	return &models.WebEntityContext{
		ID: wecID, WebEntityID: weID,
		Status:          models.SIEStatusSchedulingDone,
		SIEMode:         models.SIEModeFull,
		ProcessMetadata: models.ProcessMetadata{SchedulingMetadata: sched},
	}
}

func cadence5WebEntity(weID primitive.ObjectID) *models.WebEntity {
	return &models.WebEntity{ID: weID, Publishing: &models.PublishingConfig{ArticlesPerWeek: 5}}
}

// A fresh rerun appends a full window (articles_per_week × weeks = 5×4 = 20)
// starting the day after the latest existing slot, persists the resume marker
// before inserting, and fans out article-type generation for new rows only.
func TestRerunScheduleAppendsNextWindowAfterCalendarEnd(t *testing.T) {
	wecID := primitive.NewObjectID()
	weID := primitive.NewObjectID()
	userID := primitive.NewObjectID()

	keywords := testKeywords(60)
	lastDate := dayUTC(3)
	existing := make([]*models.ScheduledArticle, 20)
	for i := range existing {
		existing[i] = &models.ScheduledArticle{
			ID:           primitive.NewObjectID(),
			KeywordID:    keywords[i].ID,
			ScheduleDate: lastDate.AddDate(0, 0, i-19), // ends at lastDate
			ArticleType:  models.ArticleTypeHowToGuide,
		}
	}

	var created []*models.ScheduledArticle
	var markerKey string
	var markerAnchor time.Time
	var totalSet int
	st := &seMockStore{
		getWEC: func(ctx context.Context, id string) (bool, *models.WebEntityContext, error) {
			return true, fullModeWEC(wecID, weID, models.SchedulingMetadata{Total: 20}), nil
		},
		getWebEntity: func(ctx context.Context, id string) (bool, *models.WebEntity, error) {
			return true, cadence5WebEntity(weID), nil
		},
		getScheduledByWEC: func(ctx context.Context, id string) ([]*models.ScheduledArticle, error) {
			return existing, nil
		},
		getKeywordsForWEC: func(ctx context.Context, id string) ([]models.Keyword, error) {
			return keywords, nil
		},
		setRerunMarker: func(ctx context.Context, id, key string, anchor time.Time) error {
			markerKey, markerAnchor = key, anchor
			return nil
		},
		createScheduled: func(ctx context.Context, articles []*models.ScheduledArticle) error {
			created = articles
			for _, a := range articles {
				a.ID = primitive.NewObjectID()
			}
			return nil
		},
		setSchedulingTotal: func(ctx context.Context, id string, total int) error {
			totalSet = total
			return nil
		},
	}
	d := &mockDispatcher{}
	svc := &schedulingEngineService{store: st, dispatcher: d, values: config.SchedulingValues{Weeks: 4, UpgradeCadence: 15}, trial: trialValues()}

	err := svc.RerunSchedule(context.Background(), userID.Hex(), se.SEOrchestratePayload{
		WebEntityID:        weID.Hex(),
		WebEntityContextID: wecID.Hex(),
	}, "msg-1")
	if err != nil {
		t.Fatalf("RerunSchedule: %v", err)
	}

	if len(created) != 20 {
		t.Fatalf("appended %d rows, want 20 (5/wk × 4 weeks)", len(created))
	}
	if markerKey != "msg-1" {
		t.Errorf("rerun marker key = %q, want %q", markerKey, "msg-1")
	}
	wantAnchor := lastDate.AddDate(0, 0, 1)
	if !markerAnchor.Equal(wantAnchor) {
		t.Errorf("rerun marker anchor = %v, want %v (day after last slot)", markerAnchor, wantAnchor)
	}
	scheduled := map[primitive.ObjectID]bool{}
	for _, sa := range existing {
		scheduled[sa.KeywordID] = true
	}
	for _, sa := range created {
		if sa.ScheduleDate.Before(wantAnchor) {
			t.Errorf("appended slot %v precedes window start %v", sa.ScheduleDate, wantAnchor)
		}
		if scheduled[sa.KeywordID] {
			t.Errorf("appended row reuses scheduled keyword %s", sa.KeywordID.Hex())
		}
		scheduled[sa.KeywordID] = true
	}
	if totalSet != 40 {
		t.Errorf("scheduling total = %d, want 40", totalSet)
	}
	if len(d.dispatched) != 20 {
		t.Errorf("dispatched %d fan-out messages, want 20 (new rows only)", len(d.dispatched))
	}
	for _, pt := range d.dispatched {
		if pt != string(se.ProcessSchedulingGenerateArticleType) {
			t.Errorf("dispatched %q, want %q", pt, se.ProcessSchedulingGenerateArticleType)
		}
	}
}

// When the calendar ended in the past (late renewal), the new window starts
// today — a rerun never backfills past-dated articles.
func TestRerunScheduleClampsAnchorToTodayWhenCalendarEndedInPast(t *testing.T) {
	wecID := primitive.NewObjectID()
	weID := primitive.NewObjectID()

	keywords := testKeywords(40)
	existing := make([]*models.ScheduledArticle, 10)
	for i := range existing {
		existing[i] = &models.ScheduledArticle{
			ID:           primitive.NewObjectID(),
			KeywordID:    keywords[i].ID,
			ScheduleDate: dayUTC(-19 + i), // ends 10 days ago
			ArticleType:  models.ArticleTypeHowToGuide,
		}
	}

	var created []*models.ScheduledArticle
	var markerAnchor time.Time
	st := &seMockStore{
		getWEC: func(ctx context.Context, id string) (bool, *models.WebEntityContext, error) {
			return true, fullModeWEC(wecID, weID, models.SchedulingMetadata{Total: 10}), nil
		},
		getWebEntity: func(ctx context.Context, id string) (bool, *models.WebEntity, error) {
			return true, cadence5WebEntity(weID), nil
		},
		getScheduledByWEC: func(ctx context.Context, id string) ([]*models.ScheduledArticle, error) {
			return existing, nil
		},
		getKeywordsForWEC: func(ctx context.Context, id string) ([]models.Keyword, error) {
			return keywords, nil
		},
		setRerunMarker: func(ctx context.Context, id, key string, anchor time.Time) error {
			markerAnchor = anchor
			return nil
		},
		createScheduled: func(ctx context.Context, articles []*models.ScheduledArticle) error {
			created = articles
			for _, a := range articles {
				a.ID = primitive.NewObjectID()
			}
			return nil
		},
		setSchedulingTotal: func(ctx context.Context, id string, total int) error { return nil },
	}
	d := &mockDispatcher{}
	svc := &schedulingEngineService{store: st, dispatcher: d, values: config.SchedulingValues{Weeks: 4, UpgradeCadence: 15}, trial: trialValues()}

	err := svc.RerunSchedule(context.Background(), primitive.NewObjectID().Hex(), se.SEOrchestratePayload{
		WebEntityID: weID.Hex(), WebEntityContextID: wecID.Hex(),
	}, "msg-1")
	if err != nil {
		t.Fatalf("RerunSchedule: %v", err)
	}

	today := dayUTC(0)
	if !markerAnchor.Equal(today) {
		t.Errorf("rerun marker anchor = %v, want today %v", markerAnchor, today)
	}
	for _, sa := range created {
		if sa.ScheduleDate.Before(today) {
			t.Errorf("appended slot %v is in the past", sa.ScheduleDate)
		}
	}
}

// A retry of the same message after a partial insert resumes the stored
// window: it re-dispatches the fan-out for the rows the crashed attempt left
// untyped, fills exactly the remaining slots of the window, and does not
// rewrite the marker.
func TestRerunScheduleRetryResumesPartialWindow(t *testing.T) {
	wecID := primitive.NewObjectID()
	weID := primitive.NewObjectID()

	keywords := testKeywords(60)
	anchor := dayUTC(1)

	// The window's deterministic slot list: the crashed attempt occupied the
	// first 8 slots; the resume must fill slots[8:] exactly.
	windowSlots, err := utils.GenerateSlots(5, anchor, 20)
	if err != nil {
		t.Fatalf("GenerateSlots: %v", err)
	}

	existing := make([]*models.ScheduledArticle, 0, 18)
	for i := 0; i < 10; i++ { // pre-window rows
		existing = append(existing, &models.ScheduledArticle{
			ID: primitive.NewObjectID(), KeywordID: keywords[i].ID,
			ScheduleDate: anchor.AddDate(0, 0, i-10),
			ArticleType:  models.ArticleTypeHowToGuide,
		})
	}
	// Rows the crashed attempt inserted before dying: on the real slot dates,
	// still untyped because the crash preceded their fan-out dispatch.
	orphanIDs := make([]string, 8)
	for i := 0; i < 8; i++ {
		sa := &models.ScheduledArticle{
			ID: primitive.NewObjectID(), KeywordID: keywords[10+i].ID,
			ScheduleDate: windowSlots[i],
		}
		orphanIDs[i] = sa.ID.Hex()
		existing = append(existing, sa)
	}

	var created []*models.ScheduledArticle
	var totalSet int
	st := &seMockStore{
		getWEC: func(ctx context.Context, id string) (bool, *models.WebEntityContext, error) {
			return true, fullModeWEC(wecID, weID, models.SchedulingMetadata{
				Total: 10, RerunKey: "msg-1", RerunAnchor: anchor,
			}), nil
		},
		getWebEntity: func(ctx context.Context, id string) (bool, *models.WebEntity, error) {
			return true, cadence5WebEntity(weID), nil
		},
		getScheduledByWEC: func(ctx context.Context, id string) ([]*models.ScheduledArticle, error) {
			return existing, nil
		},
		getKeywordsForWEC: func(ctx context.Context, id string) ([]models.Keyword, error) {
			return keywords, nil
		},
		// setRerunMarker deliberately nil: rewriting the marker on a resumed
		// retry must not happen and would panic the mock.
		createScheduled: func(ctx context.Context, articles []*models.ScheduledArticle) error {
			created = articles
			for _, a := range articles {
				a.ID = primitive.NewObjectID()
			}
			return nil
		},
		setSchedulingTotal: func(ctx context.Context, id string, total int) error {
			totalSet = total
			return nil
		},
	}
	d := &mockDispatcher{}
	svc := &schedulingEngineService{store: st, dispatcher: d, values: config.SchedulingValues{Weeks: 4, UpgradeCadence: 15}, trial: trialValues()}

	if err := svc.RerunSchedule(context.Background(), primitive.NewObjectID().Hex(), se.SEOrchestratePayload{
		WebEntityID: weID.Hex(), WebEntityContextID: wecID.Hex(),
	}, "msg-1"); err != nil {
		t.Fatalf("RerunSchedule: %v", err)
	}

	if len(created) != 12 {
		t.Fatalf("resumed retry appended %d rows, want 12 (20 window − 8 already inserted)", len(created))
	}
	scheduled := map[primitive.ObjectID]bool{}
	for _, sa := range existing {
		scheduled[sa.KeywordID] = true
	}
	for i, sa := range created {
		// Pin the exact dates: the resume must fill the window's unfilled tail,
		// not re-fill slots the crashed attempt already occupied.
		if !sa.ScheduleDate.Equal(windowSlots[8+i]) {
			t.Errorf("resumed row %d landed on %v, want slot %v", i, sa.ScheduleDate, windowSlots[8+i])
		}
		if scheduled[sa.KeywordID] {
			t.Errorf("resumed row reuses scheduled keyword %s", sa.KeywordID.Hex())
		}
		scheduled[sa.KeywordID] = true
	}
	if totalSet != 30 {
		t.Errorf("scheduling total = %d, want 30", totalSet)
	}
	// 8 re-dispatched orphans (swept first, in calendar order) + 12 new rows.
	if len(d.dispatched) != 20 {
		t.Fatalf("dispatched %d fan-out messages, want 20 (8 re-dispatched orphans + 12 new)", len(d.dispatched))
	}
	for i, id := range orphanIDs {
		step, ok := d.payloads[i].(se.SEArticleStepPayload)
		if !ok {
			t.Fatalf("payload %d is %T, want SEArticleStepPayload", i, d.payloads[i])
		}
		if step.ScheduledArticleID != id {
			t.Errorf("sweep dispatch %d = %s, want orphan %s", i, step.ScheduledArticleID, id)
		}
	}
}

// A retry of the same message after the insert fully completed inserts
// nothing; it re-stamps the total and re-dispatches only the window rows still
// missing an article type (a fan-out the crashed attempt lost).
func TestRerunScheduleRetryAfterFullInsertRepairsAndStops(t *testing.T) {
	wecID := primitive.NewObjectID()
	weID := primitive.NewObjectID()

	anchor := dayUTC(1)
	existing := make([]*models.ScheduledArticle, 0, 25)
	for i := 0; i < 5; i++ { // pre-window rows; one untyped must NOT be re-dispatched
		sa := &models.ScheduledArticle{
			ID: primitive.NewObjectID(), KeywordID: primitive.NewObjectID(),
			ScheduleDate: anchor.AddDate(0, 0, i-5),
			ArticleType:  models.ArticleTypeHowToGuide,
		}
		if i == 0 {
			sa.ArticleType = ""
		}
		existing = append(existing, sa)
	}
	var untypedInWindow []string
	for i := 0; i < 20; i++ { // the fully inserted window
		sa := &models.ScheduledArticle{
			ID: primitive.NewObjectID(), KeywordID: primitive.NewObjectID(),
			ScheduleDate: anchor.AddDate(0, 0, i),
			ArticleType:  models.ArticleTypeHowToGuide,
		}
		if i == 3 || i == 17 {
			sa.ArticleType = ""
			untypedInWindow = append(untypedInWindow, sa.ID.Hex())
		}
		existing = append(existing, sa)
	}

	var totalSet int
	st := &seMockStore{
		getWEC: func(ctx context.Context, id string) (bool, *models.WebEntityContext, error) {
			return true, fullModeWEC(wecID, weID, models.SchedulingMetadata{
				Total: 5, RerunKey: "msg-1", RerunAnchor: anchor,
			}), nil
		},
		getWebEntity: func(ctx context.Context, id string) (bool, *models.WebEntity, error) {
			return true, cadence5WebEntity(weID), nil
		},
		getScheduledByWEC: func(ctx context.Context, id string) ([]*models.ScheduledArticle, error) {
			return existing, nil
		},
		// createScheduled / getKeywordsForWEC / setRerunMarker deliberately nil:
		// the repair path must not insert, load keywords, or touch the marker.
		setSchedulingTotal: func(ctx context.Context, id string, total int) error {
			totalSet = total
			return nil
		},
	}
	d := &mockDispatcher{}
	svc := &schedulingEngineService{store: st, dispatcher: d, values: config.SchedulingValues{Weeks: 4, UpgradeCadence: 15}, trial: trialValues()}

	err := svc.RerunSchedule(context.Background(), primitive.NewObjectID().Hex(), se.SEOrchestratePayload{
		WebEntityID: weID.Hex(), WebEntityContextID: wecID.Hex(),
	}, "msg-1")
	if err != nil {
		t.Fatalf("RerunSchedule: %v", err)
	}

	if totalSet != 25 {
		t.Errorf("scheduling total = %d, want 25", totalSet)
	}
	if len(d.dispatched) != 2 {
		t.Fatalf("re-dispatched %d fan-out messages, want 2 (untyped window rows only)", len(d.dispatched))
	}
	for i, p := range d.payloads {
		step, ok := p.(se.SEArticleStepPayload)
		if !ok {
			t.Fatalf("payload %d is %T, want SEArticleStepPayload", i, p)
		}
		if step.ScheduledArticleID != untypedInWindow[i] {
			t.Errorf("re-dispatched %s, want %s", step.ScheduledArticleID, untypedInWindow[i])
		}
	}
}

// A rerun must not run before the initial scheduling completed.
func TestRerunScheduleRequiresSchedulingDone(t *testing.T) {
	wecID := primitive.NewObjectID()
	st := &seMockStore{
		getWEC: func(ctx context.Context, id string) (bool, *models.WebEntityContext, error) {
			return true, &models.WebEntityContext{
				ID:     wecID,
				Status: models.SIEStatusClusteringDone,
			}, nil
		},
	}
	svc := &schedulingEngineService{store: st, dispatcher: &mockDispatcher{}, values: config.SchedulingValues{Weeks: 4, UpgradeCadence: 15}}

	err := svc.RerunSchedule(context.Background(), primitive.NewObjectID().Hex(), se.SEOrchestratePayload{
		WebEntityID: primitive.NewObjectID().Hex(), WebEntityContextID: wecID.Hex(),
	}, "msg-1")
	if err == nil || !strings.Contains(err.Error(), "scheduling not complete") {
		t.Fatalf("err = %v, want scheduling-not-complete error", err)
	}
}

// A rerun must never target a trial-mode WEC — its calendar is the fixed trial
// week, and expansion happens via SE_EXTEND_SCHEDULE on upgrade.
func TestRerunScheduleRejectsTrialModeWEC(t *testing.T) {
	wecID := primitive.NewObjectID()
	st := &seMockStore{
		getWEC: func(ctx context.Context, id string) (bool, *models.WebEntityContext, error) {
			return true, &models.WebEntityContext{
				ID:      wecID,
				Status:  models.SIEStatusSchedulingDone,
				SIEMode: models.SIEModeTrial,
			}, nil
		},
	}
	svc := &schedulingEngineService{store: st, dispatcher: &mockDispatcher{}, values: config.SchedulingValues{Weeks: 4, UpgradeCadence: 15}}

	err := svc.RerunSchedule(context.Background(), primitive.NewObjectID().Hex(), se.SEOrchestratePayload{
		WebEntityID: primitive.NewObjectID().Hex(), WebEntityContextID: wecID.Hex(),
	}, "msg-1")
	if err == nil || !strings.Contains(err.Error(), "trial mode") {
		t.Fatalf("err = %v, want trial-mode rejection", err)
	}
}

// When every keyword already has a calendar row there is nothing to append —
// the rerun is a logged no-op, not an error.
func TestRerunScheduleNoUnscheduledKeywordsIsNoOp(t *testing.T) {
	wecID := primitive.NewObjectID()
	weID := primitive.NewObjectID()

	keywords := testKeywords(10)
	existing := make([]*models.ScheduledArticle, 10)
	for i := range existing {
		existing[i] = &models.ScheduledArticle{
			ID: primitive.NewObjectID(), KeywordID: keywords[i].ID,
			ScheduleDate: dayUTC(i - 9),
			ArticleType:  models.ArticleTypeHowToGuide,
		}
	}

	st := &seMockStore{
		getWEC: func(ctx context.Context, id string) (bool, *models.WebEntityContext, error) {
			return true, fullModeWEC(wecID, weID, models.SchedulingMetadata{Total: 10}), nil
		},
		getWebEntity: func(ctx context.Context, id string) (bool, *models.WebEntity, error) {
			return true, cadence5WebEntity(weID), nil
		},
		getScheduledByWEC: func(ctx context.Context, id string) ([]*models.ScheduledArticle, error) {
			return existing, nil
		},
		getKeywordsForWEC: func(ctx context.Context, id string) ([]models.Keyword, error) {
			return keywords, nil
		},
		// createScheduled / setRerunMarker deliberately nil: nothing to append.
	}
	d := &mockDispatcher{}
	svc := &schedulingEngineService{store: st, dispatcher: d, values: config.SchedulingValues{Weeks: 4, UpgradeCadence: 15}, trial: trialValues()}

	err := svc.RerunSchedule(context.Background(), primitive.NewObjectID().Hex(), se.SEOrchestratePayload{
		WebEntityID: weID.Hex(), WebEntityContextID: wecID.Hex(),
	}, "msg-1")
	if err != nil {
		t.Fatalf("RerunSchedule: %v", err)
	}
	if len(d.dispatched) != 0 {
		t.Errorf("dispatched %d messages, want 0", len(d.dispatched))
	}
}

// DispatchRerunSchedule validates and enqueues SE_RERUN_SCHEDULE with the
// context's own WebEntityID — the caller supplies only the context id.
func TestDispatchRerunScheduleEnqueues(t *testing.T) {
	wecID := primitive.NewObjectID()
	weID := primitive.NewObjectID()
	userID := primitive.NewObjectID()

	st := &seMockStore{
		getWEC: func(ctx context.Context, id string) (bool, *models.WebEntityContext, error) {
			wec := fullModeWEC(wecID, weID, models.SchedulingMetadata{Total: 20})
			wec.UserID = userID
			return true, wec, nil
		},
	}
	d := &mockDispatcher{}
	svc := &schedulingEngineService{store: st, dispatcher: d, values: config.SchedulingValues{Weeks: 4}, trial: trialValues()}

	if err := svc.DispatchRerunSchedule(context.Background(), userID.Hex(), wecID.Hex()); err != nil {
		t.Fatalf("DispatchRerunSchedule: %v", err)
	}

	if len(d.dispatched) != 1 || d.dispatched[0] != string(se.ProcessSchedulingRerunSchedule) {
		t.Fatalf("dispatched = %v, want one %s", d.dispatched, se.ProcessSchedulingRerunSchedule)
	}
	if d.userIDs[0] != userID.Hex() {
		t.Errorf("dispatched user = %s, want %s", d.userIDs[0], userID.Hex())
	}
	payload, ok := d.payloads[0].(se.SEOrchestratePayload)
	if !ok {
		t.Fatalf("payload type = %T, want SEOrchestratePayload", d.payloads[0])
	}
	if payload.WebEntityID != weID.Hex() || payload.WebEntityContextID != wecID.Hex() {
		t.Errorf("payload = %+v, want webEntity %s / wec %s", payload, weID.Hex(), wecID.Hex())
	}
}

// Missing context and a context owned by another user both collapse into
// ErrWebEntityContextNotFound; nothing is enqueued.
func TestDispatchRerunScheduleRejectsMissingOrForeignContext(t *testing.T) {
	wecID := primitive.NewObjectID()
	weID := primitive.NewObjectID()
	owner := primitive.NewObjectID()

	cases := []struct {
		name   string
		getWEC func(ctx context.Context, id string) (bool, *models.WebEntityContext, error)
		caller string
	}{
		{
			name: "not found",
			getWEC: func(ctx context.Context, id string) (bool, *models.WebEntityContext, error) {
				return false, nil, nil
			},
			caller: owner.Hex(),
		},
		{
			name: "foreign owner",
			getWEC: func(ctx context.Context, id string) (bool, *models.WebEntityContext, error) {
				wec := fullModeWEC(wecID, weID, models.SchedulingMetadata{})
				wec.UserID = owner
				return true, wec, nil
			},
			caller: primitive.NewObjectID().Hex(),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &mockDispatcher{}
			svc := &schedulingEngineService{store: &seMockStore{getWEC: tc.getWEC}, dispatcher: d, values: config.SchedulingValues{Weeks: 4}, trial: trialValues()}

			err := svc.DispatchRerunSchedule(context.Background(), tc.caller, wecID.Hex())
			if !errors.Is(err, se.ErrWebEntityContextNotFound) {
				t.Fatalf("err = %v, want ErrWebEntityContextNotFound", err)
			}
			if len(d.dispatched) != 0 {
				t.Errorf("dispatched %d messages, want 0", len(d.dispatched))
			}
		})
	}
}

// A context whose scheduling hasn't completed is rejected up front — the same
// gate the handler applies, surfaced synchronously to the admin caller.
func TestDispatchRerunScheduleRejectsIncompleteScheduling(t *testing.T) {
	wecID := primitive.NewObjectID()
	weID := primitive.NewObjectID()
	userID := primitive.NewObjectID()

	st := &seMockStore{
		getWEC: func(ctx context.Context, id string) (bool, *models.WebEntityContext, error) {
			wec := fullModeWEC(wecID, weID, models.SchedulingMetadata{})
			wec.UserID = userID
			wec.Status = models.SIEStatusClusteringDone
			return true, wec, nil
		},
	}
	d := &mockDispatcher{}
	svc := &schedulingEngineService{store: st, dispatcher: d, values: config.SchedulingValues{Weeks: 4}, trial: trialValues()}

	err := svc.DispatchRerunSchedule(context.Background(), userID.Hex(), wecID.Hex())
	if !errors.Is(err, se.ErrSchedulingNotComplete) {
		t.Fatalf("err = %v, want ErrSchedulingNotComplete", err)
	}
	if len(d.dispatched) != 0 {
		t.Errorf("dispatched %d messages, want 0", len(d.dispatched))
	}
}
