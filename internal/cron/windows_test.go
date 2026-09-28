package cron

import (
	"testing"
	"time"
)

func window(t *testing.T, start string, length time.Duration) Window {
	t.Helper()
	s := mustUTC(t, start)
	return Window{Start: s, End: s.Add(length)}
}

func TestLocalWindows_PlainSegment(t *testing.T) {
	// [05:00, 05:05) UTC = [10:30, 10:35) IST on the same local day.
	w := window(t, "2026-08-07T05:00:00Z", 5*time.Minute)
	segs := LocalWindows(w, []string{kolkata})[kolkata]
	if len(segs) != 1 {
		t.Fatalf("segments = %d, want 1", len(segs))
	}
	s := segs[0]
	if s.FromMin != 10*60+30 || s.ToMin != 10*60+35 {
		t.Errorf("segment = [%d, %d), want [630, 635)", s.FromMin, s.ToMin)
	}
	if s.LocalDate != "2026-08-07" || s.Weekday != time.Friday {
		t.Errorf("segment date = %s %v, want 2026-08-07 Friday", s.LocalDate, s.Weekday)
	}
	if !s.ContainsMinute(632) || s.ContainsMinute(635) {
		t.Error("ContainsMinute half-open check failed")
	}
}

func TestLocalWindows_MidnightCrossing(t *testing.T) {
	// [18:28, 18:33) UTC = [23:58, 00:03) IST — Friday tail + Saturday head.
	w := window(t, "2026-08-07T18:28:00Z", 5*time.Minute)
	segs := LocalWindows(w, []string{kolkata})[kolkata]
	if len(segs) != 2 {
		t.Fatalf("segments = %d, want 2", len(segs))
	}
	if segs[0].FromMin != 23*60+58 || segs[0].ToMin != minutesPerDay || segs[0].Weekday != time.Friday {
		t.Errorf("tail segment = %+v", segs[0])
	}
	if segs[1].FromMin != 0 || segs[1].ToMin != 3 || segs[1].Weekday != time.Saturday {
		t.Errorf("head segment = %+v", segs[1])
	}
}

func TestLocalWindows_SpringForwardCoversSkippedMinutes(t *testing.T) {
	// US spring-forward 2026-03-08: 02:00 EST → 03:00 EDT at 07:00 UTC.
	// [06:57, 07:02) UTC reads locally as [01:57 EST, 03:02 EDT) — the segment
	// must CONTAIN the nonexistent 02:xx minutes so those users fire (an hour
	// early on the clock, never lost).
	w := window(t, "2026-03-08T06:57:00Z", 5*time.Minute)
	segs := LocalWindows(w, []string{"America/New_York"})["America/New_York"]
	if len(segs) != 1 {
		t.Fatalf("segments = %d, want 1", len(segs))
	}
	s := segs[0]
	if s.FromMin != 117 || s.ToMin != 182 {
		t.Errorf("segment = [%d, %d), want [117, 182)", s.FromMin, s.ToMin)
	}
	if !s.ContainsMinute(120) || !s.ContainsMinute(150) {
		t.Error("skipped 02:xx minutes must be inside the jump segment")
	}
}

func TestLocalWindows_FallBackClampsWraparound(t *testing.T) {
	// US fall-back 2026-11-01: 02:00 EDT → 01:00 EST at 06:00 UTC.
	// [05:57, 06:02) UTC reads locally as [01:57 EDT, 01:02 EST) — the end
	// LOOKS earlier than the start. Without the clamp this would fan a
	// 5-minute window out over nearly the whole day.
	w := window(t, "2026-11-01T05:57:00Z", 5*time.Minute)
	segs := LocalWindows(w, []string{"America/New_York"})["America/New_York"]
	if len(segs) != 1 {
		t.Fatalf("segments = %d, want 1", len(segs))
	}
	s := segs[0]
	if s.FromMin != 117 || s.ToMin != 122 {
		t.Errorf("segment = [%d, %d), want clamped [117, 122)", s.FromMin, s.ToMin)
	}
}

func TestLocalWindows_SkipsBadZone(t *testing.T) {
	w := window(t, "2026-08-07T05:00:00Z", 5*time.Minute)
	out := LocalWindows(w, []string{"Not/AZone", kolkata})
	if _, ok := out["Not/AZone"]; ok {
		t.Error("bad zone must be skipped, not present")
	}
	if len(out[kolkata]) != 1 {
		t.Error("good zone must survive a bad sibling")
	}
}
