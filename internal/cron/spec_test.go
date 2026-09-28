package cron

import (
	"testing"
	"time"
)

const testTick = 5 * time.Minute

// kolkata is UTC+5:30 year-round — fixed offsets keep expectations exact.
const kolkata = "Asia/Kolkata"

func mustUTC(t *testing.T, value string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parse %q: %v", value, err)
	}
	return ts.UTC()
}

func TestAtLocalLastOccurrence_SameDay(t *testing.T) {
	spec := AtLocal("09:00").InZone(kolkata)
	// 2026-08-07 is a Friday; 09:00 IST = 03:30 UTC.
	now := mustUTC(t, "2026-08-07T05:00:00Z")
	occ, err := spec.LastOccurrence(now, testTick, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := mustUTC(t, "2026-08-07T03:30:00Z"); !occ.Equal(want) {
		t.Errorf("occ = %v, want %v", occ, want)
	}
}

func TestAtLocalLastOccurrence_BeforeTodayInstant(t *testing.T) {
	spec := AtLocal("09:00").InZone(kolkata)
	now := mustUTC(t, "2026-08-07T02:00:00Z") // 07:30 IST — before 09:00
	occ, err := spec.LastOccurrence(now, testTick, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := mustUTC(t, "2026-08-06T03:30:00Z"); !occ.Equal(want) {
		t.Errorf("occ = %v, want yesterday %v", occ, want)
	}
}

func TestAtLocalLastOccurrence_WeekdayMask(t *testing.T) {
	// Thursday-only 17:00 IST. 2026-08-08 is a Saturday.
	spec := AtLocal("17:00").InZone(kolkata).OnWeekdays(time.Thursday)
	now := mustUTC(t, "2026-08-08T10:00:00Z")
	occ, err := spec.LastOccurrence(now, testTick, "")
	if err != nil {
		t.Fatal(err)
	}
	// Last Thursday was 2026-08-06; 17:00 IST = 11:30 UTC.
	if want := mustUTC(t, "2026-08-06T11:30:00Z"); !occ.Equal(want) {
		t.Errorf("occ = %v, want %v", occ, want)
	}
}

func TestAtLocalLastOccurrence_DayOfMonth(t *testing.T) {
	spec := AtLocal("07:00").InZone(kolkata).OnDayOfMonth(1)
	// 2026-08-23: the last 1st-of-month 07:00 IST was Aug 1 (01:30 UTC).
	now := mustUTC(t, "2026-08-23T10:00:00Z")
	occ, err := spec.LastOccurrence(now, testTick, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := mustUTC(t, "2026-08-01T01:30:00Z"); !occ.Equal(want) {
		t.Errorf("occ = %v, want %v", occ, want)
	}

	// On the 1st itself, before 07:00 local, the occurrence is the PREVIOUS
	// month's 1st — a >30-day walk-back.
	now = mustUTC(t, "2026-08-01T01:00:00Z") // 06:30 IST
	occ, err = spec.LastOccurrence(now, testTick, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := mustUTC(t, "2026-07-01T01:30:00Z"); !occ.Equal(want) {
		t.Errorf("occ = %v, want previous month %v", occ, want)
	}
}

func TestAtLocalLastOccurrence_DayOfMonthSkipsShortMonths(t *testing.T) {
	spec := AtLocal("07:00").InZone(kolkata).OnDayOfMonth(31)
	// March 2026: last 31st before Mar 15 was Jan 31 (Feb has no 31st).
	now := mustUTC(t, "2026-03-15T10:00:00Z")
	occ, err := spec.LastOccurrence(now, testTick, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := mustUTC(t, "2026-01-31T01:30:00Z"); !occ.Equal(want) {
		t.Errorf("occ = %v, want %v", occ, want)
	}
}

func TestAtLocalUsesDefaultZone(t *testing.T) {
	spec := AtLocal("09:00")
	now := mustUTC(t, "2026-08-07T05:00:00Z")
	occ, err := spec.LastOccurrence(now, testTick, kolkata)
	if err != nil {
		t.Fatal(err)
	}
	if want := mustUTC(t, "2026-08-07T03:30:00Z"); !occ.Equal(want) {
		t.Errorf("occ = %v, want %v", occ, want)
	}
}

func TestEveryLastOccurrence_TruncatesToInterval(t *testing.T) {
	spec := Every(5 * time.Minute)
	now := mustUTC(t, "2026-08-07T05:03:07Z")
	occ, err := spec.LastOccurrence(now, testTick, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := mustUTC(t, "2026-08-07T05:00:00Z"); !occ.Equal(want) {
		t.Errorf("occ = %v, want %v", occ, want)
	}
	prev, err := spec.PrevOccurrence(occ, testTick, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := mustUTC(t, "2026-08-07T04:55:00Z"); !prev.Equal(want) {
		t.Errorf("prev = %v, want %v", prev, want)
	}
}

func TestAtUserLocalIsATickSweep(t *testing.T) {
	spec := AtUserLocal("08:30").OnWeekdays()
	now := mustUTC(t, "2026-08-07T05:03:07Z")
	occ, err := spec.LastOccurrence(now, testTick, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := mustUTC(t, "2026-08-07T05:00:00Z"); !occ.Equal(want) {
		t.Errorf("occ = %v, want tick-aligned %v", occ, want)
	}
	w := spec.OccurrenceWindow(occ, testTick)
	if !w.Start.Equal(occ) || !w.End.Equal(occ.Add(testTick)) {
		t.Errorf("window = [%v, %v), want [occ, occ+tick)", w.Start, w.End)
	}
	if min, _ := spec.MinuteOfDay(); min != 8*60+30 {
		t.Errorf("MinuteOfDay = %d, want 510", min)
	}
}

func TestAtLocalWindowIsEmpty(t *testing.T) {
	spec := AtLocal("09:00").InZone(kolkata)
	occ := mustUTC(t, "2026-08-07T03:30:00Z")
	w := spec.OccurrenceWindow(occ, testTick)
	if !w.Start.Equal(w.End) {
		t.Errorf("AtLocal window should be empty, got [%v, %v)", w.Start, w.End)
	}
}

func TestSpecValidate(t *testing.T) {
	cases := []struct {
		name    string
		spec    Spec
		wantErr bool
	}{
		{"good atlocal", AtLocal("09:00").InZone(kolkata), false},
		{"bad hhmm", AtLocal("25:00").InZone(kolkata), true},
		{"bad zone", AtLocal("09:00").InZone("Mars/Olympus"), true},
		{"good every", Every(5 * time.Minute), false},
		{"sub-tick every", Every(time.Minute), true},
		{"zero every", Every(0), true},
		{"good user local", AtUserLocal("08:30"), false},
		{"bad user local", AtUserLocal("8h30"), true},
	}
	for _, tc := range cases {
		err := tc.spec.Validate(testTick, kolkata)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: Validate() err = %v, wantErr %v", tc.name, err, tc.wantErr)
		}
	}
}

func TestParseWeekdays(t *testing.T) {
	got, err := ParseWeekdays("1-5")
	if err != nil || len(got) != 5 || got[0] != time.Monday || got[4] != time.Friday {
		t.Errorf("1-5 = %v (err %v), want Mon..Fri", got, err)
	}
	got, err = ParseWeekdays("4")
	if err != nil || len(got) != 1 || got[0] != time.Thursday {
		t.Errorf("4 = %v (err %v), want Thursday", got, err)
	}
	got, err = ParseWeekdays("0,6")
	if err != nil || len(got) != 2 || got[0] != time.Sunday || got[1] != time.Saturday {
		t.Errorf("0,6 = %v (err %v), want Sun,Sat", got, err)
	}
	for _, bad := range []string{"7", "5-1", "x", "1-"} {
		if _, err := ParseWeekdays(bad); err == nil {
			t.Errorf("ParseWeekdays(%q) should fail", bad)
		}
	}
	if got, err := ParseWeekdays(""); err != nil || got != nil {
		t.Errorf("empty = %v (err %v), want nil", got, err)
	}
}
