package service

import (
	"context"
	"testing"
	"time"

	"github.com/atharva-ng/crunch/internal/config"
	"github.com/atharva-ng/crunch/internal/models"
	se "github.com/atharva-ng/crunch/internal/services/schedulingEngine"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// seMockStore implements store.Store with overridable funcs; any method called
// without an override panics so tests fail loudly on unexpected calls.
type seMockStore struct {
	getWebEntity       func(ctx context.Context, id string) (bool, *models.WebEntity, error)
	getWEC             func(ctx context.Context, id string) (bool, *models.WebEntityContext, error)
	getKeywordsForWEC  func(ctx context.Context, wecID string) ([]models.Keyword, error)
	createScheduled    func(ctx context.Context, articles []*models.ScheduledArticle) error
	countForContext    func(ctx context.Context, wecID string) (int64, error)
	countTitledForWEC  func(ctx context.Context, wecID string) (int64, error)
	setSchedulingTotal func(ctx context.Context, id string, total int) error
	getScheduledByWEC  func(ctx context.Context, wecID string) ([]*models.ScheduledArticle, error)
	getFirstScheduled  func(ctx context.Context, wecID string) (bool, *models.ScheduledArticle, error)
	tryAdvanceStatus   func(ctx context.Context, id string, fromStatuses []int, toStatus int) (bool, error)
	setCadence         func(ctx context.Context, webEntityID string, articlesPerWeek int) error
	finalizeWECUpgrade func(ctx context.Context, wecID string) error
	setRerunMarker     func(ctx context.Context, wecID, rerunKey string, anchor time.Time) error
}

func (m *seMockStore) GetWebEntityByID(ctx context.Context, id string) (bool, *models.WebEntity, error) {
	if m.getWebEntity == nil {
		panic("unexpected GetWebEntityByID")
	}
	return m.getWebEntity(ctx, id)
}
func (m *seMockStore) GetWebEntityContext(ctx context.Context, id string) (bool, *models.WebEntityContext, error) {
	if m.getWEC == nil {
		panic("unexpected GetWebEntityContext")
	}
	return m.getWEC(ctx, id)
}
func (m *seMockStore) GetWebEntityContextForUser(ctx context.Context, webEntityID, userID string) (bool, *models.WebEntityContext, error) {
	panic("unexpected GetWebEntityContextForUser")
}
func (m *seMockStore) TryAdvanceWECStatus(ctx context.Context, id string, fromStatuses []int, toStatus int) (bool, error) {
	if m.tryAdvanceStatus == nil {
		panic("unexpected TryAdvanceWECStatus")
	}
	return m.tryAdvanceStatus(ctx, id, fromStatuses, toStatus)
}
func (m *seMockStore) SetSchedulingTotal(ctx context.Context, id string, total int) error {
	if m.setSchedulingTotal == nil {
		panic("unexpected SetSchedulingTotal")
	}
	return m.setSchedulingTotal(ctx, id, total)
}
func (m *seMockStore) GetKeywordsForWEC(ctx context.Context, wecID string) ([]models.Keyword, error) {
	if m.getKeywordsForWEC == nil {
		panic("unexpected GetKeywordsForWEC")
	}
	return m.getKeywordsForWEC(ctx, wecID)
}
func (m *seMockStore) GetKeyword(ctx context.Context, id string) (bool, *models.Keyword, error) {
	panic("unexpected GetKeyword")
}
func (m *seMockStore) GetKeywordsByIDs(ctx context.Context, ids []primitive.ObjectID) ([]models.Keyword, error) {
	panic("unexpected GetKeywordsByIDs")
}
func (m *seMockStore) CreateScheduledArticle(ctx context.Context, article *models.ScheduledArticle) error {
	panic("unexpected CreateScheduledArticle")
}
func (m *seMockStore) CreateScheduledArticles(ctx context.Context, articles []*models.ScheduledArticle) error {
	if m.createScheduled == nil {
		panic("unexpected CreateScheduledArticles")
	}
	return m.createScheduled(ctx, articles)
}
func (m *seMockStore) GetScheduledArticle(ctx context.Context, id string) (bool, *models.ScheduledArticle, error) {
	panic("unexpected GetScheduledArticle")
}
func (m *seMockStore) UpdateScheduledArticle(ctx context.Context, id string, req models.ScheduledArticleUpdateReq) error {
	panic("unexpected UpdateScheduledArticle")
}
func (m *seMockStore) CountScheduledArticlesForContext(ctx context.Context, wecID string) (int64, error) {
	if m.countForContext == nil {
		panic("unexpected CountScheduledArticlesForContext")
	}
	return m.countForContext(ctx, wecID)
}
func (m *seMockStore) CountScheduledArticlesWithTitleForContext(ctx context.Context, wecID string) (int64, error) {
	if m.countTitledForWEC == nil {
		panic("unexpected CountScheduledArticlesWithTitleForContext")
	}
	return m.countTitledForWEC(ctx, wecID)
}
func (m *seMockStore) GetFirstScheduledArticleForContext(ctx context.Context, wecID string) (bool, *models.ScheduledArticle, error) {
	if m.getFirstScheduled == nil {
		panic("unexpected GetFirstScheduledArticleForContext")
	}
	return m.getFirstScheduled(ctx, wecID)
}
func (m *seMockStore) GetScheduledArticlesByWebEntityContext(ctx context.Context, wecID string) ([]*models.ScheduledArticle, error) {
	if m.getScheduledByWEC == nil {
		panic("unexpected GetScheduledArticlesByWebEntityContext")
	}
	return m.getScheduledByWEC(ctx, wecID)
}
func (m *seMockStore) SetWebEntityPublishingCadence(ctx context.Context, webEntityID string, articlesPerWeek int) error {
	if m.setCadence == nil {
		panic("unexpected SetWebEntityPublishingCadence")
	}
	return m.setCadence(ctx, webEntityID, articlesPerWeek)
}
func (m *seMockStore) FinalizeWECUpgrade(ctx context.Context, wecID string) error {
	if m.finalizeWECUpgrade == nil {
		panic("unexpected FinalizeWECUpgrade")
	}
	return m.finalizeWECUpgrade(ctx, wecID)
}
func (m *seMockStore) SetSchedulingRerunMarker(ctx context.Context, wecID, rerunKey string, anchor time.Time) error {
	if m.setRerunMarker == nil {
		panic("unexpected SetSchedulingRerunMarker")
	}
	return m.setRerunMarker(ctx, wecID, rerunKey, anchor)
}

type mockDispatcher struct {
	dispatched []string // process types, in order
	userIDs    []string // user id per dispatch, in order
	payloads   []any
	err        error // when set, Dispatch returns it without recording
}

func (d *mockDispatcher) Dispatch(ctx context.Context, processType, userID string, payload any) error {
	if d.err != nil {
		return d.err
	}
	d.dispatched = append(d.dispatched, processType)
	d.userIDs = append(d.userIDs, userID)
	d.payloads = append(d.payloads, payload)
	return nil
}

func (d *mockDispatcher) DispatchKeyed(ctx context.Context, processType, userID, _ string, payload any) error {
	return d.Dispatch(ctx, processType, userID, payload)
}

func trialValues() config.SIETrialValues {
	return config.SIETrialValues{
		UserKeywordsLimit:       100,
		CompetitorKeywordsLimit: 40,
		ExpandedKeywordsLimit:   120,
		MaxPersistedKeywords:    60,
		ClusterCount:            3,
		ScheduleWeeks:           1,
		Cadence:                 5,
		MaxArticles:             10,
	}
}

func testKeywords(n int) []models.Keyword {
	out := make([]models.Keyword, n)
	for i := range out {
		out[i] = models.Keyword{
			ID:               primitive.NewObjectID(),
			Keyword:          "kw",
			Cluster:          "c" + string(rune('a'+i%3)),
			OpportunityScore: float64(100 - i),
		}
	}
	return out
}

// Trial-mode Orchestrate must schedule trial.cadence × trial.scheduleWeeks
// slots (5×1) even when the WebEntity has no publishing cadence configured —
// the trial calendar ignores the user's chosen cadence entirely.
func TestOrchestrateTrialModeSchedulesTrialCalendar(t *testing.T) {
	wecID := primitive.NewObjectID()
	weID := primitive.NewObjectID()
	userID := primitive.NewObjectID()

	var created []*models.ScheduledArticle
	st := &seMockStore{
		countForContext: func(ctx context.Context, id string) (int64, error) { return 0, nil },
		getWEC: func(ctx context.Context, id string) (bool, *models.WebEntityContext, error) {
			return true, &models.WebEntityContext{
				ID: wecID, WebEntityID: weID,
				Status:  models.SIEStatusClusteringDone,
				SIEMode: models.SIEModeTrial,
			}, nil
		},
		getWebEntity: func(ctx context.Context, id string) (bool, *models.WebEntity, error) {
			// No Publishing config at all — resolveCadence would error; the
			// trial path must not consult it.
			return true, &models.WebEntity{ID: weID}, nil
		},
		getKeywordsForWEC: func(ctx context.Context, id string) ([]models.Keyword, error) {
			return testKeywords(20), nil
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

	err := svc.Orchestrate(context.Background(), userID.Hex(), se.SEOrchestratePayload{
		WebEntityID:        weID.Hex(),
		WebEntityContextID: wecID.Hex(),
	})
	if err != nil {
		t.Fatalf("Orchestrate: %v", err)
	}

	if len(created) != 5 {
		t.Fatalf("trial schedule created %d rows, want 5", len(created))
	}
	// All five rows must land within 7 days of the first (week 1 only).
	first := created[0].ScheduleDate
	for _, a := range created {
		if a.ScheduleDate.Sub(first) >= 7*24*time.Hour {
			t.Errorf("slot %v outside week 1 (first %v)", a.ScheduleDate, first)
		}
	}
	if len(d.dispatched) != 5 {
		t.Errorf("dispatched %d article-type messages, want 5", len(d.dispatched))
	}
}

// ExtendSchedule appends toward upgradeCadence × weeks (60) without touching
// existing rows, fans out generation for the new rows only, and finalizes.
func TestExtendScheduleAppendsAndFinalizes(t *testing.T) {
	wecID := primitive.NewObjectID()
	weID := primitive.NewObjectID()
	userID := primitive.NewObjectID()

	keywords := testKeywords(100)
	existingRows := make([]*models.ScheduledArticle, 5)
	for i := range existingRows {
		existingRows[i] = &models.ScheduledArticle{
			ID:           primitive.NewObjectID(),
			KeywordID:    keywords[i].ID,
			ScheduleDate: time.Date(2026, 7, 20+i, 0, 0, 0, 0, time.UTC),
		}
	}

	var created []*models.ScheduledArticle
	var cadenceSet int
	finalized := false
	st := &seMockStore{
		getWEC: func(ctx context.Context, id string) (bool, *models.WebEntityContext, error) {
			return true, &models.WebEntityContext{
				ID: wecID, WebEntityID: weID,
				Status:       models.SIEStatusSchedulingDone,
				SIEMode:      models.SIEModeTrial,
				UpgradeState: models.WECUpgradeStateExpanding,
			}, nil
		},
		getWebEntity: func(ctx context.Context, id string) (bool, *models.WebEntity, error) {
			return true, &models.WebEntity{ID: weID}, nil
		},
		setCadence: func(ctx context.Context, webEntityID string, articlesPerWeek int) error {
			cadenceSet = articlesPerWeek
			return nil
		},
		getScheduledByWEC: func(ctx context.Context, id string) ([]*models.ScheduledArticle, error) {
			return existingRows, nil
		},
		getKeywordsForWEC: func(ctx context.Context, id string) ([]models.Keyword, error) {
			return keywords, nil
		},
		createScheduled: func(ctx context.Context, articles []*models.ScheduledArticle) error {
			created = articles
			for _, a := range articles {
				a.ID = primitive.NewObjectID()
			}
			return nil
		},
		setSchedulingTotal: func(ctx context.Context, id string, total int) error {
			if total != 60 {
				t.Errorf("scheduling total = %d, want 60", total)
			}
			return nil
		},
		finalizeWECUpgrade: func(ctx context.Context, id string) error {
			finalized = true
			return nil
		},
	}
	d := &mockDispatcher{}
	svc := &schedulingEngineService{store: st, dispatcher: d, values: config.SchedulingValues{Weeks: 4, UpgradeCadence: 15}, trial: trialValues()}

	err := svc.ExtendSchedule(context.Background(), userID.Hex(), se.SEOrchestratePayload{
		WebEntityID:        weID.Hex(),
		WebEntityContextID: wecID.Hex(),
	})
	if err != nil {
		t.Fatalf("ExtendSchedule: %v", err)
	}

	if cadenceSet != 15 {
		t.Errorf("cadence set to %d, want 15", cadenceSet)
	}
	if len(created) != 55 {
		t.Fatalf("appended %d rows, want 55 (60 target − 5 existing)", len(created))
	}
	// Appended rows must never reuse an already-scheduled keyword.
	scheduled := map[primitive.ObjectID]bool{}
	for _, sa := range existingRows {
		scheduled[sa.KeywordID] = true
	}
	for _, sa := range created {
		if scheduled[sa.KeywordID] {
			t.Errorf("appended row reuses scheduled keyword %s", sa.KeywordID.Hex())
		}
		scheduled[sa.KeywordID] = true
	}
	if len(d.dispatched) != 55 {
		t.Errorf("dispatched %d fan-out messages, want 55 (new rows only)", len(d.dispatched))
	}
	if !finalized {
		t.Error("upgrade must be finalized after the append")
	}
}

// A redelivery after the append finds the target already met and only
// finalizes — no new rows, no fan-out.
func TestExtendScheduleIdempotentOnRedelivery(t *testing.T) {
	wecID := primitive.NewObjectID()
	weID := primitive.NewObjectID()

	full := make([]*models.ScheduledArticle, 60)
	for i := range full {
		full[i] = &models.ScheduledArticle{ID: primitive.NewObjectID(), KeywordID: primitive.NewObjectID()}
	}

	finalized := false
	st := &seMockStore{
		getWEC: func(ctx context.Context, id string) (bool, *models.WebEntityContext, error) {
			return true, &models.WebEntityContext{
				ID: wecID, WebEntityID: weID,
				Status:       models.SIEStatusSchedulingDone,
				SIEMode:      models.SIEModeTrial,
				UpgradeState: models.WECUpgradeStateExpanding,
			}, nil
		},
		getWebEntity: func(ctx context.Context, id string) (bool, *models.WebEntity, error) {
			return true, &models.WebEntity{ID: weID}, nil
		},
		setCadence:        func(ctx context.Context, webEntityID string, articlesPerWeek int) error { return nil },
		getScheduledByWEC: func(ctx context.Context, id string) ([]*models.ScheduledArticle, error) { return full, nil },
		finalizeWECUpgrade: func(ctx context.Context, id string) error {
			finalized = true
			return nil
		},
	}
	d := &mockDispatcher{}
	svc := &schedulingEngineService{store: st, dispatcher: d, values: config.SchedulingValues{Weeks: 4, UpgradeCadence: 15}, trial: trialValues()}

	if err := svc.ExtendSchedule(context.Background(), primitive.NewObjectID().Hex(), se.SEOrchestratePayload{
		WebEntityID: weID.Hex(), WebEntityContextID: wecID.Hex(),
	}); err != nil {
		t.Fatalf("ExtendSchedule: %v", err)
	}
	if !finalized {
		t.Error("redelivery with met target must still finalize")
	}
	if len(d.dispatched) != 0 {
		t.Errorf("redelivery dispatched %d messages, want 0", len(d.dispatched))
	}
}

// A redelivery after finalize is a pure no-op.
func TestExtendScheduleNoOpWhenComplete(t *testing.T) {
	wecID := primitive.NewObjectID()
	st := &seMockStore{
		getWEC: func(ctx context.Context, id string) (bool, *models.WebEntityContext, error) {
			return true, &models.WebEntityContext{
				ID:           wecID,
				Status:       models.SIEStatusSchedulingDone,
				SIEMode:      models.SIEModeFull,
				UpgradeState: models.WECUpgradeStateComplete,
			}, nil
		},
	}
	svc := &schedulingEngineService{store: st, dispatcher: &mockDispatcher{}, values: config.SchedulingValues{Weeks: 4, UpgradeCadence: 15}}
	if err := svc.ExtendSchedule(context.Background(), primitive.NewObjectID().Hex(), se.SEOrchestratePayload{
		WebEntityID: primitive.NewObjectID().Hex(), WebEntityContextID: wecID.Hex(),
	}); err != nil {
		t.Fatalf("ExtendSchedule after complete: %v", err)
	}
}
