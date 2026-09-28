package analytics

import (
	"time"

	"github.com/atharva-ng/crunch/internal/util/log"
)

// Centralized source-time (LLD §3.1b). GSC (and GA4) report data as days in
// the source's reporting zone, not UTC — dates are stored verbatim as the
// source reports them, and every "what day is settled/fetchable" computation
// goes through here. NO other package computes analytics dates.

// DateLayout is the storage format of every analytics date string.
const DateLayout = "2006-01-02"

// clockNow is the injection seam for tests; production always uses time.Now.
var clockNow = time.Now

// sourceToday is the current calendar day in the source's reporting zone. An
// unloadable zone falls back to UTC with a warning — a wrong-but-close day
// beats a dead ingest beat.
func sourceToday(src Source) time.Time {
	sched := src.Schedule()
	loc := time.UTC
	if sched.ReportingZone != "" {
		l, err := time.LoadLocation(sched.ReportingZone)
		if err != nil {
			log.Warn("analytics: unloadable reporting zone, using UTC",
				"source", src.Name(), "zone", sched.ReportingZone, "error", err)
		} else {
			loc = l
		}
	}
	now := clockNow().In(loc)
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
}

// LatestSettledDate is the newest date the source's numbers are stable for:
// today − LagDays in the reporting zone (GSC: D−2).
func LatestSettledDate(src Source) string {
	return sourceToday(src).AddDate(0, 0, -src.Schedule().LagDays).Format(DateLayout)
}

// IngestWindow is the window one ingest run covers (LLD "clock.FetchWindow").
// Daily sources get the rolling re-fetch window
// [today−(LagDays+WindowDays−1) … today−LagDays]; monthly sources get the
// current month's first day as a single-date window.
func IngestWindow(src Source) DateWindow {
	sched := src.Schedule()
	today := sourceToday(src)
	if sched.Cadence == "monthly" {
		first := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, today.Location()).Format(DateLayout)
		return DateWindow{From: first, To: first}
	}
	span := sched.WindowDays
	if span <= 0 {
		span = 1
	}
	to := today.AddDate(0, 0, -sched.LagDays)
	from := to.AddDate(0, 0, -(span - 1))
	return DateWindow{From: from.Format(DateLayout), To: to.Format(DateLayout)}
}

// ClampRange bounds a caller-supplied inclusive [from, to] to the source's
// settled data: to never exceeds LatestSettledDate, from never exceeds to.
// Used by read endpoints and replay; date strings compare lexicographically.
func ClampRange(src Source, from, to string) DateWindow {
	settled := LatestSettledDate(src)
	if to == "" || to > settled {
		to = settled
	}
	if from > to {
		from = to
	}
	return DateWindow{From: from, To: to}
}
