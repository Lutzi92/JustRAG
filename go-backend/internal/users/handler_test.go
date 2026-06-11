package users_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/users"
)

// ---------------------------------------------------------------------------
// Mock store
// ---------------------------------------------------------------------------

var _ users.Store = (*mockStore)(nil)

type mockStore struct {
	byID       *users.UserRow
	byUsername *users.UserRow
	byTerm     *users.UserRow
	updated    *users.UserRow
	err        error
}

func (m *mockStore) GetUserByID(_ context.Context, _ string) (*users.UserRow, error) {
	return m.byID, m.err
}

func (m *mockStore) GetUserByUsername(_ context.Context, _ string) (*users.UserRow, error) {
	return m.byUsername, m.err
}

func (m *mockStore) SearchUserByTerm(_ context.Context, _ string) (*users.UserRow, error) {
	return m.byTerm, m.err
}

func (m *mockStore) UpdateUser(_ context.Context, _ string, _ users.UserUpdate) (*users.UserRow, error) {
	return m.updated, m.err
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func makeUser(id, username, role string) *users.UserRow {
	email := username + "@example.com"
	first := "First"
	last := "Last"
	return &users.UserRow{
		ID:           id,
		Username:     username,
		PasswordHash: "$2a$10$somehash",
		FirstName:    &first,
		LastName:     &last,
		Email:        &email,
		Role:         role,
		CreatedAt:    time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

// newGetRequest creates a GET request with PathValue set for "id".
func newGetRequest(id string, claims *auth.Claims) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/users/"+id, nil)
	// Inject path value (Go 1.22+ net/http uses r.PathValue)
	req.SetPathValue("id", id)
	if claims != nil {
		ctx := auth.WithUser(req.Context(), claims)
		req = req.WithContext(ctx)
	}
	return req
}

// ---------------------------------------------------------------------------
// Tests: GetUser
// ---------------------------------------------------------------------------

func TestGetUser_OwnProfile_FullResponse(t *testing.T) {
	user := makeUser("11111111-1111-1111-1111-111111111111", "alice", "user")
	store := &mockStore{byID: user}
	h := users.NewHandler(store)

	req := newGetRequest("11111111-1111-1111-1111-111111111111", &auth.Claims{
		ID:   "11111111-1111-1111-1111-111111111111",
		Role: "user",
	})
	rec := httptest.NewRecorder()
	h.GetUser(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// Full response should include email, role, createdAt
	if _, ok := body["email"]; !ok {
		t.Error("expected email in own-profile response")
	}
	if _, ok := body["role"]; !ok {
		t.Error("expected role in own-profile response")
	}
	if _, ok := body["createdAt"]; !ok {
		t.Error("expected createdAt in own-profile response")
	}
	if body["id"] != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("unexpected id: %v", body["id"])
	}
}

func TestGetUser_OtherUser_LimitedResponse(t *testing.T) {
	foundUser := makeUser("22222222-2222-2222-2222-222222222222", "bob", "user")
	store := &mockStore{byID: foundUser}
	h := users.NewHandler(store)

	// viewer is a different user (not admin)
	req := newGetRequest("22222222-2222-2222-2222-222222222222", &auth.Claims{
		ID:   "33333333-3333-3333-3333-333333333333",
		Role: "user",
	})
	rec := httptest.NewRecorder()
	h.GetUser(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// Limited response: id, username, firstName, lastName only
	if _, ok := body["email"]; ok {
		t.Error("expected email to be absent in limited response")
	}
	if _, ok := body["role"]; ok {
		t.Error("expected role to be absent in limited response")
	}
	if _, ok := body["createdAt"]; ok {
		t.Error("expected createdAt to be absent in limited response")
	}
	if body["username"] != "bob" {
		t.Errorf("expected username bob, got %v", body["username"])
	}
}

func TestGetUser_NotFound_Returns404(t *testing.T) {
	store := &mockStore{byID: nil, byUsername: nil, byTerm: nil}
	h := users.NewHandler(store)

	req := newGetRequest("nobody", nil)
	rec := httptest.NewRecorder()
	h.GetUser(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// Tests: UpdateUser
// ---------------------------------------------------------------------------

func TestUpdateUser_OwnProfile_Succeeds(t *testing.T) {
	id := "11111111-1111-1111-1111-111111111111"
	existing := makeUser(id, "alice", "user")
	updated := makeUser(id, "alice", "user")
	newFirst := "Alicia"
	updated.FirstName = &newFirst

	store := &mockStore{byID: existing, updated: updated}
	h := users.NewHandler(store)

	body := map[string]string{"firstName": "Alicia"}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPatch, "/api/users/"+id, bytes.NewReader(bodyBytes))
	req.SetPathValue("id", id)
	ctx := auth.WithUser(req.Context(), &auth.Claims{ID: id, Role: "user"})
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	h.UpdateUser(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp["firstName"] != "Alicia" {
		t.Errorf("expected firstName=Alicia, got %v", resp["firstName"])
	}
	// Password hash must not appear in the response
	if _, ok := resp["passwordHash"]; ok {
		t.Error("passwordHash must not be in the response")
	}
}

func TestUpdateUser_OtherUser_Returns403(t *testing.T) {
	targetID := "22222222-2222-2222-2222-222222222222"
	viewerID := "33333333-3333-3333-3333-333333333333"

	store := &mockStore{}
	h := users.NewHandler(store)

	body := map[string]string{"firstName": "Hacker"}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPatch, "/api/users/"+targetID, bytes.NewReader(bodyBytes))
	req.SetPathValue("id", targetID)
	ctx := auth.WithUser(req.Context(), &auth.Claims{ID: viewerID, Role: "user"})
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	h.UpdateUser(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestUpdateUser_UserNotFound_Returns404(t *testing.T) {
	id := "44444444-4444-4444-4444-444444444444"

	store := &mockStore{byID: nil} // GetUserByID returns nil
	h := users.NewHandler(store)

	body := map[string]string{"firstName": "Ghost"}
	bodyBytes, _ := json.Marshal(body)

	req := httptest.NewRequest(http.MethodPatch, "/api/users/"+id, bytes.NewReader(bodyBytes))
	req.SetPathValue("id", id)
	ctx := auth.WithUser(req.Context(), &auth.Claims{ID: id, Role: "user"})
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	h.UpdateUser(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}
