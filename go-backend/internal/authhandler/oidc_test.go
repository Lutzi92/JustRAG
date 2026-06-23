package authhandler

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/adminproviders"
	"github.com/justrag/go-backend/internal/users"
)

// fakeOIDCStore is a minimal in-memory OIDCStore for exercising
// resolveOIDCUser's three-branch match. Methods unrelated to the test path
// (CreateUser-by-username, GetActiveAuthProviders, GetUserByUsername,
// GetActiveOIDCProvider) are stubbed.
type fakeOIDCStore struct {
	byExternalID map[string]*users.UserRow
	byUsername   map[string][]*users.UserRow
	linked       []*users.UserRow
	created      []users.UserCreate
	nextID       int

	getExternalErr error
	getUsernameErr error
	linkErr        error
	createErr      error

	appliedInvitesFor string
}

func (f *fakeOIDCStore) GetUserByUsername(ctx context.Context, username string) (*users.UserRow, error) {
	return nil, nil
}

func (f *fakeOIDCStore) CreateUser(ctx context.Context, data users.UserCreate) (*users.UserRow, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.created = append(f.created, data)
	f.nextID++
	row := &users.UserRow{
		ID:         "new-" + idStr(f.nextID),
		Username:   data.Username,
		AuthMethod: data.AuthMethod,
		Email:      data.Email,
		Role:       data.Role,
		ExternalID: data.ExternalID,
		CreatedAt:  time.Now(),
	}
	return row, nil
}

func (f *fakeOIDCStore) GetActiveAuthProviders(ctx context.Context) ([]adminproviders.AuthProviderRow, error) {
	return nil, nil
}

func (f *fakeOIDCStore) GetActiveOIDCProvider(ctx context.Context) (*adminproviders.AuthProviderRow, error) {
	return nil, nil
}

func (f *fakeOIDCStore) GetUserByExternalID(ctx context.Context, externalID string) (*users.UserRow, error) {
	if f.getExternalErr != nil {
		return nil, f.getExternalErr
	}
	return f.byExternalID[externalID], nil
}

func (f *fakeOIDCStore) GetUsersByUsername(ctx context.Context, username string) ([]*users.UserRow, error) {
	if f.getUsernameErr != nil {
		return nil, f.getUsernameErr
	}
	return f.byUsername[strings.ToLower(username)], nil
}

func (f *fakeOIDCStore) LinkUserExternalID(ctx context.Context, userID, externalID string) (*users.UserRow, error) {
	if f.linkErr != nil {
		return nil, f.linkErr
	}
	for _, list := range f.byUsername {
		for _, u := range list {
			if u.ID == userID {
				u.ExternalID = &externalID
				u.AuthMethod = "oidc"
				f.linked = append(f.linked, u)
				return u, nil
			}
		}
	}
	return nil, errors.New("user not found in fake store")
}

func (f *fakeOIDCStore) ApplyPendingInvites(_ context.Context, _ string, username string) error {
	f.appliedInvitesFor = username
	return nil
}

func idStr(i int) string {
	const digits = "0123456789"
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{digits[i%10]}, b...)
		i /= 10
	}
	return string(b)
}

func sampleClaims() oidcClaims {
	return oidcClaims{
		Sub:           "sub-123",
		Email:         "alice@example.com",
		EmailVerified: true,
		GivenName:     "Alice",
		FamilyName:    "Liddell",
		PreferredUN:   "alice",
	}
}

// Branch 1: external_id already mapped -> return as-is, no link/create.
func TestResolveOIDCUser_ByExternalID(t *testing.T) {
	store := &fakeOIDCStore{
		byExternalID: map[string]*users.UserRow{
			"sub-123": {ID: "u-1", Username: "alice", AuthMethod: "oidc"},
		},
	}
	got, err := resolveOIDCUser(context.Background(), store, sampleClaims())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != "u-1" {
		t.Fatalf("expected u-1, got %s", got.ID)
	}
	if len(store.created) != 0 || len(store.linked) != 0 {
		t.Fatalf("steady-state lookup must not create or link (created=%d linked=%d)", len(store.created), len(store.linked))
	}
}

// Branch 2: LDAP→OIDC migration — single preferred_username match, link
// preserves users.id (and survives an email change at the IdP).
func TestResolveOIDCUser_LinksExistingByUsername(t *testing.T) {
	existing := &users.UserRow{ID: "u-42", Username: "alice", AuthMethod: "ldap", Email: ptr("alice.old@example.com")}
	store := &fakeOIDCStore{
		byExternalID: map[string]*users.UserRow{},
		byUsername: map[string][]*users.UserRow{
			"alice": {existing},
		},
	}
	got, err := resolveOIDCUser(context.Background(), store, sampleClaims())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != "u-42" {
		t.Fatalf("expected the linked row's id u-42, got %s", got.ID)
	}
	if got.AuthMethod != "oidc" {
		t.Fatalf("expected auth_method=oidc after link, got %s", got.AuthMethod)
	}
	if got.ExternalID == nil || *got.ExternalID != "sub-123" {
		t.Fatalf("expected external_id=sub-123 after link, got %v", got.ExternalID)
	}
	if len(store.created) != 0 {
		t.Fatalf("link branch must not create a new user, got %d", len(store.created))
	}
}

// Branch 3: no match -> create new OIDC-provisioned user with external_id set.
func TestResolveOIDCUser_CreatesNewUser(t *testing.T) {
	store := &fakeOIDCStore{
		byExternalID: map[string]*users.UserRow{},
		byUsername:   map[string][]*users.UserRow{},
	}
	got, err := resolveOIDCUser(context.Background(), store, sampleClaims())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.AuthMethod != "oidc" {
		t.Fatalf("expected auth_method=oidc, got %s", got.AuthMethod)
	}
	if len(store.created) != 1 {
		t.Fatalf("expected 1 user created, got %d", len(store.created))
	}
	created := store.created[0]
	if created.ExternalID == nil || *created.ExternalID != "sub-123" {
		t.Fatalf("expected external_id=sub-123 in CreateUser arg, got %v", created.ExternalID)
	}
	if created.Username != "alice" {
		t.Fatalf("expected username from preferred_username=alice, got %s", created.Username)
	}
}

// Username conflict: >1 rows share the username -> fail closed (no link, no create).
func TestResolveOIDCUser_FailsClosedOnUsernameConflict(t *testing.T) {
	store := &fakeOIDCStore{
		byExternalID: map[string]*users.UserRow{},
		byUsername: map[string][]*users.UserRow{
			"alice": {
				{ID: "u-1", Username: "alice"},
				{ID: "u-2", Username: "Alice"},
			},
		},
	}
	_, err := resolveOIDCUser(context.Background(), store, sampleClaims())
	if err == nil {
		t.Fatalf("expected fail-closed error on username conflict")
	}
	if !strings.Contains(err.Error(), "oidc_username_conflict") {
		t.Fatalf("expected oidc_username_conflict in error, got %v", err)
	}
	if len(store.created) != 0 || len(store.linked) != 0 {
		t.Fatalf("conflict must not create or link (created=%d linked=%d)", len(store.created), len(store.linked))
	}
}

// Username derivation falls back to email local part when preferred_username is
// empty. With preferred_username missing, the link branch is skipped entirely
// (no email-fallback match) so this also creates a brand-new user.
func TestResolveOIDCUser_FallsBackToEmailLocalPart(t *testing.T) {
	claims := sampleClaims()
	claims.PreferredUN = ""

	store := &fakeOIDCStore{
		byExternalID: map[string]*users.UserRow{},
		byUsername:   map[string][]*users.UserRow{},
	}
	_, err := resolveOIDCUser(context.Background(), store, claims)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store.created[0].Username != "alice" {
		t.Fatalf("expected fallback username=alice from email local part, got %s", store.created[0].Username)
	}
}

func ptr(s string) *string { return &s }
