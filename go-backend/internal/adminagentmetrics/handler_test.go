package adminagentmetrics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
)

// fakeStore lets handler tests assert how arguments arrive at the store
// without spinning up Postgres.
type fakeStore struct {
	insertCalls []DecisionRow
	aggregateFn func(kbID *uuid.UUID, since time.Time) (AgentMetricsResponse, error)
}

func (f *fakeStore) Insert(_ context.Context, r DecisionRow) error {
	f.insertCalls = append(f.insertCalls, r)
	return nil
}

func (f *fakeStore) Aggregate(_ context.Context, kbID *uuid.UUID, since time.Time) (AgentMetricsResponse, error) {
	if f.aggregateFn != nil {
		return f.aggregateFn(kbID, since)
	}
	return AgentMetricsResponse{
		AgenticChat: DecisionDistribution{ByLabel: map[string]int{}},
		PlanExecute: DecisionDistribution{ByLabel: map[string]int{}},
		CRAG:        DecisionDistribution{ByLabel: map[string]int{}},
	}, nil
}

func TestParseWindow(t *testing.T) {
	tests := []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"", time.Hour, true},
		{"1h", time.Hour, true},
		{"24h", 24 * time.Hour, true},
		{"1d", 24 * time.Hour, true},
		{"7d", 7 * 24 * time.Hour, true},
		{"1w", 7 * 24 * time.Hour, true},
		{"2h", 0, false},
		{"forever", 0, false},
	}
	for _, tc := range tests {
		got, ok := parseWindow(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("parseWindow(%q) = (%v, %v); want (%v, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestGetMetrics_BadWindow(t *testing.T) {
	h := NewHandler(&fakeStore{})
	req := httptest.NewRequest(http.MethodGet, "/api/admin/agent-metrics?window=2h", nil)
	w := httptest.NewRecorder()
	h.GetMetrics(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestGetMetrics_BadKbID(t *testing.T) {
	h := NewHandler(&fakeStore{})
	req := httptest.NewRequest(http.MethodGet, "/api/admin/agent-metrics?kb_id=not-a-uuid", nil)
	w := httptest.NewRecorder()
	h.GetMetrics(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestGetMetrics_PassesKbAndWindow(t *testing.T) {
	want := uuid.New()
	var sawKbID *uuid.UUID
	var sawSince time.Time
	store := &fakeStore{
		aggregateFn: func(kbID *uuid.UUID, since time.Time) (AgentMetricsResponse, error) {
			sawKbID = kbID
			sawSince = since
			return AgentMetricsResponse{
				AgenticChat: DecisionDistribution{Total: 5, ByLabel: map[string]int{"early_stop_sufficient": 5}},
				PlanExecute: DecisionDistribution{ByLabel: map[string]int{}},
				CRAG:        DecisionDistribution{ByLabel: map[string]int{}},
				MedianHops:  2,
			}, nil
		},
	}
	h := NewHandler(store)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/agent-metrics?window=24h&kb_id="+want.String(), nil)
	w := httptest.NewRecorder()

	before := time.Now()
	h.GetMetrics(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if sawKbID == nil || *sawKbID != want {
		t.Errorf("store kbID = %v, want %v", sawKbID, want)
	}
	expectedSinceMin := before.Add(-25 * time.Hour)
	expectedSinceMax := before.Add(-23 * time.Hour)
	if sawSince.Before(expectedSinceMin) || sawSince.After(expectedSinceMax) {
		t.Errorf("store since = %v, want approx 24h before %v", sawSince, before)
	}

	var resp AgentMetricsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
	if resp.AgenticChat.Total != 5 {
		t.Errorf("AgenticChat.Total = %d, want 5", resp.AgenticChat.Total)
	}
	if resp.WindowSeconds != int(24*time.Hour/time.Second) {
		t.Errorf("WindowSeconds = %d, want %d", resp.WindowSeconds, int(24*time.Hour/time.Second))
	}
}

func TestPgStore_Record_BadKbIDDropped(t *testing.T) {
	// Record() must never panic on a malformed kb_id even though the chat
	// path is fire-and-forget. Use a nil pool — Record short-circuits on
	// the parse error before reaching any DB call.
	s := &PgStore{pool: nil}
	s.Record(context.Background(), "garbage", "agentic", "max_hops_reached", 3, 0, 1500, nil, nil, nil)
}
