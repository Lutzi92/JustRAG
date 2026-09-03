// Package syncwindow computes when a scheduled source sync should run.
//
// Automatic syncs (RSS feeds, Confluence sources, git repositories) are the
// heaviest compute in the system — parsing, embedding, enrichment, KG
// extraction — so they are confined to a night window and spread across it.
// Each source keeps a stable slot derived from its ID, so restarts and
// re-deploys do not reshuffle the schedule.
//
// Everything here is pure: no DB, no ambient clock. The caller passes the
// reference instant.
package syncwindow

import (
	"hash/fnv"
	"time"
)

// Schedule values. These are also the CHECK-constrained column values of
// rss_feeds.sync_schedule, confluence_sources.sync_schedule and
// git_repo_sources.sync_schedule (migration 0068).
const (
	ScheduleManual = "manual"
	ScheduleDaily  = "daily"
	ScheduleWeekly = "weekly"
)

// Window is the nightly interval automatic syncs are scheduled into.
// EndHour may be smaller than StartHour, meaning the window crosses midnight.
type Window struct {
	StartHour int
	EndHour   int
	Loc       *time.Location
}

// DueSource pairs a source id with the schedule its row carries. The stores
// return these and the sweeper feeds them straight into NextSlot, so the
// sweeper never needs a second query to learn a row's schedule.
type DueSource struct {
	ID       string
	Schedule string
}

// Valid reports whether s is one of the three stored schedule values.
func Valid(s string) bool {
	return s == ScheduleManual || s == ScheduleDaily || s == ScheduleWeekly
}

// lengthMinutes is the window length. A degenerate configuration
// (StartHour == EndHour) yields 60 minutes rather than 0, so a typo in the
// admin panel cannot make every source unschedulable.
func (w Window) lengthMinutes() int {
	if w.StartHour == w.EndHour {
		return 60
	}
	diff := (w.EndHour - w.StartHour) * 60
	if diff < 0 {
		diff += 24 * 60
	}
	return diff
}

func (w Window) location() *time.Location {
	if w.Loc == nil {
		return time.UTC
	}
	return w.Loc
}

func hash64(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}

// NextSlot returns the next instant at which sourceID should sync, strictly
// after `after`. ok is false for ScheduleManual and for any unknown schedule.
//
// The offset inside the window and (for weekly) the weekday both come from a
// hash of sourceID, so sources spread deterministically instead of all firing
// at the window's first minute.
func NextSlot(sourceID, schedule string, after time.Time, w Window) (time.Time, bool) {
	if schedule != ScheduleDaily && schedule != ScheduleWeekly {
		return time.Time{}, false
	}

	loc := w.location()
	offset := time.Duration(int(hash64(sourceID)%uint64(w.lengthMinutes()))) * time.Minute
	weekday := time.Weekday(int(hash64(sourceID+"|weekday") % 7))

	// Start one day early: with a window crossing midnight, tonight's slot is
	// anchored to yesterday's date, and starting at `after`'s own date would
	// skip it.
	local := after.In(loc).AddDate(0, 0, -1)
	for i := 0; i < 9; i++ {
		d := local.AddDate(0, 0, i)
		anchor := time.Date(d.Year(), d.Month(), d.Day(), w.StartHour, 0, 0, 0, loc)
		if schedule == ScheduleWeekly && anchor.Weekday() != weekday {
			continue
		}
		if cand := anchor.Add(offset); cand.After(after) {
			return cand, true
		}
	}
	return time.Time{}, false
}
