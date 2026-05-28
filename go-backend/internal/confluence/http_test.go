package confluence_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/confluence"
	"github.com/justrag/go-backend/internal/kbaccess"
)

// ---------------------------------------------------------------------------
// Mock store
// ---------------------------------------------------------------------------

var _ confluence.ConfluenceStore = (*mockStore)(nil)

type mockStore struct {
	conn             *confluence.ConfluenceConnectionRow
	connErr          error
	createdConn      *confluence.ConfluenceConnectionRow
	createConnErr    error
	updatedConn      *confluence.ConfluenceConnectionRow
	updateConnErr    error
	lastConnUpdate   *confluence.ConfluenceConnectionUpdate
	deleteErr        error
	source           *confluence.ConfluenceSourceRow
	sources          []confluence.ConfluenceSourceRow
	sourceErr        error
	updatedSrc       *confluence.ConfluenceSourceRow
	siteConfigs      map[string]*string
}

func (m *mockStore) GetConfluenceConnectionByUserID(_ context.Context, _ string) (*confluence.ConfluenceConnectionRow, error) {
	return m.conn, m.connErr
}

func (m *mockStore) DeleteConfluenceConnection(_ context.Context, _ string) error {
	return m.deleteErr
}

func (m *mockStore) CreateConfluenceConnection(_ context.Context, _, _ string, _ *string) (*confluence.ConfluenceConnectionRow, error) {
	return m.createdConn, m.createConnErr
}

func (m *mockStore) UpdateConfluenceConnection(_ context.Context, _ string, u confluence.ConfluenceConnectionUpdate) (*confluence.ConfluenceConnectionRow, error) {
	uCopy := u
	m.lastConnUpdate = &uCopy
	return m.updatedConn, m.updateConnErr
}

func (m *mockStore) CreateConfluenceSource(_ context.Context, kbID, connectionID, spaceKey string, rootPageID, rootPageTitle *string, includeAttachments bool, syncInterval *int) (*confluence.ConfluenceSourceRow, error) {
	if m.sourceErr != nil {
		return nil, m.sourceErr
	}
	return m.source, nil
}

func (m *mockStore) ListConfluenceSources(_ context.Context, _ string) ([]confluence.ConfluenceSourceRow, error) {
	if m.sourceErr != nil {
		return nil, m.sourceErr
	}
	return m.sources, nil
}

func (m *mockStore) GetConfluenceSourceByID(_ context.Context, _ string) (*confluence.ConfluenceSourceRow, error) {
	if m.sourceErr != nil {
		return nil, m.sourceErr
	}
	return m.source, nil
}

func (m *mockStore) UpdateConfluenceSource(_ context.Context, _ string, _ confluence.ConfluenceSourceUpdate) (*confluence.ConfluenceSourceRow, error) {
	if m.sourceErr != nil {
		return nil, m.sourceErr
	}
	return m.updatedSrc, nil
}

func (m *mockStore) DeleteConfluenceSource(_ context.Context, _ string) error {
	return m.deleteErr
}

func (m *mockStore) GetSiteConfigValue(_ context.Context, key string) (*string, error) {
	if m.siteConfigs == nil {
		return nil, nil
	}
	v, ok := m.siteConfigs[key]
	if !ok {
		return nil, nil
	}
	return v, nil
}

func (m *mockStore) ListActiveConfluenceSources(_ context.Context) ([]confluence.ConfluenceSourceRow, error) {
	if m.sourceErr != nil {
		return nil, m.sourceErr
	}
	return m.sources, nil
}

func (m *mockStore) GetConfluenceConnectionByID(_ context.Context, _ string) (*confluence.ConfluenceConnectionRow, error) {
	return m.conn, m.connErr
}

func (m *mockStore) CreateConfluenceFile(_ context.Context, _ confluence.CreateConfluenceFileData) (*confluence.ConfluenceFileRow, error) {
	return nil, nil
}

func (m *mockStore) GetFilesByConfluenceSourceID(_ context.Context, _ string) ([]confluence.ConfluenceFileRow, error) {
	return nil, nil
}

func (m *mockStore) GetConfluenceSourceIDForFile(_ context.Context, _ string) (string, error) {
	return "", nil
}

func (m *mockStore) DeleteFilesByIDs(_ context.Context, _ []string) error {
	return nil
}

func (m *mockStore) GetConfluenceSourceFileProgress(_ context.Context, _ string) (int, int, error) {
	return 0, 0, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const (
	testKBID      = "kb-111"
	testConnID    = "conn-222"
	testSourceID  = "src-333"
	testUserID    = "user-444"
	testJWTSecret = "test-secret-that-is-long-enough-for-aes256"
)

func strPtr(s string) *string { return &s }
func boolPtr(b bool) *bool    { return &b }
func intPtr(i int) *int       { return &i }

// makeEncryptedConn creates a ConfluenceConnectionRow with a real encrypted token.
func makeEncryptedConn(t *testing.T) *confluence.ConfluenceConnectionRow {
	t.Helper()
	encrypted, err := confluence.EncryptToken("my-api-token-1234", testJWTSecret)
	if err != nil {
		t.Fatalf("EncryptToken: %v", err)
	}
	return &confluence.ConfluenceConnectionRow{
		ID:          testConnID,
		UserID:      testUserID,
		DisplayName: strPtr("Alice"),
		Token:       encrypted,
		Status:      "active",
		CreatedAt:   time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func makeSource() *confluence.ConfluenceSourceRow {
	return &confluence.ConfluenceSourceRow{
		ID:                  testSourceID,
		KbID:                testKBID,
		ConnectionID:        testConnID,
		SpaceKey:            "ENG",
		IncludeAttachments:  false,
		Status:              "active",
		ConsecutiveFailures: 0,
		PageCount:           0,
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

// withUser injects auth.Claims into the request context.
func withUser(r *http.Request, userID string) *http.Request {
	claims := &auth.Claims{ID: userID, Username: "alice", Role: "user"}
	ctx := auth.WithUser(r.Context(), claims)
	return r.WithContext(ctx)
}

// withKBAccess injects a KBAccessResult into the request context.
func withKBAccess(r *http.Request, kbID string) *http.Request {
	kb := &kbaccess.KnowledgeBase{ID: kbID}
	result := &kbaccess.KBAccessResult{KB: kb, Permission: "edit"}
	return r.WithContext(kbaccess.WithAccess(r.Context(), result))
}

// serveSourceID sets PathValue("sourceId") on the request via a thin ServeMux.
func serveSourceID(sourceID string, handler http.HandlerFunc, r *http.Request) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	pattern := r.Method + " /api/kb/{id}/confluence-sources/{sourceId}"
	r.URL.Path = "/api/kb/" + testKBID + "/confluence-sources/" + sourceID
	mux.HandleFunc(pattern, handler)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, r)
	return rr
}

// serveConnID sets PathValue("id") on the DELETE connection request.
func serveConnID(connID string, handler http.HandlerFunc, r *http.Request) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	pattern := r.Method + " /api/confluence/connections/{id}"
	r.URL.Path = "/api/confluence/connections/" + connID
	mux.HandleFunc(pattern, handler)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, r)
	return rr
}

// defaultSiteConfigs returns site config values with confluence enabled.
func defaultSiteConfigs() map[string]*string {
	return map[string]*string{
		"confluence_enabled":  strPtr("true"),
		"confluence_base_url": strPtr("https://example.atlassian.net"),
	}
}

// ---------------------------------------------------------------------------
// Tests: GetConnection
// ---------------------------------------------------------------------------

// Test 1: GetConnection with no connection → null
func TestGetConnection_NoConnection(t *testing.T) {
	store := &mockStore{
		conn:        nil,
		siteConfigs: defaultSiteConfigs(),
	}
	h := confluence.NewHandler(store, testJWTSecret)

	req := withUser(newRequest(http.MethodGet, "/api/confluence/connections", nil), testUserID)
	rr := httptest.NewRecorder()
	h.GetConnection(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var got struct {
		Connection any     `json:"connection"`
		BaseURL    *string `json:"baseUrl"`
		Enabled    bool    `json:"enabled"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Connection != nil {
		t.Errorf("expected connection=null, got %v", got.Connection)
	}
	if !got.Enabled {
		t.Errorf("expected enabled=true")
	}
	if got.BaseURL == nil || *got.BaseURL != "https://example.atlassian.net" {
		t.Errorf("expected baseUrl, got %v", got.BaseURL)
	}
}

// Test 2: GetConnection with connection → masked token
func TestGetConnection_WithConnection(t *testing.T) {
	conn := makeEncryptedConn(t)
	store := &mockStore{
		conn:        conn,
		siteConfigs: defaultSiteConfigs(),
	}
	h := confluence.NewHandler(store, testJWTSecret)

	req := withUser(newRequest(http.MethodGet, "/api/confluence/connections", nil), testUserID)
	rr := httptest.NewRecorder()
	h.GetConnection(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var got struct {
		Connection struct {
			ID     string `json:"id"`
			Token  string `json:"token"`
			Status string `json:"status"`
		} `json:"connection"`
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Connection.ID != testConnID {
		t.Errorf("expected id=%q, got %q", testConnID, got.Connection.ID)
	}
	// Token must be masked (contain bullet characters, not the raw value).
	if got.Connection.Token == "my-api-token-1234" {
		t.Errorf("token must be masked, not the plaintext value")
	}
	if got.Connection.Token == "" {
		t.Errorf("token must not be empty")
	}
	// Masked token should end with the last 4 chars of the plaintext.
	if len(got.Connection.Token) < 4 {
		t.Errorf("masked token too short: %q", got.Connection.Token)
	}
	if got.Connection.Status != "active" {
		t.Errorf("expected status=active, got %q", got.Connection.Status)
	}
}

// ---------------------------------------------------------------------------
// Tests: DeleteConnection
// ---------------------------------------------------------------------------

// Test 3: DeleteConnection → 204
func TestDeleteConnection_OK(t *testing.T) {
	conn := makeEncryptedConn(t)
	store := &mockStore{conn: conn}
	h := confluence.NewHandler(store, testJWTSecret)

	req := withUser(newRequest(http.MethodDelete, "/api/confluence/connections/"+testConnID, nil), testUserID)
	rr := serveConnID(testConnID, h.DeleteConnection, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rr.Code, rr.Body.String())
	}
}

// Test: DeleteConnection for connection that doesn't belong to user → 404
func TestDeleteConnection_NotFound(t *testing.T) {
	store := &mockStore{conn: nil}
	h := confluence.NewHandler(store, testJWTSecret)

	req := withUser(newRequest(http.MethodDelete, "/api/confluence/connections/"+testConnID, nil), testUserID)
	rr := serveConnID(testConnID, h.DeleteConnection, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rr.Code, rr.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Tests: ListSources
// ---------------------------------------------------------------------------

// Test 4: ListSources → 200 + array
func TestListSources_OK(t *testing.T) {
	source := makeSource()
	store := &mockStore{sources: []confluence.ConfluenceSourceRow{*source}}
	h := confluence.NewHandler(store, testJWTSecret)

	req := withKBAccess(newRequest(http.MethodGet, "/api/kb/"+testKBID+"/confluence-sources", nil), testKBID)
	rr := httptest.NewRecorder()
	h.ListSources(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var got []confluence.ConfluenceSourceRow
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 source, got %d", len(got))
	}
	if got[0].ID != testSourceID {
		t.Errorf("expected source ID %q, got %q", testSourceID, got[0].ID)
	}
}

// Test: ListSources empty → 200 + empty array (not null)
func TestListSources_Empty(t *testing.T) {
	store := &mockStore{sources: []confluence.ConfluenceSourceRow{}}
	h := confluence.NewHandler(store, testJWTSecret)

	req := withKBAccess(newRequest(http.MethodGet, "/api/kb/"+testKBID+"/confluence-sources", nil), testKBID)
	rr := httptest.NewRecorder()
	h.ListSources(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var got []confluence.ConfluenceSourceRow
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 sources, got %d", len(got))
	}
}

// ---------------------------------------------------------------------------
// Tests: CreateSource
// ---------------------------------------------------------------------------

// Test 5: CreateSource → 201
func TestCreateSource_Valid(t *testing.T) {
	source := makeSource()
	store := &mockStore{source: source}
	h := confluence.NewHandler(store, testJWTSecret)

	body := map[string]any{
		"connectionId": testConnID,
		"spaceKey":     "ENG",
	}
	req := withKBAccess(newRequest(http.MethodPost, "/api/kb/"+testKBID+"/confluence-sources", body), testKBID)
	rr := httptest.NewRecorder()
	h.CreateSource(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}

	var got confluence.ConfluenceSourceRow
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.ID != testSourceID {
		t.Errorf("expected source ID %q, got %q", testSourceID, got.ID)
	}
}

func TestCreateSource_MissingConnectionID(t *testing.T) {
	store := &mockStore{}
	h := confluence.NewHandler(store, testJWTSecret)

	body := map[string]any{"spaceKey": "ENG"} // no connectionId
	req := withKBAccess(newRequest(http.MethodPost, "/api/kb/"+testKBID+"/confluence-sources", body), testKBID)
	rr := httptest.NewRecorder()
	h.CreateSource(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestCreateSource_MissingSpaceKey(t *testing.T) {
	store := &mockStore{}
	h := confluence.NewHandler(store, testJWTSecret)

	body := map[string]any{"connectionId": testConnID} // no spaceKey
	req := withKBAccess(newRequest(http.MethodPost, "/api/kb/"+testKBID+"/confluence-sources", body), testKBID)
	rr := httptest.NewRecorder()
	h.CreateSource(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Tests: DeleteSource
// ---------------------------------------------------------------------------

// Test 6: DeleteSource → 204
func TestDeleteSource_OK(t *testing.T) {
	source := makeSource()
	store := &mockStore{source: source}
	h := confluence.NewHandler(store, testJWTSecret)

	req := withKBAccess(
		newRequest(http.MethodDelete, "/api/kb/"+testKBID+"/confluence-sources/"+testSourceID, nil),
		testKBID,
	)
	rr := serveSourceID(testSourceID, h.DeleteSource, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rr.Code, rr.Body.String())
	}
}

// Test: DeleteSource for source belonging to different KB → 404
func TestDeleteSource_WrongKB(t *testing.T) {
	source := makeSource()
	source.KbID = "other-kb"
	store := &mockStore{source: source}
	h := confluence.NewHandler(store, testJWTSecret)

	req := withKBAccess(
		newRequest(http.MethodDelete, "/api/kb/"+testKBID+"/confluence-sources/"+testSourceID, nil),
		testKBID,
	)
	rr := serveSourceID(testSourceID, h.DeleteSource, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rr.Code, rr.Body.String())
	}
}

// Test: DeleteSource when source not found → 404
func TestDeleteSource_NotFound(t *testing.T) {
	store := &mockStore{source: nil}
	h := confluence.NewHandler(store, testJWTSecret)

	req := withKBAccess(
		newRequest(http.MethodDelete, "/api/kb/"+testKBID+"/confluence-sources/"+testSourceID, nil),
		testKBID,
	)
	rr := serveSourceID(testSourceID, h.DeleteSource, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rr.Code, rr.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Helpers for new endpoint tests
// ---------------------------------------------------------------------------

// serveConfluenceConnID wraps a handler in a mux that populates PathValue("id").
func serveConfluenceConnID(connID string, method string, handler http.HandlerFunc, r *http.Request) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	pattern := method + " /api/confluence/connections/{id}"
	r.URL.Path = "/api/confluence/connections/" + connID
	mux.HandleFunc(pattern, handler)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, r)
	return rr
}

// serveVerifyConn wraps the verify handler to populate both the {id} segment
// and the extra /verify suffix.
func serveVerifyConn(connID string, handler http.HandlerFunc, r *http.Request) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	pattern := "POST /api/confluence/connections/{id}/verify"
	r.URL.Path = "/api/confluence/connections/" + connID + "/verify"
	mux.HandleFunc(pattern, handler)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, r)
	return rr
}

// ---------------------------------------------------------------------------
// Tests: CreateConnection
// ---------------------------------------------------------------------------

// TestCreateConnection_OK mocks the Confluence API verify call and checks 201.
func TestCreateConnection_OK(t *testing.T) {
	// Spin up a fake Confluence API.
	fakeCF := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/api/user/current" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"username":"alice"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer fakeCF.Close()

	createdConn := makeEncryptedConn(t)
	createdConn.DisplayName = strPtr("My Confluence")

	store := &mockStore{
		conn:        nil, // no existing connection
		createdConn: createdConn,
		siteConfigs: map[string]*string{
			"confluence_enabled":  strPtr("true"),
			"confluence_base_url": strPtr(fakeCF.URL),
		},
	}
	h := confluence.NewHandler(store, testJWTSecret)

	body := map[string]any{
		"token":       "my-pat-token",
		"displayName": "My Confluence",
	}
	req := withUser(newRequest(http.MethodPost, "/api/confluence/connections", body), testUserID)
	rr := httptest.NewRecorder()
	h.CreateConnection(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}

	var got struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Token  string `json:"token"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.ID != testConnID {
		t.Errorf("expected id=%q, got %q", testConnID, got.ID)
	}
	if got.Status != "active" {
		t.Errorf("expected status=active, got %q", got.Status)
	}
}

// TestCreateConnection_TokenVerifyFails returns 400 when Confluence API rejects the token.
func TestCreateConnection_TokenVerifyFails(t *testing.T) {
	fakeCF := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	}))
	defer fakeCF.Close()

	store := &mockStore{
		conn: nil,
		siteConfigs: map[string]*string{
			"confluence_enabled":  strPtr("true"),
			"confluence_base_url": strPtr(fakeCF.URL),
		},
	}
	h := confluence.NewHandler(store, testJWTSecret)

	body := map[string]any{"token": "bad-token"}
	req := withUser(newRequest(http.MethodPost, "/api/confluence/connections", body), testUserID)
	rr := httptest.NewRecorder()
	h.CreateConnection(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestCreateConnection_DuplicateConnection returns 400 when connection already exists.
func TestCreateConnection_DuplicateConnection(t *testing.T) {
	fakeCF := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"username":"alice"}`))
	}))
	defer fakeCF.Close()

	store := &mockStore{
		conn: makeEncryptedConn(t), // existing connection
		siteConfigs: map[string]*string{
			"confluence_enabled":  strPtr("true"),
			"confluence_base_url": strPtr(fakeCF.URL),
		},
	}
	h := confluence.NewHandler(store, testJWTSecret)

	body := map[string]any{"token": "new-token"}
	req := withUser(newRequest(http.MethodPost, "/api/confluence/connections", body), testUserID)
	rr := httptest.NewRecorder()
	h.CreateConnection(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Tests: VerifyConnection
// ---------------------------------------------------------------------------

// TestVerifyConnection_OK returns ok:true when the token is valid.
func TestVerifyConnection_OK(t *testing.T) {
	fakeCF := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/api/user/current" {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"username":"alice"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer fakeCF.Close()

	conn := makeEncryptedConn(t)
	store := &mockStore{
		conn:        conn,
		updatedConn: conn,
		siteConfigs: map[string]*string{
			"confluence_enabled":  strPtr("true"),
			"confluence_base_url": strPtr(fakeCF.URL),
		},
	}
	h := confluence.NewHandler(store, testJWTSecret)

	req := withUser(newRequest(http.MethodPost, "/api/confluence/connections/"+testConnID+"/verify", nil), testUserID)
	rr := serveVerifyConn(testConnID, h.VerifyConnection, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var got map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if ok, _ := got["ok"].(bool); !ok {
		t.Errorf("expected ok=true, got %v", got)
	}
}

// TestVerifyConnection_Fails returns ok:false when the Confluence API rejects the token.
func TestVerifyConnection_Fails(t *testing.T) {
	fakeCF := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	}))
	defer fakeCF.Close()

	conn := makeEncryptedConn(t)
	store := &mockStore{
		conn:        conn,
		updatedConn: conn,
		siteConfigs: map[string]*string{
			"confluence_enabled":  strPtr("true"),
			"confluence_base_url": strPtr(fakeCF.URL),
		},
	}
	h := confluence.NewHandler(store, testJWTSecret)

	req := withUser(newRequest(http.MethodPost, "/api/confluence/connections/"+testConnID+"/verify", nil), testUserID)
	rr := serveVerifyConn(testConnID, h.VerifyConnection, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var got map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if ok, _ := got["ok"].(bool); ok {
		t.Errorf("expected ok=false")
	}
	if _, hasErr := got["error"]; !hasErr {
		t.Errorf("expected error field in response")
	}
}

// ---------------------------------------------------------------------------
// Tests: ListSpaces
// ---------------------------------------------------------------------------

// TestListSpaces_OK calls GET /api/confluence/spaces and checks the returned array.
func TestListSpaces_OK(t *testing.T) {
	fakeCF := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/api/space" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"results":[{"key":"ENG","name":"Engineering"},{"key":"OPS","name":"Operations"}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer fakeCF.Close()

	conn := makeEncryptedConn(t)
	store := &mockStore{
		conn: conn,
		siteConfigs: map[string]*string{
			"confluence_enabled":  strPtr("true"),
			"confluence_base_url": strPtr(fakeCF.URL),
		},
	}
	h := confluence.NewHandler(store, testJWTSecret)

	req := withUser(newRequest(http.MethodGet, "/api/confluence/spaces", nil), testUserID)
	rr := httptest.NewRecorder()
	h.ListSpaces(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var got []map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 spaces, got %d", len(got))
	}
	if got[0]["key"] != "ENG" {
		t.Errorf("expected first space key=ENG, got %v", got[0]["key"])
	}
}

// TestListSpaces_AuthFailureMarksConnection covers the failsafe path: when the
// Confluence API rejects the stored token with 401, the handler returns 401
// to the caller (so the frontend can switch to a reconnect UI) AND flips the
// connection's stored status to 'error' (so the Profile page no longer shows
// a green checkmark for a broken token).
func TestListSpaces_AuthFailureMarksConnection(t *testing.T) {
	fakeCF := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	}))
	defer fakeCF.Close()

	conn := makeEncryptedConn(t)
	store := &mockStore{
		conn:        conn,
		updatedConn: conn,
		siteConfigs: map[string]*string{
			"confluence_enabled":  strPtr("true"),
			"confluence_base_url": strPtr(fakeCF.URL),
		},
	}
	h := confluence.NewHandler(store, testJWTSecret)

	req := withUser(newRequest(http.MethodGet, "/api/confluence/spaces", nil), testUserID)
	rr := httptest.NewRecorder()
	h.ListSpaces(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rr.Code, rr.Body.String())
	}
	if store.lastConnUpdate == nil {
		t.Fatal("expected UpdateConfluenceConnection to be called after auth failure")
	}
	if store.lastConnUpdate.Status == nil || *store.lastConnUpdate.Status != "error" {
		t.Errorf("expected connection status to be set to 'error', got %v", store.lastConnUpdate.Status)
	}
	if store.lastConnUpdate.ErrorMessage == nil || *store.lastConnUpdate.ErrorMessage == "" {
		t.Errorf("expected non-empty error message on auth failure")
	}
}

// TestListSpaces_NoConnection returns 400 when the user has no connection.
func TestListSpaces_NoConnection(t *testing.T) {
	store := &mockStore{
		conn:        nil,
		siteConfigs: defaultSiteConfigs(),
	}
	h := confluence.NewHandler(store, testJWTSecret)

	req := withUser(newRequest(http.MethodGet, "/api/confluence/spaces", nil), testUserID)
	rr := httptest.NewRecorder()
	h.ListSpaces(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestListAllSpacePages_OK(t *testing.T) {
	fakeCF := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/rest/api/content") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{
			"results":[
				{"id":"10","title":"Onboarding","ancestors":[{"id":"1","title":"Engineering"},{"id":"5","title":"Team"}]}
			]
		}`))
	}))
	defer fakeCF.Close()

	conn := makeEncryptedConn(t)
	store := &mockStore{
		conn: conn,
		siteConfigs: map[string]*string{
			"confluence_enabled":  strPtr("true"),
			"confluence_base_url": strPtr(fakeCF.URL),
		},
	}
	h := confluence.NewHandler(store, testJWTSecret)

	req := withUser(newRequest(http.MethodGet, "/api/confluence/spaces/ENG/pages/all", nil), testUserID)
	req.SetPathValue("spaceKey", "ENG")
	rr := httptest.NewRecorder()
	h.ListAllSpacePages(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var got []confluence.ConfluencePageWithPath
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 page, got %d", len(got))
	}
	if got[0].ID != "10" || got[0].Title != "Onboarding" {
		t.Errorf("unexpected page: %+v", got[0])
	}
	want := []string{"Engineering", "Team"}
	if len(got[0].AncestorTitles) != 2 || got[0].AncestorTitles[0] != want[0] || got[0].AncestorTitles[1] != want[1] {
		t.Errorf("ancestor titles: got %v want %v", got[0].AncestorTitles, want)
	}
}

func TestListAllSpacePages_MissingSpaceKey(t *testing.T) {
	store := &mockStore{
		conn:        makeEncryptedConn(t),
		siteConfigs: defaultSiteConfigs(),
	}
	h := confluence.NewHandler(store, testJWTSecret)

	req := withUser(newRequest(http.MethodGet, "/api/confluence/spaces//pages/all", nil), testUserID)
	rr := httptest.NewRecorder()
	h.ListAllSpacePages(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestListAllSpacePages_EmptyReturnsArrayNotNull(t *testing.T) {
	fakeCF := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/rest/api/content") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"results":[]}`))
	}))
	defer fakeCF.Close()

	store := &mockStore{
		conn: makeEncryptedConn(t),
		siteConfigs: map[string]*string{
			"confluence_enabled":  strPtr("true"),
			"confluence_base_url": strPtr(fakeCF.URL),
		},
	}
	h := confluence.NewHandler(store, testJWTSecret)

	req := withUser(newRequest(http.MethodGet, "/api/confluence/spaces/ENG/pages/all", nil), testUserID)
	req.SetPathValue("spaceKey", "ENG")
	rr := httptest.NewRecorder()
	h.ListAllSpacePages(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	// Trim trailing newline that httputil.WriteJSON typically appends.
	body = strings.TrimRight(body, "\n")
	if body != "[]" {
		t.Errorf("expected response body %q, got %q", "[]", body)
	}
}
