package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// SpecKind is the timing shape of a job.
type SpecKind int

const (
	// SpecAtLocal fires at a fixed wall-clock time in ONE zone (e.g. a global
	// 09:00 sweep). One occurrence per allowed day.
	SpecAtLocal SpecKind = iota
	// SpecAtUserLocal fires at a fixed wall-clock minute in EACH USER'S OWN
	// zone (the 17:00 weekly check). Mechanically a sweep: every tick owns a
	// window, and the resolver's zone-grouped query matches users whose zone
	// currently sits inside the target minute's window — weekday evaluated per
	// zone too (Friday in Auckland while Kolkata is still Thursday must not
	// fire for Kolkata).
	SpecAtUserLocal
	// SpecEvery fires once per interval window — the shape behind user-CHOSEN
	// time sweeps (digest at 22:17): the window mechanism guarantees no minute
	// is ever skipped, with delivery lag bounded by the interval.
	SpecEvery
)

// Spec is a typed schedule — no cron-string dependency, and expressive enough
// for "the user's own local 22:17", which a cron string is not.
type Spec struct {
	Kind     SpecKind
	HourMin  string         // "08:30" — AtLocal / AtUserLocal
	Weekdays []time.Weekday // empty = every day
	Zone     string         // IANA; "" = the scheduler's default zone (AtLocal only)
	Interval time.Duration  // Every; for AtUserLocal the scheduler tick is used
	// DayOfMonth restricts an AtLocal spec to one calendar day per month
	// (monthly analytics sources); 0 = every allowed day. Days 29–31 fire only
	// in months that have them — the occurrence walk simply lands on the
	// previous month's matching day, which sits outside CatchUp and is a
	// logged non-fire, not a double-fire.
	DayOfMonth int
}

// AtLocal fires daily at hhmm ("09:00") in one zone (InZone, or the default).
func AtLocal(hhmm string) Spec { return Spec{Kind: SpecAtLocal, HourMin: hhmm} }

// AtUserLocal fires at hhmm in each user's own zone.
func AtUserLocal(hhmm string) Spec { return Spec{Kind: SpecAtUserLocal, HourMin: hhmm} }

// Every fires once per interval window.
func Every(d time.Duration) Spec { return Spec{Kind: SpecEvery, Interval: d} }

// InZone pins an AtLocal spec to an IANA zone.
func (s Spec) InZone(zone string) Spec { s.Zone = zone; return s }

// OnDayOfMonth restricts an AtLocal spec to one calendar day per month.
func (s Spec) OnDayOfMonth(day int) Spec { s.DayOfMonth = day; return s }

// OnWeekdays restricts firing to the given weekdays; no arguments means the
// working week (Mon–Fri).
func (s Spec) OnWeekdays(days ...time.Weekday) Spec {
	if len(days) == 0 {
		days = []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday}
	}
	s.Weekdays = days
	return s
}

// MinuteOfDay returns HourMin as minutes since local midnight — what
// AtUserLocal resolvers compare their zone windows against.
func (s Spec) MinuteOfDay() (int, error) {
	h, m, err := parseHourMin(s.HourMin)
	if err != nil {
		return 0, err
	}
	return h*60 + m, nil
}

// AllowsWeekday reports whether the spec fires on d (empty Weekdays = all).
func (s Spec) AllowsWeekday(d time.Weekday) bool {
	if len(s.Weekdays) == 0 {
		return true
	}
	for _, w := range s.Weekdays {
		if w == d {
			return true
		}
	}
	return false
}

// Validate checks the spec against the scheduler tick. Sweep intervals finer
// than the tick would systematically skip windows, so they are a build error,
// not a runtime surprise.
func (s Spec) Validate(tick time.Duration, defaultZone string) error {
	switch s.Kind {
	case SpecAtLocal:
		if _, _, err := parseHourMin(s.HourMin); err != nil {
			return err
		}
		zone := s.Zone
		if zone == "" {
			zone = defaultZone
		}
		if _, err := time.LoadLocation(zone); err != nil {
			return fmt.Errorf("bad zone %q: %w", zone, err)
		}
		if s.DayOfMonth < 0 || s.DayOfMonth > 31 {
			return fmt.Errorf("dayOfMonth %d out of range 1..31", s.DayOfMonth)
		}
	case SpecAtUserLocal:
		if _, _, err := parseHourMin(s.HourMin); err != nil {
			return err
		}
	case SpecEvery:
		if s.Interval <= 0 {
			return fmt.Errorf("Every spec needs a positive interval")
		}
		if s.Interval < tick {
			return fmt.Errorf("Every interval %s is finer than the scheduler tick %s", s.Interval, tick)
		}
	default:
		return fmt.Errorf("unknown spec kind %d", s.Kind)
	}
	return nil
}

// sweepInterval is the window size of a sweep-shaped spec: the declared
// interval for Every, the scheduler tick for AtUserLocal.
func (s Spec) sweepInterval(tick time.Duration) time.Duration {
	if s.Kind == SpecEvery {
		return s.Interval
	}
	return tick
}

// LastOccurrence is the only timing question the scheduler asks: the most
// recent scheduled instant ≤ now. Stateless — no lastRunAt to track, no drift
// to correct; 08:30 is never delivered to the process, the process notices on
// some tick that 08:30 has passed.
func (s Spec) LastOccurrence(now time.Time, tick time.Duration, defaultZone string) (time.Time, error) {
	switch s.Kind {
	case SpecAtLocal:
		return s.lastAtLocal(now, defaultZone)
	case SpecAtUserLocal, SpecEvery:
		return now.UTC().Truncate(s.sweepInterval(tick)), nil
	}
	return time.Time{}, fmt.Errorf("unknown spec kind %d", s.Kind)
}

// PrevOccurrence steps one occurrence back from occ — the CatchUp walk.
func (s Spec) PrevOccurrence(occ time.Time, tick time.Duration, defaultZone string) (time.Time, error) {
	switch s.Kind {
	case SpecAtLocal:
		// Strictly before occ: ask for the last occurrence a moment earlier.
		return s.lastAtLocal(occ.Add(-time.Second), defaultZone)
	case SpecAtUserLocal, SpecEvery:
		return occ.Add(-s.sweepInterval(tick)), nil
	}
	return time.Time{}, fmt.Errorf("unknown spec kind %d", s.Kind)
}

// OccurrenceWindow is the UTC range occ owns: [occ, occ+interval) for sweeps,
// the empty [occ, occ) for AtLocal.
func (s Spec) OccurrenceWindow(occ time.Time, tick time.Duration) Window {
	if s.Kind == SpecAtLocal {
		return Window{Start: occ, End: occ}
	}
	return Window{Start: occ, End: occ.Add(s.sweepInterval(tick))}
}

// lastAtLocal finds the latest HH:MM instant ≤ now in the spec's zone whose
// local weekday (and, when DayOfMonth is set, calendar day) is allowed. Walks
// back at most a week + a day — enough for any weekday mask — or 62 days for a
// DayOfMonth spec (covers a Feb with no day 30/31 plus a weekday mask).
func (s Spec) lastAtLocal(now time.Time, defaultZone string) (time.Time, error) {
	zone := s.Zone
	if zone == "" {
		zone = defaultZone
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return time.Time{}, fmt.Errorf("load zone %q: %w", zone, err)
	}
	h, m, err := parseHourMin(s.HourMin)
	if err != nil {
		return time.Time{}, err
	}

	maxWalk := 8
	if s.DayOfMonth > 0 {
		maxWalk = 62
	}
	local := now.In(loc)
	for day := 0; day <= maxWalk; day++ {
		d := local.AddDate(0, 0, -day)
		candidate := time.Date(d.Year(), d.Month(), d.Day(), h, m, 0, 0, loc)
		if candidate.After(now) || !s.AllowsWeekday(candidate.Weekday()) {
			continue
		}
		if s.DayOfMonth > 0 && candidate.Day() != s.DayOfMonth {
			continue
		}
		return candidate.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("no occurrence found for %q within %d days", s.HourMin, maxWalk)
}

func parseHourMin(hhmm string) (int, int, error) {
	parts := strings.SplitN(hhmm, ":", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("bad HH:MM %q", hhmm)
	}
	h, err := strconv.Atoi(parts[0])
	if err != nil || h < 0 || h > 23 {
		return 0, 0, fmt.Errorf("bad hour in %q", hhmm)
	}
	m, err := strconv.Atoi(parts[1])
	if err != nil || m < 0 || m > 59 {
		return 0, 0, fmt.Errorf("bad minute in %q", hhmm)
	}
	return h, m, nil
}

// ParseWeekdays reads the values-file weekday syntax: "1-5", "4", "1,3,5" —
// cron numbering, 0 = Sunday.
func ParseWeekdays(expr string) ([]time.Weekday, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, nil
	}
	var days []time.Weekday
	for _, part := range strings.Split(expr, ",") {
		part = strings.TrimSpace(part)
		if lo, hi, ok := strings.Cut(part, "-"); ok {
			from, err1 := strconv.Atoi(lo)
			to, err2 := strconv.Atoi(hi)
			if err1 != nil || err2 != nil || from < 0 || to > 6 || from > to {
				return nil, fmt.Errorf("bad weekday range %q", part)
			}
			for d := from; d <= to; d++ {
				days = append(days, time.Weekday(d))
			}
			continue
		}
		d, err := strconv.Atoi(part)
		if err != nil || d < 0 || d > 6 {
			return nil, fmt.Errorf("bad weekday %q", part)
		}
		days = append(days, time.Weekday(d))
	}
	return days, nil
}
