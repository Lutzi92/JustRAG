package syncwindow

import (
	"testing"
	"time"
)

func berlin(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	return loc
}

func nightWindow(t *testing.T) Window {
	t.Helper()
	return Window{StartHour: 1, EndHour: 5, Loc: berlin(t)}
}

func TestNextSlot_ManualHasNoSlot(t *testing.T) {
	if _, ok := NextSlot("src-1", ScheduleManual, time.Now(), nightWindow(t)); ok {
		t.Fatal("manual schedule must not produce a slot")
	}
	if _, ok := NextSlot("src-1", "hourly", time.Now(), nightWindow(t)); ok {
		t.Fatal("unknown schedule must not produce a slot")
	}
}

func TestNextSlot_IsDeterministic(t *testing.T) {
	w := nightWindow(t)
	after := time.Date(2026, 9, 3, 14, 0, 0, 0, w.Loc)
	a, ok := NextSlot("src-1", ScheduleDaily, after, w)
	if !ok {
		t.Fatal("expected a slot")
	}
	b, _ := NextSlot("src-1", ScheduleDaily, after, w)
	if !a.Equal(b) {
		t.Fatalf("not deterministic: %v vs %v", a, b)
	}
}

func TestNextSlot_DailyLandsInsideWindow(t *testing.T) {
	w := nightWindow(t)
	after := time.Date(2026, 9, 3, 14, 0, 0, 0, w.Loc)
	for i := 0; i < 200; i++ {
		id := "source-" + time.Duration(i).String()
		got, ok := NextSlot(id, ScheduleDaily, after, w)
		if !ok {
			t.Fatalf("%s: expected a slot", id)
		}
		got = got.In(w.Loc)
		mins := got.Hour()*60 + got.Minute()
		if mins < 1*60 || mins >= 5*60 {
			t.Fatalf("%s: slot %v outside 01:00-05:00", id, got)
		}
		if !got.After(after) {
			t.Fatalf("%s: slot %v not after %v", id, got, after)
		}
	}
}

func TestNextSlot_DailyRepeatsEvery24h(t *testing.T) {
	w := nightWindow(t)
	after := time.Date(2026, 9, 3, 14, 0, 0, 0, w.Loc)
	first, _ := NextSlot("src-1", ScheduleDaily, after, w)
	second, _ := NextSlot("src-1", ScheduleDaily, first, w)
	if d := second.Sub(first); d != 24*time.Hour {
		t.Fatalf("expected 24h between consecutive daily slots, got %v", d)
	}
}

func TestNextSlot_WindowCrossingMidnight(t *testing.T) {
	w := Window{StartHour: 22, EndHour: 5, Loc: berlin(t)}
	after := time.Date(2026, 9, 3, 14, 0, 0, 0, w.Loc)
	for i := 0; i < 200; i++ {
		id := "x-" + time.Duration(i).String()
		got, ok := NextSlot(id, ScheduleDaily, after, w)
		if !ok {
			t.Fatalf("%s: expected a slot", id)
		}
		got = got.In(w.Loc)
		h := got.Hour()
		if !(h >= 22 || h < 5) {
			t.Fatalf("%s: slot %v outside 22:00-05:00", id, got)
		}
	}
}

// A source whose slot is later tonight must not be pushed to tomorrow just
// because `after` has already crossed midnight.
func TestNextSlot_CrossMidnightDoesNotSkipTonight(t *testing.T) {
	w := Window{StartHour: 22, EndHour: 5, Loc: berlin(t)}
	// Find an id whose offset puts it after 02:00.
	var id string
	base := time.Date(2026, 9, 3, 12, 0, 0, 0, w.Loc)
	for i := 0; i < 500; i++ {
		cand := "late-" + time.Duration(i).String()
		slot, _ := NextSlot(cand, ScheduleDaily, base, w)
		if slot.In(w.Loc).Hour() >= 3 && slot.In(w.Loc).Hour() < 5 {
			id = cand
			break
		}
	}
	if id == "" {
		t.Fatal("no source hashed into the post-03:00 part of the window")
	}
	slot, _ := NextSlot(id, ScheduleDaily, base, w)
	justBefore := slot.Add(-30 * time.Minute)
	got, _ := NextSlot(id, ScheduleDaily, justBefore, w)
	if !got.Equal(slot) {
		t.Fatalf("slot moved to %v; expected tonight's %v", got, slot)
	}
}

func TestNextSlot_WeeklyKeepsWeekdayAndRepeatsEvery7Days(t *testing.T) {
	w := nightWindow(t)
	after := time.Date(2026, 9, 3, 14, 0, 0, 0, w.Loc)
	first, ok := NextSlot("src-1", ScheduleWeekly, after, w)
	if !ok {
		t.Fatal("expected a slot")
	}
	second, _ := NextSlot("src-1", ScheduleWeekly, first, w)
	if d := second.Sub(first); d != 7*24*time.Hour {
		t.Fatalf("expected 7 days between weekly slots, got %v", d)
	}
	if first.In(w.Loc).Weekday() != second.In(w.Loc).Weekday() {
		t.Fatal("weekly slots must fall on the same weekday")
	}
}

func TestNextSlot_WeeklySpreadsAcrossWeekdays(t *testing.T) {
	w := nightWindow(t)
	after := time.Date(2026, 9, 3, 14, 0, 0, 0, w.Loc)
	seen := map[time.Weekday]bool{}
	for i := 0; i < 200; i++ {
		got, _ := NextSlot("weekly-"+time.Duration(i).String(), ScheduleWeekly, after, w)
		seen[got.In(w.Loc).Weekday()] = true
	}
	if len(seen) < 5 {
		t.Fatalf("weekly slots clustered on %d weekdays, expected >= 5", len(seen))
	}
}

func TestNextSlot_DegenerateWindowIsOneHour(t *testing.T) {
	w := Window{StartHour: 3, EndHour: 3, Loc: berlin(t)}
	after := time.Date(2026, 9, 3, 14, 0, 0, 0, w.Loc)
	for i := 0; i < 100; i++ {
		got, ok := NextSlot("deg-"+time.Duration(i).String(), ScheduleDaily, after, w)
		if !ok {
			t.Fatal("expected a slot")
		}
		if h := got.In(w.Loc).Hour(); h != 3 {
			t.Fatalf("slot %v outside the 03:00-04:00 fallback window", got)
		}
	}
}

func TestNextSlot_SurvivesDSTTransitions(t *testing.T) {
	w := nightWindow(t)
	// Spring forward: 2026-03-29, 02:00 -> 03:00 CET->CEST.
	spring := time.Date(2026, 3, 28, 12, 0, 0, 0, w.Loc)
	// Fall back: 2026-10-25, 03:00 -> 02:00 CEST->CET.
	fall := time.Date(2026, 10, 24, 12, 0, 0, 0, w.Loc)
	for _, after := range []time.Time{spring, fall} {
		for i := 0; i < 100; i++ {
			got, ok := NextSlot("dst-"+time.Duration(i).String(), ScheduleDaily, after, w)
			if !ok {
				t.Fatalf("expected a slot after %v", after)
			}
			if !got.After(after) {
				t.Fatalf("slot %v not after %v", got, after)
			}
			if got.Sub(after) > 48*time.Hour {
				t.Fatalf("slot %v is more than 48h after %v", got, after)
			}
		}
	}
}

func TestNextSlot_NilLocationFallsBackToUTC(t *testing.T) {
	w := Window{StartHour: 1, EndHour: 5}
	after := time.Date(2026, 9, 3, 14, 0, 0, 0, time.UTC)
	got, ok := NextSlot("src-1", ScheduleDaily, after, w)
	if !ok {
		t.Fatal("expected a slot")
	}
	if h := got.UTC().Hour(); h < 1 || h >= 5 {
		t.Fatalf("slot %v outside 01:00-05:00 UTC", got)
	}
}

func TestValid(t *testing.T) {
	for _, s := range []string{ScheduleManual, ScheduleDaily, ScheduleWeekly} {
		if !Valid(s) {
			t.Fatalf("%q should be valid", s)
		}
	}
	for _, s := range []string{"", "hourly", "Daily", "60"} {
		if Valid(s) {
			t.Fatalf("%q should be invalid", s)
		}
	}
}
