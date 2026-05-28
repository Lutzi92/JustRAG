package vector

import (
	"testing"
	"time"
)

func TestStageTimer_RecordsDeltaSinceLastMark(t *testing.T) {
	t.Parallel()
	var observed []struct {
		stage    string
		duration time.Duration
	}
	record := func(stage string, d time.Duration) {
		observed = append(observed, struct {
			stage    string
			duration time.Duration
		}{stage, d})
	}

	timer := newStageTimer(record)
	time.Sleep(5 * time.Millisecond)
	timer.Mark("a")
	time.Sleep(5 * time.Millisecond)
	timer.Mark("b")

	if len(observed) != 2 {
		t.Fatalf("expected 2 observations, got %d", len(observed))
	}
	if observed[0].stage != "a" || observed[1].stage != "b" {
		t.Errorf("stage labels: got %v %v, want a b", observed[0].stage, observed[1].stage)
	}
	if observed[0].duration < 4*time.Millisecond {
		t.Errorf("stage a duration too small: %v", observed[0].duration)
	}
	if observed[1].duration < 4*time.Millisecond {
		t.Errorf("stage b duration too small: %v", observed[1].duration)
	}
}

func TestStageTimer_NilRecorderIsSafe(t *testing.T) {
	t.Parallel()
	timer := newStageTimer(nil)
	timer.Mark("a") // must not panic
}
