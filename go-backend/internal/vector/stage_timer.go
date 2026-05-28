package vector

import "time"

// stageTimer records the wall-clock delta between successive Mark calls.
// Construct with newStageTimer; the first Mark measures from construction
// time. Zero-value is unusable — always go through newStageTimer so the
// recorder is set explicitly. The timer is not safe for concurrent use;
// each search call gets its own.
type stageTimer struct {
	prev   time.Time
	record func(stage string, d time.Duration)
}

// newStageTimer constructs a timer whose first Mark measures from now.
// A nil record callback turns Mark into a no-op so call sites don't need
// to special-case the disabled-observability path.
func newStageTimer(record func(stage string, d time.Duration)) *stageTimer {
	return &stageTimer{prev: time.Now(), record: record}
}

// Mark records the delta since the previous Mark (or construction) under
// the given stage label, then resets the internal clock.
func (t *stageTimer) Mark(stage string) {
	now := time.Now()
	if t.record != nil {
		t.record(stage, now.Sub(t.prev))
	}
	t.prev = now
}
