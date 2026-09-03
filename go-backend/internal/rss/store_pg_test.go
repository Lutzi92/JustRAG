package rss

import (
	"testing"
	"time"
)

func TestRSSFeedRowCarriesSchedule(t *testing.T) {
	next := time.Date(2026, 9, 4, 1, 30, 0, 0, time.UTC)
	db := rssFeedRow{ID: "f1", SyncSchedule: "daily", NextSyncAt: &next}
	got := toRSSFeedRow(db)
	if got.SyncSchedule != "daily" {
		t.Fatalf("expected daily, got %q", got.SyncSchedule)
	}
	if got.NextSyncAt == nil || !got.NextSyncAt.Equal(next) {
		t.Fatalf("next_sync_at not carried through: %v", got.NextSyncAt)
	}
}

func TestRSSStoreImplementsSweeperContract(t *testing.T) {
	s := NewStore(nil)
	if s.Kind() != "rss" {
		t.Fatalf("expected kind rss, got %q", s.Kind())
	}
}
