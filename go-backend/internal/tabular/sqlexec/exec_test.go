package sqlexec

import "testing"

// TestResolveOptionsClampsSubMillisecondTimeout is the R39 minor fix:
// Postgres treats "SET LOCAL statement_timeout = 0" as disabled, so a
// caller-supplied timeout under 1ms must round UP to 1ms rather than down
// to 0 — otherwise the shortest possible timeout silently becomes no
// timeout at all.
func TestResolveOptionsClampsSubMillisecondTimeout(t *testing.T) {
	cases := []struct {
		name   string
		in     Options
		wantMS int64
	}{
		{"zero-defaults-to-5s", Options{}, 5000},
		{"negative-defaults-to-5s", Options{Timeout: -1}, 5000},
		{"sub-millisecond-clamped-to-1ms", Options{Timeout: 100}, 1}, // 100ns
		{"exactly-1ms-unchanged", Options{Timeout: 1_000_000}, 1},    // 1ms in ns
		{"large-timeout-unchanged", Options{Timeout: 30_000_000_000}, 30000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := resolveOptions(c.in)
			if ms := got.Timeout.Milliseconds(); ms != c.wantMS {
				t.Errorf("Timeout.Milliseconds() = %d, want %d (resolved=%v)", ms, c.wantMS, got.Timeout)
			}
			if got.Timeout.Milliseconds() == 0 {
				t.Error("resolved timeout must never round down to 0ms (Postgres treats 0 as disabled)")
			}
		})
	}
}

func TestResolveOptionsDefaultsRowAndByteCap(t *testing.T) {
	got := resolveOptions(Options{})
	if got.RowCap != 200 {
		t.Errorf("RowCap = %d, want 200", got.RowCap)
	}
	if got.ByteCap != 64<<10 {
		t.Errorf("ByteCap = %d, want %d", got.ByteCap, 64<<10)
	}
}
