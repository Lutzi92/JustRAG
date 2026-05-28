package apikeys_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/apikeys"
	"github.com/justrag/go-backend/internal/auth"
)

var _ apikeys.Store = (*mockStore)(nil)

// mockStore is a test double for apikeys.Store.
type mockStore struct {
	rows        []apikeys.ApiKeyRow
	createdRow  *apikeys.ApiKeyRow
	count       int
	deletedOK   bool
	err         error
	lastCreated apikeys.ApiKeyCreate
}

func (m *mockStore) CreateApiKey(_ context.Context, data apikeys.ApiKeyCreate) (*apikeys.ApiKeyRow, error) {
	m.lastCreated = data
	if m.err != nil {
		return nil, m.err
	}
	return m.createdRow, nil
}

func (m *mockStore) GetApiKeysByUser(_ context.Context, _ string) ([]apikeys.ApiKeyRow, error) {
	return m.rows, m.err
}

func (m *mockStore) CountApiKeysByUser(_ context.Context, _ string) (int, error) {
	return m.count, m.err
}

func (m *mockStore) DeleteApiKey(_ context.Context, _, _ string) (bool, error) {
	return m.deletedOK, m.err
}

// withAdminUser injects admin auth.Claims into the request context.
func withAdminUser(r *http.Request) *http.Request {
	claims := &auth.Claims{ID: "user-1", Username: "admin", Role: "admin"}
	ctx := auth.WithUser(r.Context(), claims)
	return r.WithContext(ctx)
}

// TestCreateApiKey_ValidRequest verifies that a valid POST returns 201 with a key starting with "jrag_".
func TestCreateApiKey_ValidRequest(t *testing.T) {
	createdAt := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	store := &mockStore{
		count: 0,
		createdRow: &apikeys.ApiKeyRow{
			ID:        "key-id-1",
			Name:      "My Key",
			KeyPrefix: "jrag_abc1234",
			CreatedAt: createdAt,
		},
	}
	h := apikeys.NewHandler(store)

	body := `{"name": "My Key"}`
	req := httptest.NewRequest(http.MethodPost, "/api/api-keys", strings.NewReader(body))
	req = withAdminUser(req)
	rr := httptest.NewRecorder()

	h.CreateApiKey(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	key, ok := resp["key"].(string)
	if !ok || !strings.HasPrefix(key, "jrag_") {
		t.Errorf("expected key starting with 'jrag_', got %q", key)
	}

	if resp["id"] != "key-id-1" {
		t.Errorf("expected id=key-id-1, got %v", resp["id"])
	}
	if resp["name"] != "My Key" {
		t.Errorf("expected name='My Key', got %v", resp["name"])
	}

	// Verify the store received a hashed key (not plaintext).
	if store.lastCreated.KeyHash == "" {
		t.Error("expected KeyHash to be set in store call")
	}
	if store.lastCreated.KeyHash == key {
		t.Error("KeyHash must not equal the plaintext key")
	}
}

// TestCreateApiKey_MissingName verifies that an empty name returns 400.
func TestCreateApiKey_MissingName(t *testing.T) {
	store := &mockStore{count: 0}
	h := apikeys.NewHandler(store)

	body := `{"name": ""}`
	req := httptest.NewRequest(http.MethodPost, "/api/api-keys", strings.NewReader(body))
	req = withAdminUser(req)
	rr := httptest.NewRecorder()

	h.CreateApiKey(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

// TestCreateApiKey_MaxKeysExceeded verifies that exceeding 10 keys returns 400.
func TestCreateApiKey_MaxKeysExceeded(t *testing.T) {
	store := &mockStore{count: 10}
	h := apikeys.NewHandler(store)

	body := `{"name": "Another Key"}`
	req := httptest.NewRequest(http.MethodPost, "/api/api-keys", strings.NewReader(body))
	req = withAdminUser(req)
	rr := httptest.NewRecorder()

	h.CreateApiKey(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}

	var resp map[string]string
	json.NewDecoder(rr.Body).Decode(&resp)
	if !strings.Contains(resp["error"], "maximum") {
		t.Errorf("expected 'maximum' in error message, got %q", resp["error"])
	}
}

// TestListApiKeys_ReturnsArray verifies that GET returns a JSON array of keys.
func TestListApiKeys_ReturnsArray(t *testing.T) {
	createdAt := time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC)
	store := &mockStore{
		rows: []apikeys.ApiKeyRow{
			{ID: "k1", Name: "Key One", KeyPrefix: "jrag_aaa1111", CreatedAt: createdAt},
			{ID: "k2", Name: "Key Two", KeyPrefix: "jrag_bbb2222", CreatedAt: createdAt},
		},
	}
	h := apikeys.NewHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/api/api-keys", nil)
	req = withAdminUser(req)
	rr := httptest.NewRecorder()

	h.ListApiKeys(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	var result []apikeys.ApiKeyRow
	if err := json.NewDecoder(rr.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("expected 2 keys, got %d", len(result))
	}
	if result[0].ID != "k1" {
		t.Errorf("expected first key id=k1, got %s", result[0].ID)
	}
}

// TestDeleteApiKey_Existing verifies that deleting an existing key returns 204.
func TestDeleteApiKey_Existing(t *testing.T) {
	store := &mockStore{deletedOK: true}
	h := apikeys.NewHandler(store)

	req := httptest.NewRequest(http.MethodDelete, "/api/api-keys/key-id-1", nil)
	req.SetPathValue("id", "key-id-1")
	req = withAdminUser(req)
	rr := httptest.NewRecorder()

	h.DeleteApiKey(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestDeleteApiKey_NotFound verifies that deleting a non-existent key returns 404.
func TestDeleteApiKey_NotFound(t *testing.T) {
	store := &mockStore{deletedOK: false}
	h := apikeys.NewHandler(store)

	req := httptest.NewRequest(http.MethodDelete, "/api/api-keys/missing-key", nil)
	req.SetPathValue("id", "missing-key")
	req = withAdminUser(req)
	rr := httptest.NewRecorder()

	h.DeleteApiKey(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

// TestCreateApiKey_StoreError verifies that a store error returns 500.
func TestCreateApiKey_StoreError(t *testing.T) {
	store := &mockStore{count: 0, err: errors.New("db down")}
	h := apikeys.NewHandler(store)

	// The error is returned by CountApiKeysByUser
	body, _ := json.Marshal(map[string]string{"name": "Test"})
	req := httptest.NewRequest(http.MethodPost, "/api/api-keys", bytes.NewReader(body))
	req = withAdminUser(req)
	rr := httptest.NewRecorder()

	h.CreateApiKey(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rr.Code)
	}
}
