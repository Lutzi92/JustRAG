package adminkboverview

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeSiteConfig is a per-key site_config reader returning canned values.
type fakeSiteConfig struct {
	vals map[string]string
	err  error
}

func (f *fakeSiteConfig) GetSiteConfigValue(_ context.Context, key string) (*string, error) {
	if f.err != nil {
		return nil, f.err
	}
	v, ok := f.vals[key]
	if !ok {
		return nil, nil
	}
	return &v, nil
}

func TestResolveStaleDays(t *testing.T) {
	cases := []struct {
		name string
		cfg  SiteConfigReader
		want int
	}{
		{"nil reader falls back to the default", nil, defaultStaleDays},
		{"unset key falls back to the default", &fakeSiteConfig{vals: map[string]string{}}, defaultStaleDays},
		{"empty value falls back", &fakeSiteConfig{vals: map[string]string{"kb_stale_days": ""}}, defaultStaleDays},
		{"garbage falls back", &fakeSiteConfig{vals: map[string]string{"kb_stale_days": "soon"}}, defaultStaleDays},
		{"read error falls back", &fakeSiteConfig{err: errors.New("db down")}, defaultStaleDays},
		{"configured value is used", &fakeSiteConfig{vals: map[string]string{"kb_stale_days": "30"}}, 30},
		// Mutation: drop either clamp arm → these two fail. A zero or
		// negative threshold would mark every file stale (NOW() - 0 days);
		// an absurd one would silently mean "nothing is ever stale".
		{"zero clamps up to the minimum", &fakeSiteConfig{vals: map[string]string{"kb_stale_days": "0"}}, minStaleDays},
		{"negative clamps up to the minimum", &fakeSiteConfig{vals: map[string]string{"kb_stale_days": "-5"}}, minStaleDays},
		{"too large clamps down to the maximum", &fakeSiteConfig{vals: map[string]string{"kb_stale_days": "99999"}}, maxStaleDays},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveStaleDays(context.Background(), c.cfg); got != c.want {
				t.Errorf("resolveStaleDays = %d, want %d", got, c.want)
			}
		})
	}
}

func TestOverview_MergesFreshnessAndSyncStats(t *testing.T) {
	store := &fakeStore{
		kbs: []KBBase{
			{ID: "kb-1", Name: "Alpha", CreatedAt: "2026-01-01T00:00:00Z"},
			{ID: "kb-2", Name: "Beta", CreatedAt: "2026-01-01T00:00:00Z"},
		},
		fileMap: map[string]FileStats{
			"kb-1": {
				FileCount:      10,
				StaleFileCount: 4,
				OldestFileAt:   strptr("2019-03-01T00:00:00Z"),
			},
		},
		syncStats: map[string]SyncStats{
			"kb-1": {
				LastSuccessAt: strptr("2026-09-05T01:30:00Z"),
				LastAttemptAt: strptr("2026-09-06T01:30:00Z"),
				Failing:       true,
				Kinds:         []string{"rss", "confluence"},
			},
		},
	}
	svc := NewService(store, nil)
	svc.SetSiteConfig(&fakeSiteConfig{vals: map[string]string{"kb_stale_days": "90"}})

	resp, err := svc.Overview(context.Background())
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}
	if store.gotStaleDays != 90 {
		t.Errorf("staleDays passed to the store = %d, want 90", store.gotStaleDays)
	}
	if resp.StaleDays != 90 {
		t.Errorf("response staleDays = %d, want 90", resp.StaleDays)
	}

	r1 := resp.Rows[0]
	if r1.OldestFileAt == nil || *r1.OldestFileAt != "2019-03-01T00:00:00Z" {
		t.Errorf("oldestFileAt = %v", r1.OldestFileAt)
	}
	if r1.StaleFileCount != 4 {
		t.Errorf("staleFileCount = %d, want 4", r1.StaleFileCount)
	}
	if r1.StaleShare != 0.4 {
		t.Errorf("staleShare = %v, want 0.4", r1.StaleShare)
	}
	// A source that has succeeded reports the SUCCESS timestamp, not the
	// (newer) attempt one. Mutation: prefer LastAttemptAt → this fails,
	// which is exactly the regression migration 0071 exists to prevent.
	if r1.LastSyncAt == nil || *r1.LastSyncAt != "2026-09-05T01:30:00Z" {
		t.Errorf("lastSyncAt = %v, want the last SUCCESS timestamp", r1.LastSyncAt)
	}
	if !r1.SyncFailing {
		t.Error("syncFailing should be true when a source has consecutive failures")
	}
	if len(r1.SyncKinds) != 2 {
		t.Errorf("syncKinds = %v, want two kinds", r1.SyncKinds)
	}

	// kb-2 has neither stats entry.
	r2 := resp.Rows[1]
	if r2.OldestFileAt != nil || r2.StaleFileCount != 0 || r2.StaleShare != 0 ||
		r2.LastSyncAt != nil || r2.SyncFailing || len(r2.SyncKinds) != 0 {
		t.Errorf("kb without stats must stay zeroed, got %+v", r2)
	}
}

// Mutation: drop the `LastSuccessAt == nil` fallback → this fails. A source
// that has never succeeded since the deploy still has a last ATTEMPT, and
// showing nothing at all would read as "never synced" (W3-R10).
func TestOverview_LastSyncFallsBackToTheAttemptTimestamp(t *testing.T) {
	store := &fakeStore{
		kbs: []KBBase{{ID: "kb-1", Name: "Alpha", CreatedAt: "2026-01-01T00:00:00Z"}},
		syncStats: map[string]SyncStats{
			"kb-1": {LastAttemptAt: strptr("2026-09-06T01:30:00Z"), Failing: true},
		},
	}
	svc := NewService(store, nil)
	resp, err := svc.Overview(context.Background())
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}
	row := resp.Rows[0]
	if row.LastSyncAt == nil || *row.LastSyncAt != "2026-09-06T01:30:00Z" {
		t.Errorf("lastSyncAt = %v, want the attempt timestamp", row.LastSyncAt)
	}
	// …but the row must say the timestamp is not a verified success, or the
	// fallback would read exactly like a healthy sync.
	if row.SyncSucceeded {
		t.Error("syncSucceeded must be false when last_success_at is NULL")
	}
}

// Mutation: drop the FileCount > 0 guard in the share computation → the
// division yields NaN and this fails (NaN also breaks encoding/json).
func TestOverview_StaleShareIsZeroWithoutFiles(t *testing.T) {
	store := &fakeStore{
		kbs:     []KBBase{{ID: "kb-1", Name: "Alpha", CreatedAt: "2026-01-01T00:00:00Z"}},
		fileMap: map[string]FileStats{"kb-1": {FileCount: 0, StaleFileCount: 0}},
	}
	svc := NewService(store, nil)
	resp, err := svc.Overview(context.Background())
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}
	if resp.Rows[0].StaleShare != 0 {
		t.Errorf("staleShare = %v, want 0 for an empty KB", resp.Rows[0].StaleShare)
	}
	if _, err := json.Marshal(resp); err != nil {
		t.Fatalf("response must stay JSON-encodable: %v", err)
	}
}

func TestOverview_SyncStatsErrorPropagates(t *testing.T) {
	svc := NewService(&fakeStore{syncErr: errors.New("sync stats db down")}, &fakeInspector{})
	if _, err := svc.Overview(context.Background()); err == nil {
		t.Fatal("expected error when SyncStatsByKB fails")
	}
}

// The JSON contract Task 6 (frontend) consumes.
func TestOverviewHandler_FreshnessJSONShape(t *testing.T) {
	store := &fakeStore{
		kbs:     []KBBase{{ID: "kb-1", Name: "Alpha", CreatedAt: "2026-01-01T00:00:00Z"}},
		fileMap: map[string]FileStats{"kb-1": {FileCount: 4, StaleFileCount: 1, OldestFileAt: strptr("2020-01-01T00:00:00Z")}},
		syncStats: map[string]SyncStats{
			"kb-1": {LastSuccessAt: strptr("2026-09-05T01:00:00Z"), Kinds: []string{"rss"}},
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
		StaleDays int `json:"staleDays"`
		Rows      []struct {
			OldestFileAt   *string  `json:"oldestFileAt"`
			StaleFileCount int      `json:"staleFileCount"`
			StaleShare     float64  `json:"staleShare"`
			LastSyncAt     *string  `json:"lastSyncAt"`
			SyncFailing    bool     `json:"syncFailing"`
			SyncSucceeded  bool     `json:"syncSucceeded"`
			SyncKinds      []string `json:"syncKinds"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.StaleDays != defaultStaleDays {
		t.Errorf("staleDays = %d, want the %d default", payload.StaleDays, defaultStaleDays)
	}
	row := payload.Rows[0]
	if row.OldestFileAt == nil || *row.OldestFileAt != "2020-01-01T00:00:00Z" {
		t.Errorf("oldestFileAt missing: %+v", row)
	}
	if row.StaleFileCount != 1 || row.StaleShare != 0.25 {
		t.Errorf("stale fields wrong: %+v", row)
	}
	if row.LastSyncAt == nil || !row.SyncSucceeded || row.SyncFailing {
		t.Errorf("sync fields wrong: %+v", row)
	}
	if len(row.SyncKinds) != 1 || row.SyncKinds[0] != "rss" {
		t.Errorf("syncKinds wrong: %+v", row.SyncKinds)
	}
}
