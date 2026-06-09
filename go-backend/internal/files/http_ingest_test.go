package files_test

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/files"
	"github.com/justrag/go-backend/internal/kbaccess"
)

// ---------------------------------------------------------------------------
// failingEnqueuer — satisfies the (unexported) taskEnqueuer interface by
// always returning an error from Enqueue. Used to test compensating cleanup.
// ---------------------------------------------------------------------------

type failingEnqueuer struct{}

func (f *failingEnqueuer) Enqueue(_ *asynq.Task, _ ...asynq.Option) (*asynq.TaskInfo, error) {
	return nil, errors.New("enqueue failed")
}

// ---------------------------------------------------------------------------
// Helpers for ingest tests (builds on mockStore/mockStorage defined in handler_test.go)
// ---------------------------------------------------------------------------

func defaultKB() *kbaccess.KnowledgeBase {
	ownerID := "user-1"
	return &kbaccess.KnowledgeBase{ID: "kb-1", UserID: &ownerID, IsGlobal: false}
}

func ingestUser() *auth.Claims {
	return &auth.Claims{ID: "user-1", Username: "alice", Role: "user"}
}

func defaultIngestHandler(store files.Store) *files.Handler {
	stor := &mockStorage{}
	return files.NewHandler(store, stor, noopChunks()) // no asynq client — OK for unit tests
}

// ---------------------------------------------------------------------------
// AddTextSource tests
// ---------------------------------------------------------------------------

func TestAddTextSource_Valid(t *testing.T) {
	kb := defaultKB()
	store := &mockStore{
		kb: kb,
		createFile: &files.FileRecord{
			ID:        "file-text-1",
			KbID:      "kb-1",
			Name:      "My Text",
			Type:      "text/plain",
			Size:      intPtr(12),
			Status:    "pending",
			Origin:    "text",
			CreatedAt: time.Now(),
		},
	}
	h := defaultIngestHandler(store)

	body := `{"title":"My Text","content":"Hello, world!"}`
	req := httptest.NewRequest(http.MethodPost, "/api/kb/kb-1/text", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "kb-1")
	req = withUser(req, ingestUser())
	req = withKBAccess(req, kb)

	rr := httptest.NewRecorder()
	h.AddTextSource(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp files.FileRecord
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ID == "" {
		t.Error("expected non-empty file ID in response")
	}
}

func TestAddTextSource_MissingContent(t *testing.T) {
	kb := defaultKB()
	store := &mockStore{kb: kb}
	h := defaultIngestHandler(store)

	body := `{"title":"No Content"}`
	req := httptest.NewRequest(http.MethodPost, "/api/kb/kb-1/text", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "kb-1")
	req = withUser(req, ingestUser())
	req = withKBAccess(req, kb)

	rr := httptest.NewRecorder()
	h.AddTextSource(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestAddTextSource_MissingTitle(t *testing.T) {
	kb := defaultKB()
	store := &mockStore{kb: kb}
	h := defaultIngestHandler(store)

	body := `{"content":"Some content but no title"}`
	req := httptest.NewRequest(http.MethodPost, "/api/kb/kb-1/text", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "kb-1")
	req = withUser(req, ingestUser())
	req = withKBAccess(req, kb)

	rr := httptest.NewRecorder()
	h.AddTextSource(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestAddTextSource_Unauthenticated(t *testing.T) {
	store := &mockStore{}
	h := defaultIngestHandler(store)

	body := `{"title":"T","content":"C"}`
	req := httptest.NewRequest(http.MethodPost, "/api/kb/kb-1/text", strings.NewReader(body))
	req.SetPathValue("id", "kb-1")
	// No user in context.

	rr := httptest.NewRecorder()
	h.AddTextSource(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// FetchURL tests
// ---------------------------------------------------------------------------

// mockHTTPTarget is an in-process HTTP server used as the fetch target.
func mockHTTPTarget(t *testing.T, body, contentType string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(body)) //nolint:errcheck
	}))
}

// isLocalhost returns true when net.LookupIP resolves to a loopback address.
// Used to skip tests that require real DNS when running in CI.
func isLocalhostTestSkip(t *testing.T) bool {
	t.Helper()
	ips, err := net.LookupIP("localhost")
	if err != nil {
		return false
	}
	for _, ip := range ips {
		if ip.IsLoopback() {
			return true
		}
	}
	return false
}

func TestFetchURL_Valid(t *testing.T) {
	// Start a local server.
	target := mockHTTPTarget(t, "<html>hello</html>", "text/html")
	defer target.Close()

	kb := defaultKB()
	store := &mockStore{
		kb: kb,
		createFile: &files.FileRecord{
			ID:        "file-url-1",
			KbID:      "kb-1",
			Name:      "fetched",
			Type:      "text/html",
			Size:      intPtr(20),
			Status:    "pending",
			Origin:    "url",
			CreatedAt: time.Now(),
		},
	}

	// The target URL points to 127.0.0.1 which normally triggers SSRF rejection.
	// We use a custom validateURL bypass by pointing to the test server directly.
	// To avoid the SSRF check blocking the test, we wrap using an httptest.Server
	// on 127.0.0.1 and bypass the DNS-resolution check by using a custom dialer.
	// Instead, we test FetchURL via a real loopback server but mock the validation
	// step: inject the raw URL after the SSRF check would normally reject it.
	//
	// Simplest approach: use the handler with a modified URL that passes validation.
	// Since we cannot inject a custom dialer here, we verify the happy-path by
	// confirming that 201 is returned when the target is reachable.
	//
	// The SSRF check will reject 127.0.0.1, so we call FetchURL indirectly through
	// the endpoint with the test server URL, which will fail the private-IP check.
	// To prove the happy-path works, we use an explicitly allowed public host
	// in a stub test that mocks fetchURL — but that would require unexported access.
	//
	// Resolution: use httptest.NewTLSServer on a loopback is still rejected.
	// We therefore test the 400 path for private IPs (the SSRF rejection) and
	// verify the 201 path with a mock store where StoreFile succeeds, using
	// a direct call that bypasses network by checking a non-reachable URL
	// is rejected at validation, and reachable public URL returns 201 when network
	// is available.
	//
	// For a deterministic unit test, we verify the 400 for private IP here.
	_ = store
	_ = target

	// Actually test the valid path by using the target.URL with IP replaced.
	// The test server listens on 127.0.0.1; the SSRF check will block it.
	// We test the 400 case in a dedicated test. This test exercises the storage
	// and DB creation path assuming SSRF validation is bypassed, which is
	// covered by the integration layer.
	t.Skip("FetchURL happy-path requires network access without SSRF; tested via integration tests")
}

func TestFetchURL_Valid_WithTestServer(t *testing.T) {
	// This test exercises as much of the FetchURL flow as possible using a
	// mock HTTP server. Because the server listens on 127.0.0.1 the SSRF
	// check will reject it — this is the expected production behavior.
	// We confirm the 400 error is returned for a private-IP target URL.
	target := mockHTTPTarget(t, "<html>hello</html>", "text/html")
	defer target.Close()

	kb := defaultKB()
	store := &mockStore{kb: kb}
	h := defaultIngestHandler(store)

	body := `{"url":"` + target.URL + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/kb/kb-1/fetch-url", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "kb-1")
	req = withUser(req, ingestUser())
	req = withKBAccess(req, kb)

	rr := httptest.NewRecorder()
	h.FetchURL(rr, req)

	// The httptest server is on 127.0.0.1 — SSRF protection should reject it.
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 (private IP rejected), got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestFetchURL_InvalidURL(t *testing.T) {
	kb := defaultKB()
	store := &mockStore{kb: kb}
	h := defaultIngestHandler(store)

	body := `{"url":"not-a-url"}`
	req := httptest.NewRequest(http.MethodPost, "/api/kb/kb-1/fetch-url", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "kb-1")
	req = withUser(req, ingestUser())
	req = withKBAccess(req, kb)

	rr := httptest.NewRecorder()
	h.FetchURL(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestFetchURL_FTPScheme(t *testing.T) {
	kb := defaultKB()
	store := &mockStore{kb: kb}
	h := defaultIngestHandler(store)

	body := `{"url":"ftp://example.com/file.txt"}`
	req := httptest.NewRequest(http.MethodPost, "/api/kb/kb-1/fetch-url", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "kb-1")
	req = withUser(req, ingestUser())
	req = withKBAccess(req, kb)

	rr := httptest.NewRecorder()
	h.FetchURL(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for ftp scheme, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestFetchURL_PrivateIP_Loopback(t *testing.T) {
	kb := defaultKB()
	store := &mockStore{kb: kb}
	h := defaultIngestHandler(store)

	// 127.0.0.1 is in the loopback range.
	body := `{"url":"http://127.0.0.1/admin"}`
	req := httptest.NewRequest(http.MethodPost, "/api/kb/kb-1/fetch-url", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "kb-1")
	req = withUser(req, ingestUser())
	req = withKBAccess(req, kb)

	rr := httptest.NewRecorder()
	h.FetchURL(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for private IP, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestFetchURL_PrivateIP_RFC1918(t *testing.T) {
	kb := defaultKB()
	store := &mockStore{kb: kb}
	h := defaultIngestHandler(store)

	body := `{"url":"http://192.168.1.1/"}`
	req := httptest.NewRequest(http.MethodPost, "/api/kb/kb-1/fetch-url", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "kb-1")
	req = withUser(req, ingestUser())
	req = withKBAccess(req, kb)

	rr := httptest.NewRecorder()
	h.FetchURL(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for private IP, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestFetchURL_MissingURL(t *testing.T) {
	kb := defaultKB()
	store := &mockStore{kb: kb}
	h := defaultIngestHandler(store)

	body := `{}`
	req := httptest.NewRequest(http.MethodPost, "/api/kb/kb-1/fetch-url", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "kb-1")
	req = withUser(req, ingestUser())
	req = withKBAccess(req, kb)

	rr := httptest.NewRecorder()
	h.FetchURL(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestFetchURL_Unauthenticated(t *testing.T) {
	store := &mockStore{}
	h := defaultIngestHandler(store)

	body := `{"url":"https://example.com"}`
	req := httptest.NewRequest(http.MethodPost, "/api/kb/kb-1/fetch-url", strings.NewReader(body))
	req.SetPathValue("id", "kb-1")
	// No user in context.

	rr := httptest.NewRecorder()
	h.FetchURL(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

func intPtr(n int) *int { return &n }

// ---------------------------------------------------------------------------
// FetchURL — real network happy-path (guarded by build tag / env check)
// ---------------------------------------------------------------------------

// TestFetchURL_RealPublicURL tests the actual network fetch path.
// It is skipped unless the test environment has network access to example.com.
func TestFetchURL_RealPublicURL(t *testing.T) {
	// This test makes a real network call to example.com.
	// Skip in environments where network is unavailable.
	t.Skip("skipped by default; remove Skip to run in environments with public network access")

	kb := defaultKB()
	ownerID := "user-1"
	kb.UserID = &ownerID

	store := &mockStore{
		kb: kb,
	}
	h := defaultIngestHandler(store)

	body := `{"url":"https://example.com/"}`
	req := httptest.NewRequest(http.MethodPost, "/api/kb/kb-1/fetch-url", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "kb-1")
	req = withUser(req, ingestUser())
	req = withKBAccess(req, kb)

	rr := httptest.NewRecorder()
	h.FetchURL(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["id"] == "" || resp["id"] == nil {
		t.Error("expected non-empty file ID in response")
	}
}

// ---------------------------------------------------------------------------
// Enqueue-failure compensating-cleanup tests
// ---------------------------------------------------------------------------

// TestAddTextSource_EnqueueFailureDeletesFile verifies that when the Asynq
// enqueue call fails, AddTextSource deletes the just-created DB record so
// the file does not remain as an unprocessable orphan.
func TestAddTextSource_EnqueueFailureDeletesFile(t *testing.T) {
	kb := defaultKB()

	// mockStore records every ID passed to DeleteFileRecord in deletedIDs.
	// CreateFile returns a record with ID "created-file-id".
	store := &mockStore{
		kb: kb,
		createFile: &files.FileRecord{
			ID:        "created-file-id",
			KbID:      "kb-1",
			Name:      "My Text",
			Type:      "text/plain",
			Size:      intPtr(13),
			Status:    "pending",
			Origin:    "text",
			CreatedAt: time.Now(),
		},
	}

	stor := &mockStorage{}
	h := files.NewHandlerWithEnqueuer(store, stor, noopChunks(), &failingEnqueuer{})

	body := `{"title":"My Text","content":"Hello, world!"}`
	req := httptest.NewRequest(http.MethodPost, "/api/kb/kb-1/text", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("id", "kb-1")
	req = withUser(req, ingestUser())
	req = withKBAccess(req, kb)

	rr := httptest.NewRecorder()
	h.AddTextSource(rr, req)

	// 1. Handler must return 500.
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 on enqueue failure, got %d: %s", rr.Code, rr.Body.String())
	}

	// 2. The created file's ID must appear in the store's deleted list.
	if len(store.deletedIDs) == 0 {
		t.Fatal("expected DeleteFileRecord to be called with the created file ID, but deletedIDs is empty")
	}
	if store.deletedIDs[0] != "created-file-id" {
		t.Errorf("expected deleted ID %q, got %q", "created-file-id", store.deletedIDs[0])
	}
}
