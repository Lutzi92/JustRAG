package authhandler_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/justrag/go-backend/internal/adminproviders"
)

type publicProvidersResp struct {
	Providers []struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Name string `json:"name"`
	} `json:"providers"`
	LocalAuthEnabled bool `json:"localAuthEnabled"`
}

func TestListPublicProviders_Shape(t *testing.T) {
	store := &mockStore{
		providers: []adminproviders.AuthProviderRow{
			{ID: "p1", Type: "oidc", Name: "Azure AD"},
		},
	}
	h := newHandler(store)

	t.Run("local enabled when env unset", func(t *testing.T) {
		t.Setenv("DISABLE_LOCAL_AUTH", "")
		req := httptest.NewRequest(http.MethodGet, "/api/auth/providers", nil)
		rr := httptest.NewRecorder()
		h.ListPublicProviders(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
		}
		var resp publicProvidersResp
		if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if !resp.LocalAuthEnabled {
			t.Error("expected localAuthEnabled=true when DISABLE_LOCAL_AUTH unset")
		}
		if len(resp.Providers) != 1 || resp.Providers[0].Type != "oidc" {
			t.Fatalf("expected one oidc provider, got %+v", resp.Providers)
		}
		if resp.Providers[0].Name != "Azure AD" {
			t.Errorf("expected name Azure AD, got %q", resp.Providers[0].Name)
		}
	})

	t.Run("local disabled when env true", func(t *testing.T) {
		t.Setenv("DISABLE_LOCAL_AUTH", "true")
		req := httptest.NewRequest(http.MethodGet, "/api/auth/providers", nil)
		rr := httptest.NewRecorder()
		h.ListPublicProviders(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
		}
		var resp publicProvidersResp
		if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.LocalAuthEnabled {
			t.Error("expected localAuthEnabled=false when DISABLE_LOCAL_AUTH=true")
		}
		if len(resp.Providers) != 1 || resp.Providers[0].Type != "oidc" {
			t.Fatalf("expected provider list preserved when local disabled, got %+v", resp.Providers)
		}
	})
}
