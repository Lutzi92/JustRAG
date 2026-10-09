package worker

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type fakeExpirer struct{ calls atomic.Int32 }

func (f *fakeExpirer) ExpireStale(context.Context) (int64, error) {
	f.calls.Add(1)
	return 2, nil
}

func TestAgentRunsExpire_RunsOnceAfterStartupDelay(t *testing.T) {
	fe := &fakeExpirer{}
	ctx, cancel := context.WithCancel(context.Background())
	stop := StartMaintenance(ctx, MaintenanceConfig{
		AgentRunExpirer:        fe,
		AgentRunExpireInterval: time.Hour,
		agentRunExpireDelay:    20 * time.Millisecond,
	})
	deadline := time.Now().Add(2 * time.Second)
	for fe.calls.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(60 * time.Millisecond) // a second pass would need the 1h ticker
	cancel()
	stop()
	if got := fe.calls.Load(); got != 1 {
		t.Fatalf("ExpireStale calls = %d, want exactly 1 (startup pass)", got)
	}
}

func TestAgentRunsExpire_NilExpirerLaunchesNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stop := StartMaintenance(ctx, MaintenanceConfig{agentRunExpireDelay: time.Millisecond})
	time.Sleep(30 * time.Millisecond)
	cancel()
	stop() // must not hang or panic
}
