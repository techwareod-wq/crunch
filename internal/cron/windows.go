package cron

import (
	"time"

	"github.com/atharva-ng/crunch/internal/util/log"
)

// MinuteWindow is one zone-local segment of a sweep occurrence's UTC window:
// "in zone Z, on local date D (weekday W), the minutes [FromMin, ToMin) are
// covered". This is the query shape behind per-user local times — resolvers
// run one indexed minute-range query per distinct zone instead of loading
// every user and computing local time in Go.
type MinuteWindow struct {
	Zone      string
	LocalDate string // "2026-08-08" — the segment's local calendar day
	Weekday   time.Weekday
	FromMin   int // inclusive, minutes since local midnight
	ToMin     int // exclusive
}

// ContainsMinute reports whether local minute-of-day m falls in the segment.
func (w MinuteWindow) ContainsMinute(m int) bool {
	return m >= w.FromMin && m < w.ToMin
}

// LocalWindows maps each zone onto the local minute segments the UTC window
// covers there. A window crossing local midnight yields two segments (tail of
// one day + head of the next), each with its own date and weekday.
//
// DST falls out of computing both endpoints from the zone every call:
//   - Spring-forward: the local range simply jumps ([01:57, 03:02) still
//     CONTAINS the nonexistent 02:xx minutes — those users fire, an hour early
//     on the clock but never lost).
//   - Fall-back: the local end reads EARLIER than the start although real time
//     moved forward. The segment is clamped to the window's true length from
//     the start minute; the repeated hour's second pass is a later window, and
//     day-scoped unit keys turn its duplicate deliveries into no-ops.
//
// Zones that fail to load are skipped with a log — one bad user-supplied zone
// must not sink the whole sweep.
func LocalWindows(w Window, zones []string) map[string][]MinuteWindow {
	out := make(map[string][]MinuteWindow, len(zones))
	length := int(w.End.Sub(w.Start) / time.Minute)
	if length <= 0 {
		return out
	}
	for _, zone := range zones {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			log.Warn("cron: skipping unloadable zone in sweep", "zone", zone, "error", err)
			continue
		}
		lo := w.Start.In(loc)
		hi := w.End.In(loc)
		loMin := lo.Hour()*60 + lo.Minute()
		hiMin := hi.Hour()*60 + hi.Minute()
		sameDay := lo.Year() == hi.Year() && lo.YearDay() == hi.YearDay()

		if sameDay {
			if hiMin <= loMin {
				// Fall-back wraparound: never fan a 5-minute window out over the
				// whole day — cover the true wall span from the start instead.
				hiMin = loMin + length
				if hiMin > minutesPerDay {
					hiMin = minutesPerDay
				}
			}
			out[zone] = []MinuteWindow{{
				Zone: zone, LocalDate: localDate(lo), Weekday: lo.Weekday(),
				FromMin: loMin, ToMin: hiMin,
			}}
			continue
		}
		segments := []MinuteWindow{}
		if loMin < minutesPerDay {
			segments = append(segments, MinuteWindow{
				Zone: zone, LocalDate: localDate(lo), Weekday: lo.Weekday(),
				FromMin: loMin, ToMin: minutesPerDay,
			})
		}
		if hiMin > 0 {
			segments = append(segments, MinuteWindow{
				Zone: zone, LocalDate: localDate(hi), Weekday: hi.Weekday(),
				FromMin: 0, ToMin: hiMin,
			})
		}
		out[zone] = segments
	}
	return out
}

const minutesPerDay = 24 * 60

// LocalDateLayout is the local calendar-day format used across the layer:
// MinuteWindow.LocalDate, day-scoped unit keys, and the beat bookkeeping
// stamps. Lexicographic order == chronological order by construction.
const LocalDateLayout = "2006-01-02"

func localDate(t time.Time) string {
	return t.Format(LocalDateLayout)
}
