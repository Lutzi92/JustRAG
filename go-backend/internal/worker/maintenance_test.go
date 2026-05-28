package worker

import (
	"context"
	"testing"
	"time"
)

func TestStartMaintenance_CancelStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	// StartMaintenance with nil DB pools — the goroutines will error on first
	// tick but should not panic and should stop when cancel is called.
	stopMaintenance := StartMaintenance(ctx, MaintenanceConfig{
		MainDB:                nil,
		VectorDB:              nil,
		StuckCheckInterval:    100 * time.Millisecond,
		OrphanCleanupInterval: 100 * time.Millisecond,
	})

	// Let it run briefly.
	time.Sleep(50 * time.Millisecond)

	// Cancel parent context — goroutines should exit.
	cancel()
	stopMaintenance()

	// If we get here without hanging, the lifecycle is correct.
}
