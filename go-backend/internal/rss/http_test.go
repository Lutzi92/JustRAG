package rss_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/kbaccess"
	"github.com/justrag/go-backend/internal/rss"
)

// ---------------------------------------------------------------------------
// Mock store
// ---------------------------------------------------------------------------

var (
	_ rss.RSSStore      = (*mockStore)(nil)
	_ rss.FeedValidator = (*mockValidator)(nil)
)

type mockStore struct {
	feed                 *rss.RSSFeedRow
	feeds                []rss.RSSFeedRow
	updated              *rss.RSSFeedRow
	createdFetchFullText bool
	updatedFetchFullText *bool
	lastCreatedSchedule  string
	lastUpdate           rss.RSSFeedUpdate
	err                  error
	fileIDs              []string  // returned by ListFileIDsByRSSFeedID
	events               *[]string // shared event-order log; nil = untracked
	deletedFeedID        string
}

func (m *mockStore) CreateRSSFeed(_ context.Context, kbID, url string, title *string, syncSchedule string, fetchFullText bool) (*rss.RSSFeedRow, error) {
	if m.err != nil {
		return nil, m.err
	}
	m.createdFetchFullText = fetchFullText
	m.lastCreatedSchedule = syncSchedule
	return m.feed, nil
}

func (m *mockStore) ListRSSFeeds(_ context.Context, kbID string) ([]rss.RSSFeedRow, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.feeds, nil
}

func (m *mockStore) GetRSSFeedByID(_ context.Context, feedID string) (*rss.RSSFeedRow, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.feed, nil
}

func (m *mockStore) UpdateRSSFeed(_ context.Context, feedID string, updates rss.RSSFeedUpdate) (*rss.RSSFeedRow, error) {
	if m.err != nil {
		return nil, m.err
	}
	m.updatedFetchFullText = updates.FetchFullText
	m.lastUpdate = updates
	return m.updated, nil
}

func (m *mockStore) DeleteRSSFeed(_ context.Context, feedID string) error {
	if m.events != nil {
		*m.events = append(*m.events, "delete:"+feedID)
	}
	m.deletedFeedID = feedID
	return m.err
}

func (m *mockStore) UpdateRSSFeedPollSuccess(_ context.Context, _ string, _ int) error {
	return m.err
}

func (m *mockStore) UpdateRSSFeedPollFailure(_ context.Context, _ string, _ string) error {
	return m.err
}

func (m *mockStore) ListFileNamesByRSSFeedID(_ context.Context, _ string) (map[string]bool, error) {
	return nil, m.err
}

func (m *mockStore) ListFileIDsByRSSFeedID(_ context.Context, _ string) ([]string, error) {
	return m.fileIDs, m.err
}

// fakeTableDropper implements rss.TableDropper, recording each call (and
// its position in a shared event log) so tests can assert both "called once
// per file id" and "before the feed delete".
type fakeTableDropper struct {
	events  *[]string
	dropped []string
}

func (d *fakeTableDropper) DropTablesForFile(_ context.Context, fileID string) error {
	if d.events != nil {
		*d.events = append(*d.events, "drop:"+fileID)
	}
	d.dropped = append(d.dropped, fileID)
	return nil
}

// ---------------------------------------------------------------------------
// Mock FeedValidator
// ---------------------------------------------------------------------------

type mockValidator struct {
	title string
	err   error
}

func (m *mockValidator) ValidateFeed(_ context.Context, _ string) (string, error) {
	return m.title, m.err
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const (
	testKBID   = "kb-111"
	testFeedID = "feed-222"
)

func makeFeed() *rss.RSSFeedRow {
	title := "Test Feed"
	return &rss.RSSFeedRow{
		ID:                  testFeedID,
		KbID:                testKBID,
		URL:                 "https://example.com/rss",
		Title:               &title,
		SyncSchedule:        "manual",
		Status:              "active",
		ConsecutiveFailures: 0,
		ItemCount:           0,
		CreatedAt:           time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

// newRequest builds a test HTTP request with optional JSON body.
func newRequest(method, path string, body any) *http.Request {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	return httptest.NewRequest(method, path, &buf)
}

// withKBAccess injects a KBAccessResult into the request context so
// kbIDFromContext picks it up without needing real middleware.
func withKBAccess(r *http.Request, kbID string) *http.Request {
	kb := &kbaccess.KnowledgeBase{ID: kbID}
	result := &kbaccess.KBAccessResult{KB: kb, Role: kbaccess.RoleEdit}
	return r.WithContext(kbaccess.WithAccess(r.Context(), result))
}

// withFeedIDPath sets PathValue("feedId") on the request.
// net/http/httptest does not parse path values, so we use a thin ServeMux trick.
func serveFeedID(feedID string, handler http.HandlerFunc, r *http.Request) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	pattern := r.Method + " /api/kb/{id}/rss/{feedId}"
	// Rebuild URL so the mux can match it.
	r.URL.Path = "/api/kb/" + testKBID + "/rss/" + feedID
	mux.HandleFunc(pattern, handler)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, r)
	return rr
}

func newHandlerForTest(store *mockStore, validator *mockValidator) *rss.Handler {
	return rss.NewHandlerWithValidator(store, validator)
}

// ---------------------------------------------------------------------------
// Tests: CreateRSSFeed
// ---------------------------------------------------------------------------

func TestCreateRSSFeed_Valid(t *testing.T) {
	feed := makeFeed()
	store := &mockStore{feed: feed}
	validator := &mockValidator{title: "Test Feed"}
	h := newHandlerForTest(store, validator)

	body := map[string]any{"url": "https://example.com/rss"}
	req := withKBAccess(newRequest(http.MethodPost, "/api/kb/"+testKBID+"/rss", body), testKBID)
	rr := httptest.NewRecorder()
	h.CreateRSSFeed(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}

	var got rss.RSSFeedRow
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.ID != testFeedID {
		t.Errorf("expected feed ID %q, got %q", testFeedID, got.ID)
	}
}

func TestCreateRSSFeed_MissingURL(t *testing.T) {
	store := &mockStore{}
	validator := &mockValidator{}
	h := newHandlerForTest(store, validator)

	body := map[string]any{} // no url
	req := withKBAccess(newRequest(http.MethodPost, "/api/kb/"+testKBID+"/rss", body), testKBID)
	rr := httptest.NewRecorder()
	h.CreateRSSFeed(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestCreateRSSFeed_InvalidSyncSchedule(t *testing.T) {
	store := &mockStore{}
	validator := &mockValidator{title: "Feed"}
	h := newHandlerForTest(store, validator)

	body := map[string]any{"url": "https://example.com/feed.xml", "syncSchedule": "hourly"}
	req := withKBAccess(newRequest(http.MethodPost, "/api/kb/"+testKBID+"/rss", body), testKBID)
	rr := httptest.NewRecorder()
	h.CreateRSSFeed(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestCreateRSSFeed_DefaultsToManual(t *testing.T) {
	feed := makeFeed()
	store := &mockStore{feed: feed}
	validator := &mockValidator{title: "Feed"}
	h := newHandlerForTest(store, validator)

	body := map[string]any{"url": "https://example.com/feed.xml"}
	req := withKBAccess(newRequest(http.MethodPost, "/api/kb/"+testKBID+"/rss", body), testKBID)
	rr := httptest.NewRecorder()
	h.CreateRSSFeed(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}
	if store.lastCreatedSchedule != "manual" {
		t.Fatalf("expected manual, got %q", store.lastCreatedSchedule)
	}
}

// ---------------------------------------------------------------------------
// Tests: ListRSSFeeds
// ---------------------------------------------------------------------------

func TestListRSSFeeds_OK(t *testing.T) {
	feed := makeFeed()
	store := &mockStore{feeds: []rss.RSSFeedRow{*feed}}
	validator := &mockValidator{}
	h := newHandlerForTest(store, validator)

	req := withKBAccess(newRequest(http.MethodGet, "/api/kb/"+testKBID+"/rss", nil), testKBID)
	rr := httptest.NewRecorder()
	h.ListRSSFeeds(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var got []rss.RSSFeedRow
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 feed, got %d", len(got))
	}
	if got[0].ID != testFeedID {
		t.Errorf("expected feed ID %q, got %q", testFeedID, got[0].ID)
	}
}

// ---------------------------------------------------------------------------
// Tests: UpdateRSSFeed
// ---------------------------------------------------------------------------

func TestUpdateRSSFeed_OK(t *testing.T) {
	feed := makeFeed()
	updated := makeFeed()
	updated.SyncSchedule = "daily"

	store := &mockStore{feed: feed, updated: updated}
	validator := &mockValidator{}
	h := newHandlerForTest(store, validator)

	body := map[string]any{"syncSchedule": "daily"}
	req := withKBAccess(newRequest(http.MethodPatch, "/api/kb/"+testKBID+"/rss/"+testFeedID, body), testKBID)
	rr := serveFeedID(testFeedID, h.UpdateRSSFeed, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var got rss.RSSFeedRow
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.SyncSchedule != "daily" {
		t.Errorf("expected syncSchedule %q, got %q", "daily", got.SyncSchedule)
	}
}

func TestUpdateRSSFeed_InvalidSyncSchedule(t *testing.T) {
	feed := makeFeed()
	store := &mockStore{feed: feed}
	validator := &mockValidator{}
	h := newHandlerForTest(store, validator)

	body := map[string]any{"syncSchedule": "hourly"}
	req := withKBAccess(newRequest(http.MethodPatch, "/api/kb/"+testKBID+"/rss/"+testFeedID, body), testKBID)
	rr := serveFeedID(testFeedID, h.UpdateRSSFeed, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestUpdateRSSFeed_ClearsNextSyncAt verifies that changing syncSchedule
// clears next_sync_at so the sweeper re-stamps the slot on its next tick
// instead of leaving a stale slot from the previous schedule in place.
func TestUpdateRSSFeed_ClearsNextSyncAt(t *testing.T) {
	feed := makeFeed()
	updated := makeFeed()
	updated.SyncSchedule = "daily"

	store := &mockStore{feed: feed, updated: updated}
	validator := &mockValidator{}
	h := newHandlerForTest(store, validator)

	body := map[string]any{"syncSchedule": "daily"}
	req := withKBAccess(newRequest(http.MethodPatch, "/api/kb/"+testKBID+"/rss/"+testFeedID, body), testKBID)
	rr := serveFeedID(testFeedID, h.UpdateRSSFeed, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if store.lastUpdate.NextSyncAt == nil {
		t.Fatal("expected NextSyncAt to be set (to clear it) when syncSchedule changes")
	}
	if *store.lastUpdate.NextSyncAt != nil {
		t.Fatalf("expected NextSyncAt to be cleared to NULL, got %v", **store.lastUpdate.NextSyncAt)
	}
}

// TestUpdateRSSFeed_NoScheduleChangeLeavesNextSyncAt verifies that a PATCH
// not touching syncSchedule does not touch next_sync_at.
func TestUpdateRSSFeed_NoScheduleChangeLeavesNextSyncAt(t *testing.T) {
	feed := makeFeed()
	updated := makeFeed()
	updated.FetchFullText = true

	store := &mockStore{feed: feed, updated: updated}
	validator := &mockValidator{}
	h := newHandlerForTest(store, validator)

	body := map[string]any{"fetchFullText": true}
	req := withKBAccess(newRequest(http.MethodPatch, "/api/kb/"+testKBID+"/rss/"+testFeedID, body), testKBID)
	rr := serveFeedID(testFeedID, h.UpdateRSSFeed, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if store.lastUpdate.NextSyncAt != nil {
		t.Fatalf("expected NextSyncAt to stay untouched, got %v", store.lastUpdate.NextSyncAt)
	}
}

// TestUpdateRSSFeed_ReactivatingClearsNextSyncAt verifies that resuming a
// paused feed (status -> "active", with no syncSchedule in the request body)
// clears next_sync_at. Without this, resuming a feed that was paused for a
// week fires an immediate daytime sync on the next sweep, because the
// week-old next_sync_at is still in the past — exactly what the
// stamp-without-enqueue design in ListUnscheduled exists to prevent.
func TestUpdateRSSFeed_ReactivatingClearsNextSyncAt(t *testing.T) {
	feed := makeFeed()
	feed.Status = "paused"
	updated := makeFeed()
	updated.Status = "active"

	store := &mockStore{feed: feed, updated: updated}
	validator := &mockValidator{}
	h := newHandlerForTest(store, validator)

	body := map[string]any{"status": "active"}
	req := withKBAccess(newRequest(http.MethodPatch, "/api/kb/"+testKBID+"/rss/"+testFeedID, body), testKBID)
	rr := serveFeedID(testFeedID, h.UpdateRSSFeed, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if store.lastUpdate.NextSyncAt == nil {
		t.Fatal("expected NextSyncAt to be set (to clear it) when re-activating")
	}
	if *store.lastUpdate.NextSyncAt != nil {
		t.Fatalf("expected NextSyncAt to be cleared to NULL, got %v", **store.lastUpdate.NextSyncAt)
	}
}

// ---------------------------------------------------------------------------
// Tests: DeleteRSSFeed
// ---------------------------------------------------------------------------

func TestDeleteRSSFeed_OK(t *testing.T) {
	feed := makeFeed()
	store := &mockStore{feed: feed}
	validator := &mockValidator{}
	h := newHandlerForTest(store, validator)

	req := withKBAccess(newRequest(http.MethodDelete, "/api/kb/"+testKBID+"/rss/"+testFeedID, nil), testKBID)
	rr := serveFeedID(testFeedID, h.DeleteRSSFeed, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestDeleteRSSFeed_DropsTablesBeforeDeletingFeed pins R60: DeleteRSSFeed
// must drop every one of its files' materialised spreadsheet tables BEFORE
// the feed delete, which relies on files.rss_feed_id ON DELETE CASCADE and
// never drops the physical tables itself.
func TestDeleteRSSFeed_DropsTablesBeforeDeletingFeed(t *testing.T) {
	feed := makeFeed()
	var events []string
	store := &mockStore{feed: feed, fileIDs: []string{"file-1", "file-2"}, events: &events}
	dropper := &fakeTableDropper{events: &events}
	h := newHandlerForTest(store, &mockValidator{})
	h.SetTableDropper(dropper)

	req := withKBAccess(newRequest(http.MethodDelete, "/api/kb/"+testKBID+"/rss/"+testFeedID, nil), testKBID)
	rr := serveFeedID(testFeedID, h.DeleteRSSFeed, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rr.Code, rr.Body.String())
	}
	wantEvents := []string{"drop:file-1", "drop:file-2", "delete:" + testFeedID}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Errorf("event order = %v, want %v", events, wantEvents)
	}
}

// TestDeleteRSSFeed_NilDropperIsNoop pins the nil-safety half: a deployment
// without a main pool (or a caller that never wired one) must still delete
// the feed, unaffected.
func TestDeleteRSSFeed_NilDropperIsNoop(t *testing.T) {
	feed := makeFeed()
	store := &mockStore{feed: feed, fileIDs: []string{"file-1"}}
	h := newHandlerForTest(store, &mockValidator{})
	// TableDropper deliberately left nil.

	req := withKBAccess(newRequest(http.MethodDelete, "/api/kb/"+testKBID+"/rss/"+testFeedID, nil), testKBID)
	rr := serveFeedID(testFeedID, h.DeleteRSSFeed, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rr.Code, rr.Body.String())
	}
	if store.deletedFeedID != testFeedID {
		t.Errorf("deletedFeedID = %q, want %q", store.deletedFeedID, testFeedID)
	}
}

// ---------------------------------------------------------------------------
// Tests: fetchFullText wiring (regression tests)
// ---------------------------------------------------------------------------

// TestCreateRSSFeed_PassesFetchFullText verifies that fetchFullText=true in the
// POST body is forwarded to the store. The create wiring was added in Task 1, so
// this test is expected to pass immediately; it serves as a regression guard.
func TestCreateRSSFeed_PassesFetchFullText(t *testing.T) {
	feed := makeFeed()
	store := &mockStore{feed: feed}
	validator := &mockValidator{title: "Feed"}
	h := newHandlerForTest(store, validator)

	body := map[string]any{"url": "https://example.com/feed.xml", "fetchFullText": true}
	req := withKBAccess(newRequest(http.MethodPost, "/api/kb/"+testKBID+"/rss", body), testKBID)
	rr := httptest.NewRecorder()
	h.CreateRSSFeed(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	if !store.createdFetchFullText {
		t.Fatal("expected fetchFullText=true to be passed to the store")
	}
}

// TestUpdateRSSFeed_PassesFetchFullText verifies that fetchFullText=true in the
// PATCH body is forwarded to the store via RSSFeedUpdate.FetchFullText.
// This test was RED before the updateRSSFeedRequest / UpdateRSSFeed wiring was added.
func TestUpdateRSSFeed_PassesFetchFullText(t *testing.T) {
	feed := makeFeed()
	updated := makeFeed()
	updated.FetchFullText = true

	store := &mockStore{feed: feed, updated: updated}
	validator := &mockValidator{}
	h := newHandlerForTest(store, validator)

	body := map[string]any{"fetchFullText": true}
	req := withKBAccess(newRequest(http.MethodPatch, "/api/kb/"+testKBID+"/rss/"+testFeedID, body), testKBID)
	rr := serveFeedID(testFeedID, h.UpdateRSSFeed, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if store.updatedFetchFullText == nil || !*store.updatedFetchFullText {
		t.Fatal("expected fetchFullText=true to be passed to the store via RSSFeedUpdate")
	}
}
