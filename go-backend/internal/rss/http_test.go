package rss_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	err                  error
}

func (m *mockStore) CreateRSSFeed(_ context.Context, kbID, url string, title *string, pollInterval int, fetchFullText bool) (*rss.RSSFeedRow, error) {
	if m.err != nil {
		return nil, m.err
	}
	m.createdFetchFullText = fetchFullText
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
	return m.updated, nil
}

func (m *mockStore) DeleteRSSFeed(_ context.Context, feedID string) error {
	return m.err
}

func (m *mockStore) ListActiveRSSFeeds(_ context.Context) ([]rss.RSSFeedRow, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.feeds, nil
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
		PollInterval:        60,
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
	result := &kbaccess.KBAccessResult{KB: kb, Permission: "edit"}
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

	body := map[string]any{"url": "https://example.com/rss", "pollInterval": 60}
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

	body := map[string]any{"pollInterval": 60} // no url
	req := withKBAccess(newRequest(http.MethodPost, "/api/kb/"+testKBID+"/rss", body), testKBID)
	rr := httptest.NewRecorder()
	h.CreateRSSFeed(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestCreateRSSFeed_InvalidPollInterval(t *testing.T) {
	store := &mockStore{}
	validator := &mockValidator{title: "Feed"}
	h := newHandlerForTest(store, validator)

	body := map[string]any{"url": "https://example.com/rss", "pollInterval": 5} // too low
	req := withKBAccess(newRequest(http.MethodPost, "/api/kb/"+testKBID+"/rss", body), testKBID)
	rr := httptest.NewRecorder()
	h.CreateRSSFeed(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
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
	newInterval := 120
	updated.PollInterval = newInterval

	store := &mockStore{feed: feed, updated: updated}
	validator := &mockValidator{}
	h := newHandlerForTest(store, validator)

	body := map[string]any{"pollInterval": 120}
	req := withKBAccess(newRequest(http.MethodPatch, "/api/kb/"+testKBID+"/rss/"+testFeedID, body), testKBID)
	rr := serveFeedID(testFeedID, h.UpdateRSSFeed, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var got rss.RSSFeedRow
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.PollInterval != newInterval {
		t.Errorf("expected pollInterval %d, got %d", newInterval, got.PollInterval)
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
