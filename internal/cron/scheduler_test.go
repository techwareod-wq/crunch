package cron

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeStore is an in-memory claim table + run log.
type fakeStore struct {
	mu        sync.Mutex
	claims    map[string]claimRow
	runs      []RunRecord
	claimErr  error
	staleTime time.Time // claimed_at stamped on new claims (for stale tests)
}

type claimRow struct {
	claimedAt time.Time
	completed bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{claims: map[string]claimRow{}}
}

func claimKey(job string, occ time.Time) string {
	return job + "@" + occ.UTC().Format(time.RFC3339)
}

func (f *fakeStore) AcquireClaim(_ context.Context, job string, occ time.Time, _ string, staleAfter time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.claimErr != nil {
		return false, f.claimErr
	}
	key := claimKey(job, occ)
	row, exists := f.claims[key]
	now := time.Now()
	if !exists {
		claimedAt := now
		if !f.staleTime.IsZero() {
			claimedAt = f.staleTime
		}
		f.claims[key] = claimRow{claimedAt: claimedAt}
		return true, nil
	}
	if !row.completed && now.Sub(row.claimedAt) > staleAfter {
		row.claimedAt = now
		f.claims[key] = row
		return true, nil
	}
	return false, nil
}

func (f *fakeStore) CompleteClaim(_ context.Context, job string, occ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := f.claims[claimKey(job, occ)]
	row.completed = true
	f.claims[claimKey(job, occ)] = row
	return nil
}

func (f *fakeStore) RecordRun(_ context.Context, run RunRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, run)
	return nil
}

// fakeDispatcher records keyed dispatches; failKeys makes chosen units fail.
type fakeDispatcher struct {
	mu       sync.Mutex
	sent     []sentMsg
	failKeys map[string]bool
}

type sentMsg struct {
	Process string
	UserID  string
	Key     string
}

func (f *fakeDispatcher) Dispatch(ctx context.Context, processType, userID string, payload any) error {
	return f.DispatchKeyed(ctx, processType, userID, "", payload)
}

func (f *fakeDispatcher) DispatchKeyed(_ context.Context, processType, userID, key string, _ any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failKeys[key] {
		return errors.New("enqueue failed")
	}
	f.sent = append(f.sent, sentMsg{Process: processType, UserID: userID, Key: key})
	return nil
}

func fixedNow(t *testing.T, value string) func() time.Time {
	ts := mustUTC(t, value)
	return func() time.Time { return ts }
}

func testScheduler(t *testing.T, jobs []Job, store Store, disp *fakeDispatcher, now func() time.Time) *Scheduler {
	t.Helper()
	s, err := NewScheduler(SchedulerConfig{
		Jobs:        jobs,
		Store:       store,
		Dispatcher:  disp,
		Tick:        testTick,
		StaleAfter:  10 * time.Minute,
		DefaultZone: kolkata,
		Host:        "test-host",
		Now:         now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func staticResolver(units []Unit) Resolver {
	return func(context.Context, Occurrence) ([]Unit, error) { return units, nil }
}

func TestRunJob_FiresLatestOccurrenceOnce(t *testing.T) {
	store := newFakeStore()
	disp := &fakeDispatcher{}
	now := fixedNow(t, "2026-08-07T03:33:07Z") // 09:03 IST — 09:00 just passed
	job := Job{
		Name:    "test_daily",
		Spec:    AtLocal("09:00").InZone(kolkata),
		Resolve: staticResolver([]Unit{{UserID: "u1"}, {UserID: "u2"}}),
		Process: "TEST_PROC",
		CatchUp: time.Hour,
	}
	s := testScheduler(t, []Job{job}, store, disp, now)

	s.runJob(context.Background(), job, now())
	if len(disp.sent) != 2 {
		t.Fatalf("dispatched %d, want 2", len(disp.sent))
	}
	wantOcc := mustUTC(t, "2026-08-07T03:30:00Z")
	wantKey := DefaultUnitKey("test_daily", wantOcc, "u1")
	if disp.sent[0].Key != wantKey {
		t.Errorf("key = %q, want %q", disp.sent[0].Key, wantKey)
	}
	if !store.claims[claimKey("test_daily", wantOcc)].completed {
		t.Error("claim must be completed after full dispatch")
	}
	if len(store.runs) != 1 || store.runs[0].Dispatched != 2 || store.runs[0].Error != "" {
		t.Errorf("run record = %+v", store.runs)
	}

	// A second tick sees the same occurrence: the claim eats it.
	s.runJob(context.Background(), job, now())
	if len(disp.sent) != 2 {
		t.Errorf("second tick re-dispatched: %d sends", len(disp.sent))
	}
}

func TestRunJob_MisfireOutsideCatchUp(t *testing.T) {
	store := newFakeStore()
	disp := &fakeDispatcher{}
	now := fixedNow(t, "2026-08-07T09:30:00Z") // 15:00 IST — six hours past 09:00
	job := Job{
		Name:    "test_daily",
		Spec:    AtLocal("09:00").InZone(kolkata),
		Resolve: staticResolver([]Unit{{UserID: "u1"}}),
		Process: "TEST_PROC",
		CatchUp: time.Hour,
	}
	s := testScheduler(t, []Job{job}, store, disp, now)
	s.runJob(context.Background(), job, now())
	if len(disp.sent) != 0 || len(store.claims) != 0 {
		t.Errorf("misfired occurrence must not fire: sent=%d claims=%d", len(disp.sent), len(store.claims))
	}
}

func TestRunJob_CatchUpAllWalksEveryWindow(t *testing.T) {
	store := newFakeStore()
	disp := &fakeDispatcher{}
	now := fixedNow(t, "2026-08-07T05:12:00Z")
	var got []Occurrence
	job := Job{
		Name: "test_sweep",
		Spec: Every(5 * time.Minute),
		Resolve: func(_ context.Context, occ Occurrence) ([]Unit, error) {
			got = append(got, occ)
			return nil, nil
		},
		Process:    "TEST_PROC",
		CatchUp:    15 * time.Minute,
		CatchUpAll: true,
	}
	s := testScheduler(t, []Job{job}, store, disp, now)
	s.runJob(context.Background(), job, now())

	// Windows within 15m of 05:12: 05:10, 05:05, 05:00 (04:55 is 17m old).
	if len(got) != 3 {
		t.Fatalf("resolved %d occurrences, want 3: %+v", len(got), got)
	}
	if !got[0].At.Equal(mustUTC(t, "2026-08-07T05:00:00Z")) {
		t.Errorf("catch-up must fire oldest first, got %v", got[0].At)
	}
	for _, occ := range got {
		if !occ.Window.End.Equal(occ.Window.Start.Add(5 * time.Minute)) {
			t.Errorf("sweep window = [%v, %v)", occ.Window.Start, occ.Window.End)
		}
	}
}

func TestRunOccurrence_PartialDispatchLeavesClaimOpen(t *testing.T) {
	store := newFakeStore()
	occ := mustUTC(t, "2026-08-07T03:30:00Z")
	failKey := DefaultUnitKey("test_daily", occ, "u2")
	disp := &fakeDispatcher{failKeys: map[string]bool{failKey: true}}
	now := fixedNow(t, "2026-08-07T03:33:07Z")
	job := Job{
		Name:    "test_daily",
		Spec:    AtLocal("09:00").InZone(kolkata),
		Resolve: staticResolver([]Unit{{UserID: "u1"}, {UserID: "u2"}, {UserID: "u3"}}),
		Process: "TEST_PROC",
		CatchUp: time.Hour,
	}
	s := testScheduler(t, []Job{job}, store, disp, now)
	s.runJob(context.Background(), job, now())

	if store.claims[claimKey("test_daily", occ)].completed {
		t.Error("claim must stay open after a failed unit dispatch")
	}
	if len(store.runs) != 1 || !strings.HasPrefix(store.runs[0].Error, "dispatch:") {
		t.Errorf("run record = %+v", store.runs)
	}
	if store.runs[0].Dispatched != 2 {
		t.Errorf("dispatched = %d, want 2 of 3", store.runs[0].Dispatched)
	}
}

func TestRunOccurrence_MaxUnitsCapIsRecorded(t *testing.T) {
	store := newFakeStore()
	disp := &fakeDispatcher{}
	now := fixedNow(t, "2026-08-07T03:33:07Z")
	units := make([]Unit, 5)
	for i := range units {
		units[i] = Unit{UserID: fmt.Sprintf("u%d", i)}
	}
	job := Job{
		Name:     "test_daily",
		Spec:     AtLocal("09:00").InZone(kolkata),
		Resolve:  staticResolver(units),
		Process:  "TEST_PROC",
		CatchUp:  time.Hour,
		MaxUnits: 3,
	}
	s := testScheduler(t, []Job{job}, store, disp, now)
	s.runJob(context.Background(), job, now())
	if len(disp.sent) != 3 {
		t.Errorf("dispatched %d, want capped 3", len(disp.sent))
	}
	if store.runs[0].Capped != 2 || store.runs[0].Candidates != 5 {
		t.Errorf("run record = %+v", store.runs[0])
	}
}

func TestRunJob_StaleClaimIsTakenOver(t *testing.T) {
	store := newFakeStore()
	store.staleTime = time.Now().Add(-time.Hour) // first claim looks long dead
	disp := &fakeDispatcher{}
	now := fixedNow(t, "2026-08-07T03:33:07Z")
	job := Job{
		Name:    "test_daily",
		Spec:    AtLocal("09:00").InZone(kolkata),
		Resolve: staticResolver([]Unit{{UserID: "u1"}}),
		Process: "TEST_PROC",
		CatchUp: time.Hour,
	}
	s := testScheduler(t, []Job{job}, store, disp, now)

	occ := mustUTC(t, "2026-08-07T03:30:00Z")
	// Simulate the dead node: claim exists, never completed, gone stale.
	if ok, _ := store.AcquireClaim(context.Background(), "test_daily", occ, "dead-node", 10*time.Minute); !ok {
		t.Fatal("seed claim failed")
	}
	store.staleTime = time.Time{}

	s.runJob(context.Background(), job, now())
	if len(disp.sent) != 1 {
		t.Errorf("stale unfinished claim must be taken over and re-run, sent=%d", len(disp.sent))
	}
}

func TestForceRun_BypassesClaimButKeepsKeys(t *testing.T) {
	store := newFakeStore()
	disp := &fakeDispatcher{}
	now := fixedNow(t, "2026-08-07T03:33:07Z")
	job := Job{
		Name:    "test_daily",
		Spec:    AtLocal("09:00").InZone(kolkata),
		Resolve: staticResolver([]Unit{{UserID: "u1"}}),
		Process: "TEST_PROC",
		CatchUp: time.Hour,
	}
	s := testScheduler(t, []Job{job}, store, disp, now)

	rec, err := s.ForceRun(context.Background(), "test_daily")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Dispatched != 1 || !rec.Forced {
		t.Errorf("force-run record = %+v", rec)
	}
	if len(store.claims) != 0 {
		t.Error("force-run must not claim the occurrence")
	}
	occ := mustUTC(t, "2026-08-07T03:30:00Z")
	if disp.sent[0].Key != DefaultUnitKey("test_daily", occ, "u1") {
		t.Errorf("force-run key = %q", disp.sent[0].Key)
	}

	if _, err := s.ForceRun(context.Background(), "no_such_job"); err == nil {
		t.Error("unknown job must error")
	}
}

func TestNewScheduler_Validation(t *testing.T) {
	store := newFakeStore()
	disp := &fakeDispatcher{}
	good := Job{
		Name:    "a",
		Spec:    Every(5 * time.Minute),
		Resolve: staticResolver(nil),
		Process: "P",
		CatchUp: 15 * time.Minute,
	}
	base := func(jobs []Job) SchedulerConfig {
		return SchedulerConfig{Jobs: jobs, Store: store, Dispatcher: disp, Tick: testTick, StaleAfter: 10 * time.Minute, DefaultZone: kolkata}
	}

	if _, err := NewScheduler(base([]Job{good})); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}

	catchupTooSmall := good
	catchupTooSmall.CatchUp = testTick // must EXCEED the tick
	if _, err := NewScheduler(base([]Job{catchupTooSmall})); err == nil {
		t.Error("CatchUp == tick must be rejected")
	}

	dup := good
	if _, err := NewScheduler(base([]Job{good, dup})); err == nil {
		t.Error("duplicate job names must be rejected")
	}

	noResolver := good
	noResolver.Name = "b"
	noResolver.Resolve = nil
	if _, err := NewScheduler(base([]Job{noResolver})); err == nil {
		t.Error("missing resolver must be rejected")
	}

	subTick := good
	subTick.Name = "c"
	subTick.Spec = Every(time.Minute)
	if _, err := NewScheduler(base([]Job{subTick})); err == nil {
		t.Error("sub-tick interval must be rejected")
	}
}
