package admineval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/eval"
	storepkg "github.com/justrag/go-backend/internal/store"
)

// ---------------------------------------------------------------------------
// Mock implementations
// ---------------------------------------------------------------------------

var (
	_ runStore         = (*mockRunStore)(nil)
	_ kbReader         = (*mockKBReader)(nil)
	_ siteConfigReader = (*mockSiteConfig)(nil)
	_ enqueuer         = (*mockEnqueuer)(nil)
	_ goldenSetStore   = (*mockGoldenSetStore)(nil)
)

type mockRunStore struct {
	insertID        uuid.UUID
	insertErr       error
	inserted        eval.Run
	markFailedCalls []struct {
		id  uuid.UUID
		msg string
	}
	markFailedErr error

	// Get — if getByID is non-nil, ID-keyed lookup is used; otherwise the
	// fixed getRun/getErr pair is returned (backwards-compatible).
	getRun     *eval.Run
	getErr     error
	getByID    map[uuid.UUID]*eval.Run
	getErrByID map[uuid.UUID]error

	// List
	listRuns  []eval.Run
	listTotal int
	listErr   error
	listOpts  eval.ListOpts // captured for assertion

	// Delete
	deleteDeleted bool
	deleteRunning bool
	deleteErr     error
}

func (m *mockRunStore) Insert(_ context.Context, r eval.Run) (uuid.UUID, error) {
	m.inserted = r
	return m.insertID, m.insertErr
}

func (m *mockRunStore) MarkFailed(_ context.Context, id uuid.UUID, msg string) error {
	m.markFailedCalls = append(m.markFailedCalls, struct {
		id  uuid.UUID
		msg string
	}{id, msg})
	return m.markFailedErr
}

func (m *mockRunStore) Get(_ context.Context, id uuid.UUID) (*eval.Run, error) {
	// Per-ID map takes precedence over the fixed pair.
	if m.getByID != nil {
		if errMap, ok := m.getErrByID[id]; ok {
			return nil, errMap
		}
		if run, ok := m.getByID[id]; ok {
			return run, nil
		}
	}
	return m.getRun, m.getErr
}

func (m *mockRunStore) List(_ context.Context, opts eval.ListOpts) ([]eval.Run, int, error) {
	m.listOpts = opts
	return m.listRuns, m.listTotal, m.listErr
}

func (m *mockRunStore) Delete(_ context.Context, _ uuid.UUID) (bool, bool, error) {
	return m.deleteDeleted, m.deleteRunning, m.deleteErr
}

func (m *mockRunStore) MarkRunning(_ context.Context, _ uuid.UUID) error { return nil }

func (m *mockRunStore) MarkCompleted(_ context.Context, _ uuid.UUID, _ json.RawMessage) error {
	return nil
}

func (m *mockRunStore) HasActiveRun(_ context.Context, _ uuid.UUID) (bool, error) {
	return false, nil
}

// ---------------------------------------------------------------------------

type mockKBReader struct {
	name  string
	found bool
	err   error
}

func (m *mockKBReader) GetKBInfo(_ context.Context, _ uuid.UUID) (string, bool, error) {
	return m.name, m.found, m.err
}

// ---------------------------------------------------------------------------

type mockSiteConfig struct {
	values map[string]*string
}

func (m *mockSiteConfig) GetSiteConfigValue(_ context.Context, key string) (*string, error) {
	v, ok := m.values[key]
	if !ok {
		return nil, nil
	}
	return v, nil
}

// ---------------------------------------------------------------------------

type mockEnqueuer struct {
	err   error
	calls int
}

func (m *mockEnqueuer) EnqueueContext(_ context.Context, _ *asynq.Task, _ ...asynq.Option) (*asynq.TaskInfo, error) {
	m.calls++
	return &asynq.TaskInfo{}, m.err
}

// ---------------------------------------------------------------------------

type mockGoldenSetStore struct {
	// Create
	createID        uuid.UUID
	createCreatedAt time.Time
	createErr       error

	// Get
	getGoldenSet *eval.GoldenSet
	getErr       error

	// List
	listSets []eval.GoldenSet
	listErr  error

	// Delete
	deleteDeleted bool
	deleteErr     error
}

func (m *mockGoldenSetStore) Create(_ context.Context, _ eval.GoldenSet) (uuid.UUID, time.Time, error) {
	return m.createID, m.createCreatedAt, m.createErr
}

func (m *mockGoldenSetStore) Get(_ context.Context, _ uuid.UUID) (*eval.GoldenSet, error) {
	return m.getGoldenSet, m.getErr
}

func (m *mockGoldenSetStore) List(_ context.Context) ([]eval.GoldenSet, error) {
	return m.listSets, m.listErr
}

func (m *mockGoldenSetStore) Delete(_ context.Context, _ uuid.UUID) (bool, error) {
	return m.deleteDeleted, m.deleteErr
}

func (m *mockGoldenSetStore) ListByKB(_ context.Context, _ uuid.UUID) ([]eval.GoldenSet, error) {
	return m.listSets, m.listErr
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func strPtr(s string) *string { return &s }

// injectUser builds an http.Request with auth.Claims injected into context,
// mirroring the pattern used by auth.Middleware.
func injectUser(r *http.Request, userID string) *http.Request {
	claims := &auth.Claims{
		ID:   userID,
		Role: "admin",
	}
	ctx := auth.WithUser(r.Context(), claims)
	return r.WithContext(ctx)
}

var testKBID = uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
var testRunID = uuid.MustParse("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
var testUserID = "cccccccc-cccc-cccc-cccc-cccccccccccc"
var testGoldenSetID = uuid.MustParse("dddddddd-dddd-dddd-dddd-dddddddddddd")

// defaultSiteConfig returns a site config mock with all eval defaults wired.
func defaultSiteConfig() *mockSiteConfig {
	return &mockSiteConfig{
		values: map[string]*string{
			"eval_default_kb_id":         strPtr(testKBID.String()),
			"eval_default_golden_set_id": strPtr(testGoldenSetID.String()),
			"eval_default_judge":         strPtr("false"),
			"eval_default_top_k":         strPtr("10"),
		},
	}
}

// defaultGoldenSet returns a GoldenSet stub used in happy-path tests.
func defaultGoldenSet() *eval.GoldenSet {
	return &eval.GoldenSet{
		ID:            testGoldenSetID,
		Name:          "test-set",
		ContentHash:   "abcdef1234567890",
		QuestionCount: 1,
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestCreateRun_Valid_201 verifies the happy path: all defaults wired via
// site_config, valid KB, valid golden set → 201 + CreateRunResponse.
func TestCreateRun_Valid_201(t *testing.T) {
	store := &mockRunStore{insertID: testRunID}
	kbStore := &mockKBReader{name: "Test KB", found: true}
	cfg := defaultSiteConfig()
	eq := &mockEnqueuer{}
	gsStore := &mockGoldenSetStore{getGoldenSet: defaultGoldenSet()}

	h := NewHandler(store, kbStore, cfg, eq, gsStore, nil)

	body, _ := json.Marshal(CreateRunRequest{})
	req := httptest.NewRequest(http.MethodPost, "/api/admin/eval/runs", bytes.NewReader(body))
	req = injectUser(req, testUserID)
	rec := httptest.NewRecorder()
	h.CreateRun(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp CreateRunResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ID != testRunID {
		t.Errorf("expected run ID %s, got %s", testRunID, resp.ID)
	}
	if resp.Status != "queued" {
		t.Errorf("expected status 'queued', got %q", resp.Status)
	}

	// Enqueuer must have been called exactly once.
	if eq.calls != 1 {
		t.Errorf("expected 1 enqueue call, got %d", eq.calls)
	}

	// MarkFailed must NOT have been called.
	if len(store.markFailedCalls) != 0 {
		t.Errorf("expected no MarkFailed calls, got %d", len(store.markFailedCalls))
	}

	// GoldenSetID must be set on the inserted run.
	if store.inserted.GoldenSetID == nil || *store.inserted.GoldenSetID != testGoldenSetID {
		t.Errorf("expected GoldenSetID %s on inserted run, got %v", testGoldenSetID, store.inserted.GoldenSetID)
	}
	// FixtureHash must come from the golden set.
	if store.inserted.FixtureHash != defaultGoldenSet().ContentHash {
		t.Errorf("expected FixtureHash %q, got %q", defaultGoldenSet().ContentHash, store.inserted.FixtureHash)
	}
}

// TestCreateRun_UnknownGoldenSet verifies that a golden_set_id not found in
// the store returns 400.
func TestCreateRun_UnknownGoldenSet(t *testing.T) {
	store := &mockRunStore{insertID: testRunID}
	kbStore := &mockKBReader{name: "Test KB", found: true}
	cfg := defaultSiteConfig()
	eq := &mockEnqueuer{}
	gsStore := &mockGoldenSetStore{getErr: storepkg.ErrNotFound}

	h := NewHandler(store, kbStore, cfg, eq, gsStore, nil)

	body, _ := json.Marshal(CreateRunRequest{})
	req := httptest.NewRequest(http.MethodPost, "/api/admin/eval/runs", bytes.NewReader(body))
	req = injectUser(req, testUserID)
	rec := httptest.NewRecorder()
	h.CreateRun(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestCreateRun_UnknownKB verifies that an unknown KB ID returns 400.
func TestCreateRun_UnknownKB(t *testing.T) {
	store := &mockRunStore{insertID: testRunID}
	kbStore := &mockKBReader{found: false} // KB not found
	cfg := defaultSiteConfig()
	eq := &mockEnqueuer{}
	gsStore := &mockGoldenSetStore{} // KB check runs before golden set lookup

	h := NewHandler(store, kbStore, cfg, eq, gsStore, nil)

	body, _ := json.Marshal(CreateRunRequest{})
	req := httptest.NewRequest(http.MethodPost, "/api/admin/eval/runs", bytes.NewReader(body))
	req = injectUser(req, testUserID)
	rec := httptest.NewRecorder()
	h.CreateRun(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestCreateRun_InvalidJSON verifies that an unparseable body returns 400.
func TestCreateRun_InvalidJSON(t *testing.T) {
	store := &mockRunStore{insertID: testRunID}
	kbStore := &mockKBReader{name: "Test KB", found: true}
	cfg := defaultSiteConfig()
	eq := &mockEnqueuer{}

	h := NewHandler(store, kbStore, cfg, eq, &mockGoldenSetStore{}, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/admin/eval/runs", bytes.NewReader([]byte("not-json")))
	req.ContentLength = 8 // non-zero so the decoder runs
	req = injectUser(req, testUserID)
	rec := httptest.NewRecorder()
	h.CreateRun(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestCreateRun_InsertFailure verifies that a DB insert error returns 500.
func TestCreateRun_InsertFailure(t *testing.T) {
	store := &mockRunStore{
		insertErr: errors.New("db connection lost"),
	}
	kbStore := &mockKBReader{name: "Test KB", found: true}
	cfg := defaultSiteConfig()
	eq := &mockEnqueuer{}
	gsStore := &mockGoldenSetStore{getGoldenSet: defaultGoldenSet()}

	h := NewHandler(store, kbStore, cfg, eq, gsStore, nil)

	body, _ := json.Marshal(CreateRunRequest{})
	req := httptest.NewRequest(http.MethodPost, "/api/admin/eval/runs", bytes.NewReader(body))
	req = injectUser(req, testUserID)
	rec := httptest.NewRecorder()
	h.CreateRun(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}

	// Enqueuer must NOT have been called.
	if eq.calls != 0 {
		t.Errorf("expected no enqueue calls after insert failure, got %d", eq.calls)
	}
}

// TestCreateRun_EnqueueFailure verifies that an enqueue error returns 500 AND
// the store sees a MarkFailed call with the error message.
func TestCreateRun_EnqueueFailure(t *testing.T) {
	store := &mockRunStore{insertID: testRunID}
	kbStore := &mockKBReader{name: "Test KB", found: true}
	cfg := defaultSiteConfig()
	eq := &mockEnqueuer{err: errors.New("redis unavailable")}
	gsStore := &mockGoldenSetStore{getGoldenSet: defaultGoldenSet()}

	h := NewHandler(store, kbStore, cfg, eq, gsStore, nil)

	body, _ := json.Marshal(CreateRunRequest{})
	req := httptest.NewRequest(http.MethodPost, "/api/admin/eval/runs", bytes.NewReader(body))
	req = injectUser(req, testUserID)
	rec := httptest.NewRecorder()
	h.CreateRun(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}

	// MarkFailed must have been called exactly once with the run ID.
	if len(store.markFailedCalls) != 1 {
		t.Fatalf("expected 1 MarkFailed call, got %d", len(store.markFailedCalls))
	}
	call := store.markFailedCalls[0]
	if call.id != testRunID {
		t.Errorf("MarkFailed called with wrong run ID: got %s, want %s", call.id, testRunID)
	}
	if call.msg == "" {
		t.Error("MarkFailed error message must not be empty")
	}
}

// ---------------------------------------------------------------------------
// Task 9: ListRuns tests
// ---------------------------------------------------------------------------

// makeRuns builds n eval.Run stubs with the given status.
func makeRuns(n int, status string) []eval.Run {
	runs := make([]eval.Run, n)
	for i := range runs {
		runs[i] = eval.Run{
			ID:     uuid.New(),
			Status: status,
			KBID:   testKBID,
		}
	}
	return runs
}

// TestListRuns_Pagination seeds 75 mock runs and requests limit=50, offset=0.
// Asserts len(Runs)==50 and Total==75.
func TestListRuns_Pagination(t *testing.T) {
	runs := makeRuns(50, "completed")
	store := &mockRunStore{
		listRuns:  runs,
		listTotal: 75,
	}
	kbStore := &mockKBReader{name: "Test KB", found: true}
	h := NewHandler(store, kbStore, &mockSiteConfig{}, &mockEnqueuer{}, &mockGoldenSetStore{}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/eval/runs?limit=50&offset=0", nil)
	rec := httptest.NewRecorder()
	h.ListRuns(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp ListRunsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Runs) != 50 {
		t.Errorf("expected 50 runs, got %d", len(resp.Runs))
	}
	if resp.Total != 75 {
		t.Errorf("expected Total=75, got %d", resp.Total)
	}
	// Verify store was called with the correct opts.
	if store.listOpts.Limit != 50 {
		t.Errorf("store called with limit=%d, want 50", store.listOpts.Limit)
	}
	if store.listOpts.Offset != 0 {
		t.Errorf("store called with offset=%d, want 0", store.listOpts.Offset)
	}
}

// TestListRuns_StatusFilter verifies that only runs matching the status filter
// are returned.
func TestListRuns_StatusFilter(t *testing.T) {
	runningRuns := makeRuns(3, "running")
	store := &mockRunStore{
		listRuns:  runningRuns,
		listTotal: 3,
	}
	kbStore := &mockKBReader{name: "Test KB", found: true}
	h := NewHandler(store, kbStore, &mockSiteConfig{}, &mockEnqueuer{}, &mockGoldenSetStore{}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/eval/runs?status=running", nil)
	rec := httptest.NewRecorder()
	h.ListRuns(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp ListRunsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 3 {
		t.Errorf("expected Total=3, got %d", resp.Total)
	}
	for _, r := range resp.Runs {
		if r.Status != "running" {
			t.Errorf("expected status=running, got %q", r.Status)
		}
	}
	// Verify the store received the status filter.
	if store.listOpts.Status != "running" {
		t.Errorf("store called with status=%q, want %q", store.listOpts.Status, "running")
	}
}

// TestListRuns_FlattenedAggregate seeds one completed run with a valid Report
// JSON and asserts the response's Aggregate and RouteMeanRecall are populated.
func TestListRuns_FlattenedAggregate(t *testing.T) {
	reportJSON := json.RawMessage(`{"aggregate":{"count":10,"mean_recall":0.5,"mrr":0.3},"route_aggregates":{"lookup":{"mean_recall":0.6},"enumeration":{"mean_recall":0.4}}}`)
	run := eval.Run{
		ID:     uuid.New(),
		Status: "completed",
		KBID:   testKBID,
		Report: reportJSON,
	}
	store := &mockRunStore{
		listRuns:  []eval.Run{run},
		listTotal: 1,
	}
	kbStore := &mockKBReader{name: "Test KB", found: true}
	h := NewHandler(store, kbStore, &mockSiteConfig{}, &mockEnqueuer{}, &mockGoldenSetStore{}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/eval/runs", nil)
	rec := httptest.NewRecorder()
	h.ListRuns(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp ListRunsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(resp.Runs))
	}
	s := resp.Runs[0]
	if s.Aggregate == nil {
		t.Fatal("expected Aggregate to be populated, got nil")
	}
	if s.Aggregate.Count != 10 {
		t.Errorf("Aggregate.Count = %d, want 10", s.Aggregate.Count)
	}
	if s.Aggregate.MeanRecall != 0.5 {
		t.Errorf("Aggregate.MeanRecall = %f, want 0.5", s.Aggregate.MeanRecall)
	}
	if s.Aggregate.MRR != 0.3 {
		t.Errorf("Aggregate.MRR = %f, want 0.3", s.Aggregate.MRR)
	}
	if s.RouteMeanRecall == nil {
		t.Fatal("expected RouteMeanRecall to be populated, got nil")
	}
	if s.RouteMeanRecall["lookup"] != 0.6 {
		t.Errorf("RouteMeanRecall[lookup] = %f, want 0.6", s.RouteMeanRecall["lookup"])
	}
	if s.RouteMeanRecall["enumeration"] != 0.4 {
		t.Errorf("RouteMeanRecall[enumeration] = %f, want 0.4", s.RouteMeanRecall["enumeration"])
	}
}

// TestListRuns_LimitCap verifies that limit=500 is capped to 200 when passed
// to the store.
func TestListRuns_LimitCap(t *testing.T) {
	store := &mockRunStore{
		listRuns:  []eval.Run{},
		listTotal: 0,
	}
	kbStore := &mockKBReader{name: "Test KB", found: true}
	h := NewHandler(store, kbStore, &mockSiteConfig{}, &mockEnqueuer{}, &mockGoldenSetStore{}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/eval/runs?limit=500", nil)
	rec := httptest.NewRecorder()
	h.ListRuns(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if store.listOpts.Limit != 200 {
		t.Errorf("store called with limit=%d, want 200 (capped)", store.listOpts.Limit)
	}
}

// TestListRuns_KBIDFilter verifies that kb_id query param is forwarded as KBID
// in the list opts.
func TestListRuns_KBIDFilter(t *testing.T) {
	filterKBID := uuid.MustParse("dddddddd-dddd-dddd-dddd-dddddddddddd")
	store := &mockRunStore{
		listRuns:  []eval.Run{},
		listTotal: 0,
	}
	kbStore := &mockKBReader{name: "Test KB", found: true}
	h := NewHandler(store, kbStore, &mockSiteConfig{}, &mockEnqueuer{}, &mockGoldenSetStore{}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/eval/runs?kb_id="+filterKBID.String(), nil)
	rec := httptest.NewRecorder()
	h.ListRuns(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if store.listOpts.KBID == nil {
		t.Fatal("expected KBID to be set in list opts, got nil")
	}
	if *store.listOpts.KBID != filterKBID {
		t.Errorf("store called with KBID=%s, want %s", *store.listOpts.KBID, filterKBID)
	}
}

// ---------------------------------------------------------------------------
// Task 10: GetRun tests
// ---------------------------------------------------------------------------

// newGetRunRequest builds a GET request for /api/admin/eval/runs/{id} using
// PathValue so r.PathValue("id") works in handler tests.
func newGetRunRequest(id string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/admin/eval/runs/"+id, nil)
	req.SetPathValue("id", id)
	return req
}

// TestGetRun_ReturnsFullReport verifies the happy path: store returns a run
// with a Report; the response body includes the report.
func TestGetRun_ReturnsFullReport(t *testing.T) {
	reportJSON := json.RawMessage(`{"aggregate":{"count":5,"mean_recall":0.8,"mrr":0.7}}`)
	run := &eval.Run{
		ID:     testRunID,
		Status: "completed",
		KBID:   testKBID,
		Report: reportJSON,
	}
	store := &mockRunStore{getRun: run}
	h := NewHandler(store, &mockKBReader{name: "KB", found: true}, &mockSiteConfig{}, &mockEnqueuer{}, &mockGoldenSetStore{}, nil)

	req := newGetRunRequest(testRunID.String())
	rec := httptest.NewRecorder()
	h.GetRun(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var got eval.Run
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != testRunID {
		t.Errorf("run ID = %s, want %s", got.ID, testRunID)
	}
	if got.Status != "completed" {
		t.Errorf("status = %q, want %q", got.Status, "completed")
	}
	// Report should be preserved in the response.
	if string(got.Report) == "" {
		t.Error("expected Report to be present in response")
	}
}

// TestGetRun_404 verifies that a store returning store.ErrNotFound yields 404.
func TestGetRun_404(t *testing.T) {
	store := &mockRunStore{getErr: storepkg.ErrNotFound}
	h := NewHandler(store, &mockKBReader{}, &mockSiteConfig{}, &mockEnqueuer{}, &mockGoldenSetStore{}, nil)

	req := newGetRunRequest(testRunID.String())
	rec := httptest.NewRecorder()
	h.GetRun(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestGetRun_InvalidUUID verifies that a non-UUID path value returns 400.
func TestGetRun_InvalidUUID(t *testing.T) {
	store := &mockRunStore{}
	h := NewHandler(store, &mockKBReader{}, &mockSiteConfig{}, &mockEnqueuer{}, &mockGoldenSetStore{}, nil)

	req := newGetRunRequest("not-a-uuid")
	rec := httptest.NewRecorder()
	h.GetRun(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestGetRun_StoreError verifies that a generic store error returns 500.
func TestGetRun_StoreError(t *testing.T) {
	store := &mockRunStore{getErr: errors.New("db exploded")}
	h := NewHandler(store, &mockKBReader{}, &mockSiteConfig{}, &mockEnqueuer{}, &mockGoldenSetStore{}, nil)

	req := newGetRunRequest(testRunID.String())
	rec := httptest.NewRecorder()
	h.GetRun(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Task 12: DeleteRun tests
// ---------------------------------------------------------------------------

// newDeleteRunRequest builds a DELETE request for /api/admin/eval/runs/{id}.
func newDeleteRunRequest(id string) *http.Request {
	req := httptest.NewRequest(http.MethodDelete, "/api/admin/eval/runs/"+id, nil)
	req.SetPathValue("id", id)
	return req
}

// TestDeleteRun_OK verifies that a successful delete returns 204.
func TestDeleteRun_OK(t *testing.T) {
	store := &mockRunStore{deleteDeleted: true, deleteRunning: false}
	h := NewHandler(store, &mockKBReader{}, &mockSiteConfig{}, &mockEnqueuer{}, &mockGoldenSetStore{}, nil)

	req := newDeleteRunRequest(testRunID.String())
	rec := httptest.NewRecorder()
	h.DeleteRun(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestDeleteRun_NotFound verifies that (false, false, nil) yields 404.
func TestDeleteRun_NotFound(t *testing.T) {
	store := &mockRunStore{deleteDeleted: false, deleteRunning: false}
	h := NewHandler(store, &mockKBReader{}, &mockSiteConfig{}, &mockEnqueuer{}, &mockGoldenSetStore{}, nil)

	req := newDeleteRunRequest(testRunID.String())
	rec := httptest.NewRecorder()
	h.DeleteRun(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestDeleteRun_RunningConflict verifies that a running run returns 409 with
// the expected message.
func TestDeleteRun_RunningConflict(t *testing.T) {
	store := &mockRunStore{deleteDeleted: false, deleteRunning: true}
	h := NewHandler(store, &mockKBReader{}, &mockSiteConfig{}, &mockEnqueuer{}, &mockGoldenSetStore{}, nil)

	req := newDeleteRunRequest(testRunID.String())
	rec := httptest.NewRecorder()
	h.DeleteRun(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if body == "" {
		t.Error("expected non-empty error message in 409 response")
	}
}

// TestDeleteRun_InvalidUUID verifies that a non-UUID path value returns 400.
func TestDeleteRun_InvalidUUID(t *testing.T) {
	store := &mockRunStore{}
	h := NewHandler(store, &mockKBReader{}, &mockSiteConfig{}, &mockEnqueuer{}, &mockGoldenSetStore{}, nil)

	req := newDeleteRunRequest("not-a-uuid")
	rec := httptest.NewRecorder()
	h.DeleteRun(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestDeleteRun_StoreError verifies that a store error returns 500.
func TestDeleteRun_StoreError(t *testing.T) {
	store := &mockRunStore{deleteErr: errors.New("db exploded")}
	h := NewHandler(store, &mockKBReader{}, &mockSiteConfig{}, &mockEnqueuer{}, &mockGoldenSetStore{}, nil)

	req := newDeleteRunRequest(testRunID.String())
	rec := httptest.NewRecorder()
	h.DeleteRun(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Task 11: ExportRun tests
// ---------------------------------------------------------------------------

// newExportRunRequest builds a GET request for /api/admin/eval/runs/{id}/export.
func newExportRunRequest(id, compareWith string) *http.Request {
	target := "/api/admin/eval/runs/" + id + "/export"
	if compareWith != "" {
		target += "?compare_with=" + compareWith
	}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.SetPathValue("id", id)
	return req
}

// makeCompletedRun constructs a minimal completed Run with a parseable Report.
func makeCompletedRun(id uuid.UUID, label string) eval.Run {
	report := eval.Report{
		K: 10,
		Aggregate: eval.AggregateMetrics{
			K: 10, Count: 2, MeanRecall: 0.5,
		},
		RouteAggregates: map[string]eval.AggregateMetrics{
			"lookup": {K: 10, Count: 2, MeanRecall: 0.5},
		},
		Questions: []eval.QuestionReport{
			{
				Question: eval.Question{ID: "q001", Question: "What is X?", QueryType: "lookup"},
				Metrics:  eval.PerQuestionMetrics{K: 10, RecallAtK: 1.0},
			},
			{
				Question: eval.Question{ID: "q002", Question: "What is Y?", QueryType: "lookup"},
				Metrics:  eval.PerQuestionMetrics{K: 10, RecallAtK: 0.0},
			},
		},
	}
	repJSON, _ := json.Marshal(report)
	snap, _ := json.Marshal(map[string]*string{"mmr_lambda": strPtr("0.7")})
	return eval.Run{
		ID:             id,
		Status:         "completed",
		Label:          label,
		KBID:           testKBID,
		FixtureHash:    "abcdef1234567890",
		ConfigSnapshot: json.RawMessage(snap),
		Report:         json.RawMessage(repJSON),
	}
}

// TestExportRun_SingleRun verifies the happy path for a single-run export:
// 200, correct Content-Type, body contains single-run scaffold markers.
func TestExportRun_SingleRun(t *testing.T) {
	run := makeCompletedRun(testRunID, "my-baseline")
	store := &mockRunStore{getRun: &run}
	h := NewHandler(store, &mockKBReader{}, &mockSiteConfig{}, &mockEnqueuer{}, &mockGoldenSetStore{}, nil)

	req := newExportRunRequest(testRunID.String(), "")
	rec := httptest.NewRecorder()
	h.ExportRun(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	ct := rec.Header().Get("Content-Type")
	if ct != "text/markdown; charset=utf-8" {
		t.Errorf("Content-Type = %q, want %q", ct, "text/markdown; charset=utf-8")
	}
	body := rec.Body.String()
	// Must contain single-run scaffold markers.
	for _, want := range []string{"# Eval run:", "## Config snapshot", "## Aggregate", "## Top 5 questions"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing marker %q in body:\n%s", want, body)
		}
	}
	// Must NOT contain the compare header.
	if strings.Contains(body, "# Eval delta:") {
		t.Errorf("compare header must not appear in single-run export:\n%s", body)
	}
}

// TestExportRun_Compare verifies the compare path: two completed runs,
// ?compare_with param set; body contains compare scaffold markers.
func TestExportRun_Compare(t *testing.T) {
	otherID := uuid.MustParse("dddddddd-dddd-dddd-dddd-dddddddddddd")
	runA := makeCompletedRun(testRunID, "baseline")
	runB := makeCompletedRun(otherID, "candidate")

	store := &mockRunStore{
		getByID: map[uuid.UUID]*eval.Run{
			testRunID: &runA,
			otherID:   &runB,
		},
	}
	h := NewHandler(store, &mockKBReader{}, &mockSiteConfig{}, &mockEnqueuer{}, &mockGoldenSetStore{}, nil)

	req := newExportRunRequest(testRunID.String(), otherID.String())
	rec := httptest.NewRecorder()
	h.ExportRun(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	ct := rec.Header().Get("Content-Type")
	if ct != "text/markdown; charset=utf-8" {
		t.Errorf("Content-Type = %q, want %q", ct, "text/markdown; charset=utf-8")
	}
	body := rec.Body.String()
	// Must contain compare scaffold markers.
	for _, want := range []string{"# Eval delta:", "## Config diff", "## Aggregate delta (B − A)", "## Notable question changes"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing marker %q in body:\n%s", want, body)
		}
	}
}

// TestExportRun_NotFound verifies that a missing run returns 404.
func TestExportRun_NotFound(t *testing.T) {
	store := &mockRunStore{getErr: storepkg.ErrNotFound}
	h := NewHandler(store, &mockKBReader{}, &mockSiteConfig{}, &mockEnqueuer{}, &mockGoldenSetStore{}, nil)

	req := newExportRunRequest(testRunID.String(), "")
	rec := httptest.NewRecorder()
	h.ExportRun(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestExportRun_CompareInvalidUUID verifies that an invalid compare_with UUID
// returns 400.
func TestExportRun_CompareInvalidUUID(t *testing.T) {
	run := makeCompletedRun(testRunID, "baseline")
	store := &mockRunStore{getRun: &run}
	h := NewHandler(store, &mockKBReader{}, &mockSiteConfig{}, &mockEnqueuer{}, &mockGoldenSetStore{}, nil)

	req := newExportRunRequest(testRunID.String(), "not-a-uuid")
	rec := httptest.NewRecorder()
	h.ExportRun(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestExportRun_CompareRunNotFound verifies that a missing compare_with run
// returns 404.
func TestExportRun_CompareRunNotFound(t *testing.T) {
	otherID := uuid.MustParse("eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee")
	run := makeCompletedRun(testRunID, "baseline")
	store := &mockRunStore{
		getByID: map[uuid.UUID]*eval.Run{
			testRunID: &run,
		},
		getErrByID: map[uuid.UUID]error{
			otherID: storepkg.ErrNotFound,
		},
	}
	h := NewHandler(store, &mockKBReader{}, &mockSiteConfig{}, &mockEnqueuer{}, &mockGoldenSetStore{}, nil)

	req := newExportRunRequest(testRunID.String(), otherID.String())
	rec := httptest.NewRecorder()
	h.ExportRun(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// C5: GoldenSet handler tests
// ---------------------------------------------------------------------------

// minimalJSONL returns a two-question JSONL fixture as bytes.
func minimalJSONL() []byte {
	return []byte(
		`{"id":"q1","question":"What is X?","kb_id":"00000000-0000-0000-0000-000000000001","language":"en","must_cite_file_ids":["file-1"]}` + "\n" +
			`{"id":"q2","question":"What is Y?","kb_id":"00000000-0000-0000-0000-000000000001","language":"en","must_cite_file_ids":["file-2"]}` + "\n",
	)
}

// buildUploadRequest constructs a multipart/form-data request for POST
// /api/admin/eval/golden-sets. If name is empty, the field is omitted.
// If content is nil, the file field is omitted. kbID is always included when
// non-empty (CreateGoldenSet requires kb_id after FIX 1).
func buildUploadRequest(name string, content []byte) *http.Request {
	return buildUploadRequestWithKBID(name, content, testKBID.String())
}

// buildUploadRequestWithKBID is like buildUploadRequest but allows controlling
// the kb_id field. Pass an empty string to omit the field (tests the missing-field path).
func buildUploadRequestWithKBID(name string, content []byte, kbID string) *http.Request {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if kbID != "" {
		_ = mw.WriteField("kb_id", kbID)
	}
	if name != "" {
		_ = mw.WriteField("name", name)
	}
	if content != nil {
		part, _ := mw.CreateFormFile("file", "fixture.jsonl")
		_, _ = part.Write(content)
	}
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/eval/golden-sets", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return injectUser(req, testUserID)
}

// TestCreateGoldenSet_Valid_201 verifies the happy path: valid name + JSONL +
// kb_id → 201. The kbStore reports the KB as found (required by FIX 1).
func TestCreateGoldenSet_Valid_201(t *testing.T) {
	created := time.Now().UTC().Truncate(time.Second)
	gsStore := &mockGoldenSetStore{
		createID:        testGoldenSetID,
		createCreatedAt: created,
	}
	kbStore := &mockKBReader{name: "Test KB", found: true}
	h := NewHandler(&mockRunStore{}, kbStore, &mockSiteConfig{}, &mockEnqueuer{}, gsStore, nil)

	// buildUploadRequest now includes testKBID as kb_id by default.
	req := buildUploadRequest("my-set", minimalJSONL())
	rec := httptest.NewRecorder()
	h.CreateGoldenSet(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp CreateGoldenSetResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ID != testGoldenSetID {
		t.Errorf("ID = %s, want %s", resp.ID, testGoldenSetID)
	}
	if resp.Name != "my-set" {
		t.Errorf("Name = %q, want %q", resp.Name, "my-set")
	}
	if resp.QuestionCount != 2 {
		t.Errorf("QuestionCount = %d, want 2", resp.QuestionCount)
	}
	if resp.ContentHash == "" {
		t.Error("ContentHash must not be empty")
	}
}

// TestCreateGoldenSet_MissingKBID verifies that omitting kb_id returns 400.
func TestCreateGoldenSet_MissingKBID(t *testing.T) {
	gsStore := &mockGoldenSetStore{}
	h := NewHandler(&mockRunStore{}, &mockKBReader{name: "Test KB", found: true}, &mockSiteConfig{}, &mockEnqueuer{}, gsStore, nil)

	req := buildUploadRequestWithKBID("my-set", minimalJSONL(), "") // no kb_id
	rec := httptest.NewRecorder()
	h.CreateGoldenSet(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestCreateGoldenSet_KBNotFound verifies that an unknown kb_id returns 400.
func TestCreateGoldenSet_KBNotFound(t *testing.T) {
	gsStore := &mockGoldenSetStore{}
	kbStore := &mockKBReader{found: false} // KB not found
	h := NewHandler(&mockRunStore{}, kbStore, &mockSiteConfig{}, &mockEnqueuer{}, gsStore, nil)

	req := buildUploadRequest("my-set", minimalJSONL())
	rec := httptest.NewRecorder()
	h.CreateGoldenSet(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestCreateGoldenSet_MissingName verifies that an empty name field returns 400.
// The name check fires before the kb_id check, so kbStore is not reached.
func TestCreateGoldenSet_MissingName(t *testing.T) {
	gsStore := &mockGoldenSetStore{}
	h := NewHandler(&mockRunStore{}, &mockKBReader{name: "Test KB", found: true}, &mockSiteConfig{}, &mockEnqueuer{}, gsStore, nil)

	req := buildUploadRequest("", minimalJSONL())
	rec := httptest.NewRecorder()
	h.CreateGoldenSet(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestCreateGoldenSet_NoFile verifies that a missing file field returns 400.
func TestCreateGoldenSet_NoFile(t *testing.T) {
	gsStore := &mockGoldenSetStore{}
	h := NewHandler(&mockRunStore{}, &mockKBReader{name: "Test KB", found: true}, &mockSiteConfig{}, &mockEnqueuer{}, gsStore, nil)

	req := buildUploadRequest("my-set", nil)
	rec := httptest.NewRecorder()
	h.CreateGoldenSet(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestCreateGoldenSet_ParseError verifies that invalid JSONL returns 400.
func TestCreateGoldenSet_ParseError(t *testing.T) {
	gsStore := &mockGoldenSetStore{}
	h := NewHandler(&mockRunStore{}, &mockKBReader{name: "Test KB", found: true}, &mockSiteConfig{}, &mockEnqueuer{}, gsStore, nil)

	req := buildUploadRequest("my-set", []byte("not valid jsonl\n"))
	rec := httptest.NewRecorder()
	h.CreateGoldenSet(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestCreateGoldenSet_DuplicateName verifies that a duplicate name returns 409.
func TestCreateGoldenSet_DuplicateName(t *testing.T) {
	gsStore := &mockGoldenSetStore{createErr: eval.ErrGoldenSetNameTaken}
	h := NewHandler(&mockRunStore{}, &mockKBReader{name: "Test KB", found: true}, &mockSiteConfig{}, &mockEnqueuer{}, gsStore, nil)

	req := buildUploadRequest("existing-set", minimalJSONL())
	rec := httptest.NewRecorder()
	h.CreateGoldenSet(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestListGoldenSets seeds 3 sets in the mock and asserts the response contains them.
func TestListGoldenSets(t *testing.T) {
	now := time.Now().UTC()
	sets := []eval.GoldenSet{
		{ID: uuid.New(), Name: "set-a", ContentHash: "hash1", QuestionCount: 5, CreatedAt: now},
		{ID: uuid.New(), Name: "set-b", ContentHash: "hash2", QuestionCount: 3, CreatedAt: now.Add(-time.Hour)},
		{ID: uuid.New(), Name: "set-c", ContentHash: "hash3", QuestionCount: 1, CreatedAt: now.Add(-2 * time.Hour)},
	}
	gsStore := &mockGoldenSetStore{listSets: sets}
	h := NewHandler(&mockRunStore{}, &mockKBReader{}, &mockSiteConfig{}, &mockEnqueuer{}, gsStore, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/eval/golden-sets", nil)
	rec := httptest.NewRecorder()
	h.ListGoldenSets(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp ListGoldenSetsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.GoldenSets) != 3 {
		t.Fatalf("expected 3 golden sets, got %d", len(resp.GoldenSets))
	}
	for i, s := range resp.GoldenSets {
		if s.Name != sets[i].Name {
			t.Errorf("[%d] Name = %q, want %q", i, s.Name, sets[i].Name)
		}
		if s.QuestionCount != sets[i].QuestionCount {
			t.Errorf("[%d] QuestionCount = %d, want %d", i, s.QuestionCount, sets[i].QuestionCount)
		}
	}
}

// newDeleteGoldenSetRequest builds a DELETE request for /api/admin/eval/golden-sets/{id}.
func newDeleteGoldenSetRequest(id string) *http.Request {
	req := httptest.NewRequest(http.MethodDelete, "/api/admin/eval/golden-sets/"+id, nil)
	req.SetPathValue("id", id)
	return req
}

// TestDeleteGoldenSet_OK verifies that a successful delete returns 204.
func TestDeleteGoldenSet_OK(t *testing.T) {
	gsStore := &mockGoldenSetStore{deleteDeleted: true}
	h := NewHandler(&mockRunStore{}, &mockKBReader{}, &mockSiteConfig{}, &mockEnqueuer{}, gsStore, nil)

	req := newDeleteGoldenSetRequest(testGoldenSetID.String())
	rec := httptest.NewRecorder()
	h.DeleteGoldenSet(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestDeleteGoldenSet_NotFound verifies that (false, nil) yields 404.
func TestDeleteGoldenSet_NotFound(t *testing.T) {
	gsStore := &mockGoldenSetStore{deleteDeleted: false}
	h := NewHandler(&mockRunStore{}, &mockKBReader{}, &mockSiteConfig{}, &mockEnqueuer{}, gsStore, nil)

	req := newDeleteGoldenSetRequest(testGoldenSetID.String())
	rec := httptest.NewRecorder()
	h.DeleteGoldenSet(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestDeleteGoldenSet_InvalidUUID verifies that a non-UUID path value returns 400.
func TestDeleteGoldenSet_InvalidUUID(t *testing.T) {
	gsStore := &mockGoldenSetStore{}
	h := NewHandler(&mockRunStore{}, &mockKBReader{}, &mockSiteConfig{}, &mockEnqueuer{}, gsStore, nil)

	req := newDeleteGoldenSetRequest("not-a-uuid")
	rec := httptest.NewRecorder()
	h.DeleteGoldenSet(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}
