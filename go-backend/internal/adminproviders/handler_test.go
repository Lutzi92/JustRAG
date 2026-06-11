package adminproviders_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/adminproviders"
)

// ---------------------------------------------------------------------------
// Mock store
// ---------------------------------------------------------------------------

type mockStore struct {
	listed  []adminproviders.AuthProviderRow
	fetched *adminproviders.AuthProviderRow
	created *adminproviders.AuthProviderRow
	updated *adminproviders.AuthProviderRow
	err     error

	// lastCreate / lastUpdate capture what the handler asked the store to
	// persist so tests can assert on the (post-encryption) config.
	lastCreate *adminproviders.AuthProviderCreate
	lastUpdate *adminproviders.AuthProviderUpdate
}

var _ adminproviders.Store = (*mockStore)(nil)

func (m *mockStore) ListAuthProviders(_ context.Context) ([]adminproviders.AuthProviderRow, error) {
	return m.listed, m.err
}

func (m *mockStore) GetAuthProvider(_ context.Context, _ string) (*adminproviders.AuthProviderRow, error) {
	return m.fetched, m.err
}

func (m *mockStore) CreateAuthProvider(_ context.Context, data adminproviders.AuthProviderCreate) (*adminproviders.AuthProviderRow, error) {
	m.lastCreate = &data
	return m.created, m.err
}

func (m *mockStore) UpdateAuthProvider(_ context.Context, _ string, data adminproviders.AuthProviderUpdate) (*adminproviders.AuthProviderRow, error) {
	m.lastUpdate = &data
	return m.updated, m.err
}

func (m *mockStore) DeleteAuthProvider(_ context.Context, _ string) error {
	return m.err
}

func (m *mockStore) LogAuditAction(_ context.Context, _, _, _, _ string, _ any) error {
	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func makeProvider(id, typ, name string) adminproviders.AuthProviderRow {
	return adminproviders.AuthProviderRow{
		ID:        id,
		Type:      typ,
		Name:      name,
		Config:    json.RawMessage(`{"host":"ldap.example.com"}`),
		IsActive:  true,
		CreatedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

// ---------------------------------------------------------------------------
// Tests: ListAuthProviders
// ---------------------------------------------------------------------------

func TestListAuthProviders_ReturnsArray(t *testing.T) {
	provider := makeProvider("provider-1", "ldap", "Corporate LDAP")
	store := &mockStore{listed: []adminproviders.AuthProviderRow{provider}}
	h := adminproviders.NewHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/auth-providers", nil)
	rr := httptest.NewRecorder()

	h.ListAuthProviders(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var body []map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(body) != 1 {
		t.Fatalf("expected 1 provider, got %d", len(body))
	}
	if body[0]["id"] != "provider-1" {
		t.Errorf("expected id=provider-1, got %v", body[0]["id"])
	}
	if body[0]["type"] != "ldap" {
		t.Errorf("expected type=ldap, got %v", body[0]["type"])
	}
}

// ---------------------------------------------------------------------------
// Tests: CreateAuthProvider
// ---------------------------------------------------------------------------

func TestCreateAuthProvider_Valid_Returns201(t *testing.T) {
	provider := makeProvider("new-id", "oauth2", "Google OAuth")
	store := &mockStore{created: &provider}
	h := adminproviders.NewHandler(store)

	bodyJSON := `{"type":"oauth2","name":"Google OAuth","config":{"client_id":"abc123"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/admin/auth-providers", bytes.NewBufferString(bodyJSON))
	rr := httptest.NewRecorder()

	h.CreateAuthProvider(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if body["id"] != "new-id" {
		t.Errorf("expected id=new-id, got %v", body["id"])
	}
}

func TestCreateAuthProvider_MissingName_Returns400(t *testing.T) {
	store := &mockStore{}
	h := adminproviders.NewHandler(store)

	bodyJSON := `{"type":"ldap","config":{"host":"ldap.example.com"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/admin/auth-providers", bytes.NewBufferString(bodyJSON))
	rr := httptest.NewRecorder()

	h.CreateAuthProvider(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if body["error"] == nil {
		t.Error("expected error field in response")
	}
}

func TestCreateAuthProvider_InvalidType_Returns400(t *testing.T) {
	store := &mockStore{}
	h := adminproviders.NewHandler(store)

	bodyJSON := `{"type":"unknown","name":"Test","config":{"host":"example.com"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/admin/auth-providers", bytes.NewBufferString(bodyJSON))
	rr := httptest.NewRecorder()

	h.CreateAuthProvider(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if body["error"] == nil {
		t.Error("expected error field in response")
	}
}

// ---------------------------------------------------------------------------
// Tests: LDAP bindCredentials encryption (M2)
// ---------------------------------------------------------------------------

// fakeWrap is a stand-in for the AES secret wrapper. The real round-trip is
// covered by authhandler/secrets_test.go; here we only need to observe that
// the handler routes the value through the wrapper.
func fakeWrap(plaintext string) (string, error) { return "enc:" + plaintext, nil }

// bindCreds pulls the bindCredentials field out of a stored config blob.
func bindCreds(t *testing.T, cfg json.RawMessage) (string, bool) {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(cfg, &m); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	v, ok := m["bindCredentials"].(string)
	return v, ok
}

func TestCreateLDAPProvider_EncryptsBindCredentials(t *testing.T) {
	provider := makeProvider("ldap-1", "ldap", "Corp LDAP")
	store := &mockStore{created: &provider}
	h := adminproviders.NewHandler(store).WithOIDCHooks(fakeWrap, nil, nil)

	bodyJSON := `{"type":"ldap","name":"Corp LDAP","config":{"url":"ldap://x","bindCredentials":"s3cr3t"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/admin/auth-providers", bytes.NewBufferString(bodyJSON))
	rr := httptest.NewRecorder()

	h.CreateAuthProvider(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}
	if store.lastCreate == nil {
		t.Fatal("store.CreateAuthProvider was not called")
	}
	got, ok := bindCreds(t, store.lastCreate.Config)
	if !ok {
		t.Fatal("bindCredentials missing from stored config")
	}
	if got != "enc:s3cr3t" {
		t.Errorf("expected encrypted bindCredentials, got %q", got)
	}
}

func TestUpdateLDAPProvider_PreservesMaskedBindCredentials(t *testing.T) {
	existing := makeProvider("ldap-1", "ldap", "Corp LDAP")
	existing.Config = json.RawMessage(`{"url":"ldap://x","bindCredentials":"enc:original"}`)
	updated := existing
	store := &mockStore{fetched: &existing, updated: &updated}
	h := adminproviders.NewHandler(store).WithOIDCHooks(fakeWrap, nil, nil)

	// Admin edits the URL but leaves the masked password untouched.
	bodyJSON := `{"type":"ldap","config":{"url":"ldap://y","bindCredentials":"********"}}`
	req := httptest.NewRequest(http.MethodPatch, "/api/admin/auth-providers/ldap-1", bytes.NewBufferString(bodyJSON))
	req.SetPathValue("id", "ldap-1")
	rr := httptest.NewRecorder()

	h.UpdateAuthProvider(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if store.lastUpdate == nil || store.lastUpdate.Config == nil {
		t.Fatal("store.UpdateAuthProvider was not called with a config")
	}
	got, ok := bindCreds(t, *store.lastUpdate.Config)
	if !ok {
		t.Fatal("bindCredentials missing from stored config")
	}
	if got != "enc:original" {
		t.Errorf("expected preserved encrypted value, got %q", got)
	}
}

func TestUpdateLDAPProvider_EncryptsNewBindCredentials(t *testing.T) {
	existing := makeProvider("ldap-1", "ldap", "Corp LDAP")
	existing.Config = json.RawMessage(`{"url":"ldap://x","bindCredentials":"enc:original"}`)
	updated := existing
	store := &mockStore{fetched: &existing, updated: &updated}
	h := adminproviders.NewHandler(store).WithOIDCHooks(fakeWrap, nil, nil)

	bodyJSON := `{"type":"ldap","config":{"url":"ldap://x","bindCredentials":"rotated"}}`
	req := httptest.NewRequest(http.MethodPatch, "/api/admin/auth-providers/ldap-1", bytes.NewBufferString(bodyJSON))
	req.SetPathValue("id", "ldap-1")
	rr := httptest.NewRecorder()

	h.UpdateAuthProvider(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	got, ok := bindCreds(t, *store.lastUpdate.Config)
	if !ok {
		t.Fatal("bindCredentials missing from stored config")
	}
	if got != "enc:rotated" {
		t.Errorf("expected newly encrypted value, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Tests: DeleteAuthProvider
// ---------------------------------------------------------------------------

func TestDeleteAuthProvider_Returns204(t *testing.T) {
	store := &mockStore{err: nil}
	h := adminproviders.NewHandler(store)

	req := httptest.NewRequest(http.MethodDelete, "/api/admin/auth-providers/provider-1", nil)
	req.SetPathValue("id", "provider-1")
	rr := httptest.NewRecorder()

	h.DeleteAuthProvider(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rr.Code, rr.Body.String())
	}
}
