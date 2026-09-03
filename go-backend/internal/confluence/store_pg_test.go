package confluence

import (
	"testing"
	"time"
)

func TestConfluenceSourceRowCarriesSchedule(t *testing.T) {
	next := time.Date(2026, 9, 4, 2, 15, 0, 0, time.UTC)
	got := toConfluenceSourceRow(confluenceSourceRow{ID: "s1", SyncSchedule: "weekly", NextSyncAt: &next})
	if got.SyncSchedule != "weekly" {
		t.Fatalf("expected weekly, got %q", got.SyncSchedule)
	}
	if got.NextSyncAt == nil || !got.NextSyncAt.Equal(next) {
		t.Fatalf("next_sync_at not carried through: %v", got.NextSyncAt)
	}
}

func TestConfluenceStoreImplementsSweeperContract(t *testing.T) {
	if k := NewStore(nil).Kind(); k != "confluence" {
		t.Fatalf("expected kind confluence, got %q", k)
	}
}
