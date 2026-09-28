// Package cron is the time-trigger layer: an in-process 5-minute ticker that
// resolves "who is due" per registered job and enqueues one async message per
// due unit onto the existing asyncHandler/SQS spine.
//
// The rule the whole layer rests on: A CRON JOB NEVER DOES WORK. It resolves
// who is due and enqueues. Retries, DLQ, timeouts and LLM token-gating are all
// inherited from the async handler; the cron layer needs no worker pool and no
// error policy of its own. See docs/CRON_SCHEDULER_DESIGN.md.
package cron

import (
	"context"
	"fmt"
	"time"

	"github.com/atharva-ng/crunch/internal/pipeline"
)

// JobName is a job's stable identity — the claim key and the unit-key prefix.
// NEVER change an existing name: in-flight claims and dedupe keys carry it.
type JobName string

// Job is one scheduled trigger. A feature owns its Resolve; the framework owns
// time, exactly-once, fan-out and observability.
type Job struct {
	Name    JobName              // "demo_heartbeat" — the claim key
	Spec    Spec                 // when (spec.go)
	Resolve Resolver             // who is due
	Process pipeline.ProcessType // what gets enqueued per unit

	// CatchUp is how long after an occurrence the job may still fire (a restart
	// spanning 08:30 fires the nudge at 08:37, not never). Must exceed the
	// scheduler tick or the job can never fire at all — validated at build.
	CatchUp time.Duration
	// CatchUpAll fires EVERY missed occurrence in the CatchUp window instead of
	// only the newest. Sweep jobs need it: each window belongs to different
	// users, so skipping windows silently drops those users.
	CatchUpAll bool
	// MaxUnits caps the fan-out per occurrence; 0 = uncapped. Overflow is
	// LOGGED and recorded on the run row, never silently dropped.
	MaxUnits int
}

// Resolver returns the units due for one occurrence. One cheap indexed query —
// no per-user state assembly, no work. Pure (Occurrence) → []Unit by design so
// every resolver is unit-testable with no clock, no queue and no LLM.
type Resolver func(ctx context.Context, occ Occurrence) ([]Unit, error)

// Occurrence is one scheduled firing the claim was taken for.
type Occurrence struct {
	Job JobName
	// At is the scheduled instant, UTC — for sweep jobs the window's start
	// boundary, for AtLocal jobs the local HH:MM converted to UTC.
	At time.Time
	// Window is [Start, End): the UTC range this occurrence owns. Sweep jobs
	// ([At, At+interval)) match per-user minutes inside it via LocalWindows;
	// AtLocal jobs carry the empty window [At, At).
	Window Window
	// Spec is the job's EFFECTIVE schedule (code default + values overlay) —
	// how an AtUserLocal resolver reads its target minute and weekday mask
	// without capturing a stale copy at registry-build time.
	Spec Spec
}

// Window is a half-open UTC time range.
type Window struct {
	Start time.Time
	End   time.Time
}

// Unit is one enqueueable piece of due work.
type Unit struct {
	UserID  string
	Payload any
	// IdempotencyKey is the message ID the dispatch is keyed on — the consumer
	// idempotency gate collapses duplicate dispatches of the same key. Empty
	// uses DefaultUnitKey (per job+occurrence+user). Set it only to widen the
	// dedupe: per-entity jobs widen to the entity (an articleID — the per-user
	// default would dedupe a user's second due article into oblivion);
	// user-facing beats widen to the local DAY (one nudge per day is the
	// product invariant, and a day key kills every mechanical double-fire at
	// once — CatchUpAll windows, DST fall-back repeats, claim-takeover reruns).
	IdempotencyKey string
}

// unitKeyPrefix namespaces every cron unit key in the shared
// processed_messages ledger.
const unitKeyPrefix = "cron"

// UnitKey builds a unit idempotency key: cron:<job>:<scope>:<id>. The scope
// picks the dedupe granularity — an occurrence instant for the default, a
// local DAY for user-facing beats — and id the dedupe subject (userID, or an
// entity id for per-entity jobs). Every key the layer emits goes through
// here; the format is load-bearing (it IS the ledger key) and must not fork.
func UnitKey(job JobName, scope, id string) string {
	return fmt.Sprintf("%s:%s:%s:%s", unitKeyPrefix, job, scope, id)
}

// DefaultUnitKey is the ledger key a Unit gets when it doesn't set its own:
// occurrence-scoped, per user.
func DefaultUnitKey(job JobName, occ time.Time, userID string) string {
	return UnitKey(job, occ.UTC().Format(time.RFC3339), userID)
}
