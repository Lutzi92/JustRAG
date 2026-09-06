package adminkboverview

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func floatptr(f float64) *float64 { return &f }

// TestOverview_MergesRagasStats pins the merge: a KB with a sample block in
// the store's map carries it on the row; a KB absent from the map gets a nil
// Ragas — not a zeroed struct, which would read as "0 samples" rather than
// "no data yet" (the FE's dash rendering depends on this distinction).
//
// Mutation: change `if rs, ok := ...; ok { row.Ragas = &rs }` to always
// assign `row.Ragas = &RagasStats{}` on the miss branch → kb-2 fails.
func TestOverview_MergesRagasStats(t *testing.T) {
	store := &fakeStore{
		kbs: []KBBase{
			{ID: "kb-1", Name: "Alpha", CreatedAt: "2026-01-01T00:00:00Z"},
			{ID: "kb-2", Name: "Beta", CreatedAt: "2026-01-01T00:00:00Z"},
		},
		ragasStats: map[string]RagasStats{
			"kb-1": {
				N24h:             5,
				Faithfulness:     floatptr(0.61),
				AnswerRelevance:  floatptr(0.98),
				ContextPrecision: floatptr(0.47),
			},
		},
	}
	svc := NewService(store, nil)
	resp, err := svc.Overview(context.Background())
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}

	r1 := resp.Rows[0]
	if r1.Ragas == nil {
		t.Fatal("kb-1 should carry ragas stats")
	}
	if r1.Ragas.N24h != 5 {
		t.Errorf("n24h = %d, want 5", r1.Ragas.N24h)
	}
	if r1.Ragas.Faithfulness == nil || *r1.Ragas.Faithfulness != 0.61 {
		t.Errorf("faithfulness = %v, want 0.61", r1.Ragas.Faithfulness)
	}
	if r1.Ragas.AnswerRelevance == nil || *r1.Ragas.AnswerRelevance != 0.98 {
		t.Errorf("answerRelevance = %v, want 0.98", r1.Ragas.AnswerRelevance)
	}
	if r1.Ragas.ContextPrecision == nil || *r1.Ragas.ContextPrecision != 0.47 {
		t.Errorf("contextPrecision = %v, want 0.47", r1.Ragas.ContextPrecision)
	}

	r2 := resp.Rows[1]
	if r2.Ragas != nil {
		t.Errorf("kb-2 should have no ragas stats, got %+v", r2.Ragas)
	}
}

// TestOverview_RagasStatsWindowIsTrailing24Hours pins the fixed 24h window
// Overview passes to RagasStatsByKB — the admin column is meant to answer
// "how is this KB doing right now", not a tunable lookback.
//
// Mutation: change ragasWindow to any other duration, or pass time.Time{}
// (no window) → the `since` the store receives drifts far enough from
// "now - 24h" that this fails.
func TestOverview_RagasStatsWindowIsTrailing24Hours(t *testing.T) {
	store := &fakeStore{kbs: []KBBase{{ID: "kb-1", Name: "Alpha", CreatedAt: "2026-01-01T00:00:00Z"}}}
	svc := NewService(store, nil)
	before := time.Now().Add(-24 * time.Hour)
	if _, err := svc.Overview(context.Background()); err != nil {
		t.Fatalf("Overview: %v", err)
	}
	after := time.Now().Add(-24 * time.Hour)

	if store.gotSince.Before(before.Add(-time.Second)) || store.gotSince.After(after.Add(time.Second)) {
		t.Errorf("since = %v, want within a second of now-24h (between %v and %v)", store.gotSince, before, after)
	}
}

// Mutation: drop the RagasStatsByKB error check in Overview → this fails.
func TestOverview_RagasStatsErrorPropagates(t *testing.T) {
	svc := NewService(&fakeStore{ragasErr: errors.New("ragas stats db down")}, nil)
	if _, err := svc.Overview(context.Background()); err == nil {
		t.Fatal("expected error when RagasStatsByKB fails")
	}
}

// The JSON contract the FE (KBOverviewDashboard.tsx) consumes: n24h always
// present, the three score fields omitted (not null) when nil, and the whole
// `ragas` key omitted for a KB with no samples in the window.
func TestOverviewHandler_RagasJSONShape(t *testing.T) {
	store := &fakeStore{
		kbs: []KBBase{
			{ID: "kb-1", Name: "Alpha", CreatedAt: "2026-01-01T00:00:00Z"},
			{ID: "kb-2", Name: "Beta", CreatedAt: "2026-01-01T00:00:00Z"},
		},
		ragasStats: map[string]RagasStats{
			"kb-1": {N24h: 3, Faithfulness: floatptr(0.9)},
		},
	}
	h := NewHandler(NewService(store, nil))
	req := httptest.NewRequest(http.MethodGet, "/api/admin/kb-overview", nil)
	w := httptest.NewRecorder()
	h.Overview(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}

	var payload struct {
		Rows []map[string]json.RawMessage `json:"rows"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(payload.Rows))
	}

	raw, ok := payload.Rows[0]["ragas"]
	if !ok {
		t.Fatal("kb-1 row must carry a ragas key")
	}
	var ragas struct {
		N24h             int      `json:"n24h"`
		Faithfulness     *float64 `json:"faithfulness"`
		AnswerRelevance  *float64 `json:"answerRelevance"`
		ContextPrecision *float64 `json:"contextPrecision"`
	}
	if err := json.Unmarshal(raw, &ragas); err != nil {
		t.Fatalf("decode ragas block: %v", err)
	}
	if ragas.N24h != 3 {
		t.Errorf("n24h = %d, want 3", ragas.N24h)
	}
	if ragas.Faithfulness == nil || *ragas.Faithfulness != 0.9 {
		t.Errorf("faithfulness = %v, want 0.9", ragas.Faithfulness)
	}
	if ragas.AnswerRelevance != nil || ragas.ContextPrecision != nil {
		t.Errorf("unset scores must round-trip as omitted/null, got AR=%v CP=%v", ragas.AnswerRelevance, ragas.ContextPrecision)
	}

	if _, ok := payload.Rows[1]["ragas"]; ok {
		t.Error("kb-2 (no samples in window) must omit the ragas key entirely")
	}
}
