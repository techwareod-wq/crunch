package cron

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/atharva-ng/crunch/internal/providers/interfaces"
	"github.com/atharva-ng/crunch/internal/util/log"
)

// Store is the scheduler's persistence seam: occurrence claims (exactly-once)
// and the run ledger (observability). The Mongo implementation lives in
// store.go; tests inject fakes.
type Store interface {
	// AcquireClaim takes (or takes over a stale unfinished) claim for the
	// occurrence. false with nil error = another node owns it.
	AcquireClaim(ctx context.Context, job string, occ time.Time, claimedBy string, staleAfter time.Duration) (bool, error)
	// CompleteClaim marks every unit of the occurrence dispatched.
	CompleteClaim(ctx context.Context, job string, occ time.Time) error
	// RecordRun appends one observability row. Best-effort at call sites.
	RecordRun(ctx context.Context, run RunRecord) error
}

// RunRecord mirrors models.CronRun without importing it here — the scheduler
// stays testable with no Mongo.
type RunRecord struct {
	Job        string
	Occurrence time.Time
	StartedAt  time.Time
	FinishedAt time.Time
	Candidates int
	Dispatched int
	Skipped    int
	Capped     int
	Error      string
	ClaimedBy  string
	Forced     bool
}

// SchedulerConfig wires a Scheduler.
type SchedulerConfig struct {
	Jobs       []Job
	Store      Store
	Dispatcher interfaces.Dispatcher
	// Tick is the sweep period (values cron.tickSeconds; 5 minutes). Every
	// job's CatchUp must exceed it and every Every interval must be ≥ it.
	Tick time.Duration
	// StaleAfter is the claim-takeover threshold: an unfinished claim older
	// than this is presumed dead and re-run (values cron.claimStaleSeconds).
	StaleAfter time.Duration
	// DefaultZone backs AtLocal specs with no explicit zone.
	DefaultZone string
	// Host identifies this node on claims and run rows.
	Host string
	// Now is the clock seam (tests). nil = time.Now.
	Now func() time.Time
}

// Scheduler is the in-process ticker. It never does work: each tick asks every
// enabled job for its latest occurrence, claims it in Mongo (exactly-once
// across N instances), runs the job's resolver, and enqueues one keyed message
// per unit. A tick with nobody due costs a handful of in-memory calls.
type Scheduler struct {
	jobs        []Job
	store       Store
	dispatcher  interfaces.Dispatcher
	tick        time.Duration
	staleAfter  time.Duration
	defaultZone string
	host        string
	now         func() time.Time

	// inFlight guards against a slow resolver stacking up under later ticks.
	mu       sync.Mutex
	inFlight map[JobName]bool
}

// NewScheduler validates the job set against the tick and builds the
// scheduler. Misconfigurations (CatchUp ≤ tick, sub-tick intervals, bad
// HH:MM/zone) fail here, at boot, not silently at 08:30.
func NewScheduler(cfg SchedulerConfig) (*Scheduler, error) {
	if cfg.Store == nil || cfg.Dispatcher == nil {
		return nil, fmt.Errorf("cron: scheduler needs a store and a dispatcher")
	}
	if cfg.Tick <= 0 {
		return nil, fmt.Errorf("cron: tick must be positive, got %s", cfg.Tick)
	}
	if cfg.StaleAfter <= 0 {
		return nil, fmt.Errorf("cron: claim stale threshold must be positive, got %s", cfg.StaleAfter)
	}
	seen := map[JobName]bool{}
	for _, j := range cfg.Jobs {
		if j.Name == "" || j.Resolve == nil || j.Process == "" {
			return nil, fmt.Errorf("cron: job %q needs a name, a resolver and a process type", j.Name)
		}
		if seen[j.Name] {
			return nil, fmt.Errorf("cron: duplicate job name %q", j.Name)
		}
		seen[j.Name] = true
		if err := j.Spec.Validate(cfg.Tick, cfg.DefaultZone); err != nil {
			return nil, fmt.Errorf("cron: job %q spec: %w", j.Name, err)
		}
		if j.CatchUp <= cfg.Tick {
			return nil, fmt.Errorf("cron: job %q CatchUp %s must exceed the tick %s (it could otherwise never fire)",
				j.Name, j.CatchUp, cfg.Tick)
		}
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Scheduler{
		jobs:        cfg.Jobs,
		store:       cfg.Store,
		dispatcher:  cfg.Dispatcher,
		tick:        cfg.Tick,
		staleAfter:  cfg.StaleAfter,
		defaultZone: cfg.DefaultZone,
		host:        cfg.Host,
		now:         now,
		inFlight:    map[JobName]bool{},
	}, nil
}

// Jobs returns the registered jobs (admin listing / run-now validation).
func (s *Scheduler) Jobs() []Job { return s.jobs }

// Start runs the tick loop until ctx is cancelled. The first sweep runs
// immediately — a deploy landing just after 09:00 should not sit idle for a
// full tick before noticing (the claim makes an overlapping old instance a
// non-event).
func (s *Scheduler) Start(ctx context.Context) {
	log.Info("cron scheduler starting", "jobs", len(s.jobs), "tick", s.tick)
	t := time.NewTicker(s.tick)
	defer t.Stop()
	s.sweep(ctx, s.now())
	for {
		select {
		case <-ctx.Done():
			log.Info("cron scheduler stopping")
			return
		case <-t.C:
			s.sweep(ctx, s.now())
		}
	}
}

// sweep fires each job's due-check concurrently. A job whose previous run is
// still in flight is skipped this tick — the claim it holds keeps the
// occurrence safe, and the next tick picks up whatever it left.
func (s *Scheduler) sweep(ctx context.Context, now time.Time) {
	for _, job := range s.jobs {
		s.mu.Lock()
		if s.inFlight[job.Name] {
			s.mu.Unlock()
			log.Warn("cron: job still in flight, skipping this tick", "job", job.Name)
			continue
		}
		s.inFlight[job.Name] = true
		s.mu.Unlock()

		go func(j Job) {
			defer func() {
				s.mu.Lock()
				delete(s.inFlight, j.Name)
				s.mu.Unlock()
			}()
			s.runJob(ctx, j, now)
		}(job)
	}
}

// runJob claims and executes every due occurrence of one job at tick instant
// now, oldest first.
func (s *Scheduler) runJob(ctx context.Context, job Job, now time.Time) {
	occs, err := s.dueOccurrences(job, now)
	if err != nil {
		log.Error("cron: computing occurrences failed", "job", job.Name, "error", err)
		return
	}
	for _, occ := range occs {
		acquired, err := s.store.AcquireClaim(ctx, string(job.Name), occ, s.host, s.staleAfter)
		if err != nil {
			log.Error("cron: claim failed", "job", job.Name, "occurrence", occ, "error", err)
			continue
		}
		if !acquired {
			continue // another node owns it — the entire exactly-once mechanism
		}
		s.runOccurrence(ctx, job, occ, false)
	}
}

// dueOccurrences lists the unexpired occurrences at tick instant now, oldest
// first: the latest one when now-occ ≤ CatchUp, plus — for CatchUpAll jobs —
// every earlier occurrence still inside the CatchUp window (each sweep window
// belongs to DIFFERENT users; skipping windows silently drops those users). A
// latest occurrence older than CatchUp is a logged misfire, not a fire.
func (s *Scheduler) dueOccurrences(job Job, now time.Time) ([]time.Time, error) {
	latest, err := job.Spec.LastOccurrence(now, s.tick, s.defaultZone)
	if err != nil {
		return nil, err
	}
	if now.Sub(latest) > job.CatchUp {
		// Not necessarily a misfire: every tick between catch-up expiry and
		// the next occurrence lands here (the occurrence usually ran hours
		// ago). A genuinely missed occurrence shows as a HOLE in cron_runs —
		// warn-logging here would cry wolf ~200 times a day per AtLocal job.
		log.Debug("cron: latest occurrence outside the catch-up window, nothing due",
			"job", job.Name, "occurrence", latest, "catchUp", job.CatchUp)
		return nil, nil
	}
	occs := []time.Time{latest}
	if job.CatchUpAll {
		occ := latest
		for {
			prev, err := job.Spec.PrevOccurrence(occ, s.tick, s.defaultZone)
			if err != nil {
				return nil, err
			}
			if now.Sub(prev) > job.CatchUp {
				break
			}
			occs = append(occs, prev)
			occ = prev
		}
	}
	// Oldest first, so catch-up fires in schedule order.
	for i, j := 0, len(occs)-1; i < j; i, j = i+1, j-1 {
		occs[i], occs[j] = occs[j], occs[i]
	}
	return occs, nil
}

// runOccurrence resolves and dispatches one claimed occurrence. The claim is
// completed only when EVERY unit was enqueued — a partial dispatch leaves the
// claim unfinished so a later tick takes it over and re-runs it, and the
// keyed message IDs make the already-dispatched units no-ops.
func (s *Scheduler) runOccurrence(ctx context.Context, job Job, occ time.Time, forced bool) RunRecord {
	rec := RunRecord{
		Job:        string(job.Name),
		Occurrence: occ,
		StartedAt:  s.now().UTC(),
		ClaimedBy:  s.host,
		Forced:     forced,
	}

	units, err := job.Resolve(ctx, Occurrence{
		Job:    job.Name,
		At:     occ,
		Window: job.Spec.OccurrenceWindow(occ, s.tick),
		Spec:   job.Spec,
	})
	if err != nil {
		rec.Error = fmt.Sprintf("resolve: %v", err)
		s.finishRun(ctx, rec)
		log.Error("cron: resolver failed", "job", job.Name, "occurrence", occ, "error", err)
		return rec
	}
	rec.Candidates = len(units)

	if job.MaxUnits > 0 && len(units) > job.MaxUnits {
		rec.Capped = len(units) - job.MaxUnits
		units = units[:job.MaxUnits]
		log.Warn("cron: fan-out capped — overflow deferred, NOT silently covered",
			"job", job.Name, "occurrence", occ, "capped", rec.Capped, "maxUnits", job.MaxUnits)
	}

	var dispatchErr error
	for _, u := range units {
		key := u.IdempotencyKey
		if key == "" {
			key = DefaultUnitKey(job.Name, occ, u.UserID)
		}
		if err := s.dispatcher.DispatchKeyed(ctx, string(job.Process), u.UserID, key, u.Payload); err != nil {
			dispatchErr = err
			log.Error("cron: unit dispatch failed", "job", job.Name, "occurrence", occ, "user", u.UserID, "error", err)
			continue
		}
		rec.Dispatched++
	}
	rec.Skipped = rec.Candidates - rec.Dispatched - rec.Capped

	if dispatchErr != nil {
		// Leave the claim unfinished: the stale takeover re-runs the resolver
		// and the unit keys skip what already went out.
		rec.Error = fmt.Sprintf("dispatch: %v", dispatchErr)
		s.finishRun(ctx, rec)
		return rec
	}

	if !forced {
		if err := s.store.CompleteClaim(ctx, string(job.Name), occ); err != nil {
			// Worst case: a stale takeover re-runs a finished occurrence and the
			// unit keys make it a no-op. Log, don't fail the run.
			log.Error("cron: completing claim failed", "job", job.Name, "occurrence", occ, "error", err)
		}
	}
	s.finishRun(ctx, rec)
	log.Info("cron: occurrence dispatched",
		"job", job.Name, "occurrence", occ,
		"candidates", rec.Candidates, "dispatched", rec.Dispatched, "capped", rec.Capped, "forced", forced)
	return rec
}

func (s *Scheduler) finishRun(ctx context.Context, rec RunRecord) {
	rec.FinishedAt = s.now().UTC()
	if err := s.store.RecordRun(ctx, rec); err != nil {
		log.Error("cron: recording run failed", "job", rec.Job, "occurrence", rec.Occurrence, "error", err)
	}
}

// ForceRun executes jobName for its latest occurrence NOW, bypassing the
// enabled flag's schedule and the claim. Unit keys still apply, so a force-run
// cannot double-deliver what the schedule already delivered.
func (s *Scheduler) ForceRun(ctx context.Context, jobName string) (RunRecord, error) {
	return s.ForceRunAt(ctx, jobName, time.Time{})
}

// ForceRunAt is the admin "run it this second" seam (the cheapest substitute
// for the n8n Sim In webhook). A zero occ means "the latest occurrence", which
// is the whole story only for SpecAtLocal, whose LastOccurrence walks back to a
// real scheduled instant.
//
// For the sweep-shaped specs it is NOT: SpecAtUserLocal and SpecEvery truncate
// now to the tick, so the forged window is simply "the last few minutes", and
// the resolvers then filter it by target minute and per-zone weekday. Forcing
// such a job outside its window therefore resolved 0 candidates and
// looked like a broken beat when the seam was the thing that could not express
// the question. Pass occ explicitly to aim at the window you actually want to
// test — 03:00Z reaches 08:30 in Asia/Kolkata — and the unit keys still stop a
// forced rehearsal from double-delivering a real day's beat.
func (s *Scheduler) ForceRunAt(ctx context.Context, jobName string, occ time.Time) (RunRecord, error) {
	for _, job := range s.jobs {
		if string(job.Name) != jobName {
			continue
		}
		if occ.IsZero() {
			last, err := job.Spec.LastOccurrence(s.now(), s.tick, s.defaultZone)
			if err != nil {
				return RunRecord{}, fmt.Errorf("cron: force-run %q: %w", jobName, err)
			}
			occ = last
		}
		return s.runOccurrence(ctx, job, occ.UTC(), true), nil
	}
	return RunRecord{}, fmt.Errorf("cron: no enabled job named %q", jobName)
}
