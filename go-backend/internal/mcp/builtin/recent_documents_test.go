package builtin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/mcp"
)

type fakeRecentStore struct {
	rows                []RecentDocRow
	gotAfter, gotBefore time.Time
	gotLimit            int
}

func (f *fakeRecentStore) RecentDocuments(_ context.Context, _ string, after, before time.Time, limit int) ([]RecentDocRow, error) {
	f.gotAfter, f.gotBefore, f.gotLimit = after, before, limit
	return f.rows, nil
}

func enabledTrue(context.Context) bool { return true }

func invoke(t *testing.T, tool mcp.Tool, args map[string]any) (mcp.ToolResult, error) {
	t.Helper()
	raw, _ := json.Marshal(args)
	return tool.Handler.Invoke(context.Background(), raw)
}

func TestRecentDocumentsDisabled(t *testing.T) {
	tool := NewRecentDocuments(&fakeRecentStore{}, func(context.Context) bool { return false }, nil)
	_, err := invoke(t, tool, map[string]any{"kb_id": "kb1", "date_from": "2026-07-01"})
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("want disabled error, got %v", err)
	}
}

func TestRecentDocumentsLists(t *testing.T) {
	store := &fakeRecentStore{rows: []RecentDocRow{
		{Name: "Advisory A", Origin: "rss", CreatedAt: time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)},
	}}
	tool := NewRecentDocuments(store, func(context.Context) bool { return true }, nil)
	res, err := invoke(t, tool, map[string]any{"kb_id": "kb1", "date_from": "2026-07-01"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res.Text, "Advisory A") || !strings.Contains(res.Text, "2026-07-01") {
		t.Errorf("result missing doc: %q", res.Text)
	}
}

func TestRecentDocumentsEmpty(t *testing.T) {
	tool := NewRecentDocuments(&fakeRecentStore{}, func(context.Context) bool { return true }, nil)
	res, err := invoke(t, tool, map[string]any{"kb_id": "kb1", "date_from": "2026-07-01"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(strings.ToLower(res.Text), "no documents") {
		t.Errorf("expected empty-window message, got %q", res.Text)
	}
}

func TestRecentDocumentsBadDate(t *testing.T) {
	tool := NewRecentDocuments(&fakeRecentStore{}, func(context.Context) bool { return true }, nil)
	_, err := invoke(t, tool, map[string]any{"kb_id": "kb1", "date_from": "July 1st"})
	if err == nil {
		t.Fatal("expected parse error for bad date")
	}
}

func TestRecentDocumentsRequiresKbID(t *testing.T) {
	tool := NewRecentDocuments(&fakeRecentStore{}, func(context.Context) bool { return true }, nil)
	_, err := invoke(t, tool, map[string]any{"date_from": "2026-07-01"})
	if err == nil {
		t.Fatal("expected error when kb_id missing")
	}
}

func TestRecentDocumentsMaxResults(t *testing.T) {
	// The configured cap (chat_date_tools_max_results) reaches the store.
	store := &fakeRecentStore{}
	tool := NewRecentDocuments(store, enabledTrue, func(context.Context) int { return 7 })
	if _, err := invoke(t, tool, map[string]any{"kb_id": "kb1", "date_from": "2026-07-01"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store.gotLimit != 7 {
		t.Errorf("limit passed to store = %d, want 7", store.gotLimit)
	}

	// nil maxResults falls back to the default cap.
	store2 := &fakeRecentStore{}
	tool2 := NewRecentDocuments(store2, enabledTrue, nil)
	if _, err := invoke(t, tool2, map[string]any{"kb_id": "kb1", "date_from": "2026-07-01"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store2.gotLimit != recentDocumentsMaxDefault {
		t.Errorf("default limit = %d, want %d", store2.gotLimit, recentDocumentsMaxDefault)
	}
}
