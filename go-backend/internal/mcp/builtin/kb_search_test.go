package builtin

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/vector"
)

type capturingSearch struct{ gotOpts vector.SearchOptions }

func (c *capturingSearch) Search(_ context.Context, _, _ string, _ int, opts vector.SearchOptions) (*vector.SearchResult, error) {
	c.gotOpts = opts
	return &vector.SearchResult{}, nil
}

func TestKbSearchDateParams(t *testing.T) {
	svc := &capturingSearch{}
	tool := NewKbSearch(svc)
	raw, _ := json.Marshal(map[string]any{
		"query":     "log4j",
		"kb_id":     "kb1",
		"date_from": "2026-05-01",
		"date_to":   "2026-05-31",
	})
	if _, err := tool.Handler.Invoke(context.Background(), raw); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if svc.gotOpts.CreatedAfter == nil || !svc.gotOpts.CreatedAfter.Equal(time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("CreatedAfter = %v", svc.gotOpts.CreatedAfter)
	}
	if svc.gotOpts.CreatedBefore == nil {
		t.Fatal("CreatedBefore nil")
	}
	// date_to is inclusive of the whole day.
	if svc.gotOpts.CreatedBefore.Before(time.Date(2026, 5, 31, 23, 0, 0, 0, time.UTC)) {
		t.Errorf("CreatedBefore = %v, want end-of-day 2026-05-31", svc.gotOpts.CreatedBefore)
	}
}

func TestKbSearchNoDateParams(t *testing.T) {
	svc := &capturingSearch{}
	tool := NewKbSearch(svc)
	raw, _ := json.Marshal(map[string]any{"query": "x", "kb_id": "kb1"})
	if _, err := tool.Handler.Invoke(context.Background(), raw); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if svc.gotOpts.CreatedAfter != nil || svc.gotOpts.CreatedBefore != nil {
		t.Error("no date params must leave the window nil")
	}
}
