package utils

import (
	"fmt"
	"time"
)

// cadenceConfig captures, for each supported `articles_per_week` value, which
// weekdays are publish days and how many slots each publish day holds.
type cadenceConfig struct {
	weekdays []time.Weekday
	perDay   int
}

var cadenceTable = map[int]cadenceConfig{
	3: {
		weekdays: []time.Weekday{time.Monday, time.Wednesday, time.Friday},
		perDay:   1,
	},
	5: {
		weekdays: []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday},
		perDay:   1,
	},
	10: {
		weekdays: []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday},
		perDay:   2,
	},
	15: {
		weekdays: []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday},
		perDay:   3,
	},
}

// SlotsPerWeek returns the total publish slots a cadence consumes per week.
// Errors on unsupported cadences so callers can fail fast at orchestrate time.
func SlotsPerWeek(articlesPerWeek int) (int, error) {
	cfg, ok := cadenceTable[articlesPerWeek]
	if !ok {
		return 0, fmt.Errorf("unsupported cadence: %d articles per week", articlesPerWeek)
	}
	return len(cfg.weekdays) * cfg.perDay, nil
}

// SlotsForWindow returns the total slots the rolling window holds for the
// given cadence — the value used as `top N` when selecting keywords. weeks is
// the rolling-window length (values.scheduling.weeks).
func SlotsForWindow(articlesPerWeek, weeks int) (int, error) {
	per, err := SlotsPerWeek(articlesPerWeek)
	if err != nil {
		return 0, err
	}
	return per * weeks, nil
}

// GenerateSlots returns publish slots in chronological order, starting from
// the first valid publish day on or after `anchor`. For cadences that publish
// multiple times per day, the same date is repeated `perDay` times — the
// caller is responsible for assigning different keywords to each repeat.
//
// Each returned time is normalised to 00:00 UTC of its publish day.
func GenerateSlots(articlesPerWeek int, anchor time.Time, totalSlots int) ([]time.Time, error) {
	cfg, ok := cadenceTable[articlesPerWeek]
	if !ok {
		return nil, fmt.Errorf("unsupported cadence: %d articles per week", articlesPerWeek)
	}

	publishDay := make(map[time.Weekday]bool, len(cfg.weekdays))
	for _, d := range cfg.weekdays {
		publishDay[d] = true
	}

	cursor := normaliseToMidnightUTC(anchor)
	slots := make([]time.Time, 0, totalSlots)

	for len(slots) < totalSlots {
		if publishDay[cursor.Weekday()] {
			for i := 0; i < cfg.perDay && len(slots) < totalSlots; i++ {
				slots = append(slots, cursor)
			}
		}
		cursor = cursor.AddDate(0, 0, 1)
	}
	return slots, nil
}

// normaliseToMidnightUTC strips the time-of-day component so every scheduled
// article anchors to 00:00 UTC of its calendar date — required by spec.
func normaliseToMidnightUTC(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
