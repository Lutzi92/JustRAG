package siteconfig_test

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/siteconfig"
)

var _ siteconfig.Store = (*mockStore)(nil)

type mockStore struct {
	rows      []siteconfig.SiteConfigRow
	err       error
	batchRows []siteconfig.SiteConfigRow
	batchErr  error
	updateErr error

	lastBatch []siteconfig.KeyValue // captures what UpdateSiteConfigsBatch received
}

func (m *mockStore) GetAllSiteConfigs(ctx context.Context) ([]siteconfig.SiteConfigRow, error) {
	return m.rows, m.err
}

func (m *mockStore) UpdateSiteConfigsBatch(ctx context.Context, configs []siteconfig.KeyValue) ([]siteconfig.SiteConfigRow, error) {
	m.lastBatch = configs
	return m.batchRows, m.batchErr
}

func (m *mockStore) UpdateSiteConfig(ctx context.Context, key, value string) error {
	return m.updateErr
}

func strPtr(s string) *string {
	return &s
}

func makeRows() []siteconfig.SiteConfigRow {
	return []siteconfig.SiteConfigRow{
		{Key: "logo_path", Value: strPtr("/images/logo.png")},
		{Key: "imprint", Value: strPtr("Some imprint text")},
		{Key: "kb_header", Value: strPtr("Welcome")},
		{Key: "example_prompts", Value: strPtr("[]")},
		{Key: "chat_footer", Value: strPtr("Footer text")},
		{Key: "confluence_enabled", Value: strPtr("true")},
		{Key: "web_search_enabled", Value: strPtr("true")},
		{Key: "academic_search_enabled", Value: strPtr("false")},
		{Key: "academic_search_name", Value: strPtr("Scholar")},
		{Key: "google_search_api_key", Value: strPtr("secret-api-key")},
		{Key: "openai_api_key", Value: strPtr("sk-secret")},
		{Key: "null_value_key", Value: nil},
	}
}

func TestGetSiteConfig_Unauthenticated(t *testing.T) {
	store := &mockStore{rows: makeRows()}
	h := siteconfig.NewHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/api/site-config", nil)
	rec := httptest.NewRecorder()
	h.GetSiteConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// Public keys should be present
	publicKeys := []string{
		"logo_path", "imprint", "kb_header", "example_prompts",
		"chat_footer", "confluence_enabled", "web_search_enabled",
		"academic_search_enabled", "academic_search_name",
	}
	for _, k := range publicKeys {
		if _, ok := body[k]; !ok {
			t.Errorf("expected public key %q to be present", k)
		}
	}

	// Secret keys must be absent
	secretKeys := []string{"google_search_api_key", "openai_api_key"}
	for _, k := range secretKeys {
		if _, ok := body[k]; ok {
			t.Errorf("expected secret key %q to be filtered out for unauthenticated user", k)
		}
	}

	// NULL value key must be absent
	if _, ok := body["null_value_key"]; ok {
		t.Error("expected null_value_key to be absent")
	}
}

func TestGetSiteConfig_Admin(t *testing.T) {
	store := &mockStore{rows: makeRows()}
	h := siteconfig.NewHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/api/site-config", nil)
	ctx := auth.WithUser(req.Context(), &auth.Claims{
		ID:   "1",
		Role: "admin",
	})
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	h.GetSiteConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// All non-null keys should be present
	expectedKeys := []string{
		"logo_path", "imprint", "kb_header", "example_prompts",
		"chat_footer", "confluence_enabled", "web_search_enabled",
		"academic_search_enabled", "academic_search_name",
		"google_search_api_key", "openai_api_key",
	}
	for _, k := range expectedKeys {
		if _, ok := body[k]; !ok {
			t.Errorf("expected admin to see key %q", k)
		}
	}

	// NULL value key must still be absent
	if _, ok := body["null_value_key"]; ok {
		t.Error("expected null_value_key to be absent even for admin")
	}
}

func TestGetSiteConfig_Superadmin(t *testing.T) {
	store := &mockStore{rows: makeRows()}
	h := siteconfig.NewHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/api/site-config", nil)
	ctx := auth.WithUser(req.Context(), &auth.Claims{
		ID:   "2",
		Role: "superadmin",
	})
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	h.GetSiteConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// Superadmin should see all non-null keys including secrets
	if _, ok := body["google_search_api_key"]; !ok {
		t.Error("expected superadmin to see google_search_api_key")
	}
	if _, ok := body["openai_api_key"]; !ok {
		t.Error("expected superadmin to see openai_api_key")
	}
}

func TestGetSiteConfig_RegularUser(t *testing.T) {
	store := &mockStore{rows: makeRows()}
	h := siteconfig.NewHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/api/site-config", nil)
	ctx := auth.WithUser(req.Context(), &auth.Claims{
		ID:   "3",
		Role: "user",
	})
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	h.GetSiteConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// Regular user should not see secret keys
	if _, ok := body["google_search_api_key"]; ok {
		t.Error("expected regular user to not see google_search_api_key")
	}
	if _, ok := body["openai_api_key"]; ok {
		t.Error("expected regular user to not see openai_api_key")
	}

	// But should see public keys
	if _, ok := body["logo_path"]; !ok {
		t.Error("expected regular user to see logo_path")
	}
}

func TestGetSiteConfig_AdminSecretIsMasked(t *testing.T) {
	store := &mockStore{rows: makeRows()}
	h := siteconfig.NewHandler(store)

	req := httptest.NewRequest(http.MethodGet, "/api/site-config", nil)
	req = req.WithContext(auth.WithUser(req.Context(), &auth.Claims{ID: "1", Role: "admin"}))
	rec := httptest.NewRecorder()
	h.GetSiteConfig(rec, req)

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["google_search_api_key"] == "secret-api-key" {
		t.Error("admin response leaked the raw google_search_api_key")
	}
	if body["google_search_api_key"] != "********" {
		t.Errorf("expected masked google_search_api_key, got %q", body["google_search_api_key"])
	}
}

func TestUpdateSiteConfig_MaskedSecretIsPreserved(t *testing.T) {
	store := &mockStore{batchRows: []siteconfig.SiteConfigRow{}}
	h := siteconfig.NewHandler(store)

	// The admin saves the form without changing the masked key.
	body := strings.NewReader(`{"configs":{"google_search_api_key":"********","kb_header":"new"}}`)
	req := httptest.NewRequest(http.MethodPost, "/api/site-config", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.UpdateSiteConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	for _, kv := range store.lastBatch {
		if kv.Key == "google_search_api_key" {
			t.Errorf("masked secret should not have been written, got value %v", kv.Value)
		}
	}
	// The non-secret edit must still go through.
	var sawKB bool
	for _, kv := range store.lastBatch {
		if kv.Key == "kb_header" {
			sawKB = true
		}
	}
	if !sawKB {
		t.Error("expected non-secret kb_header update to be persisted")
	}
}

// ---------------------------------------------------------------------------
// UpdateSiteConfig tests
// ---------------------------------------------------------------------------

func TestUpdateSiteConfig_ValidBody(t *testing.T) {
	val := "updated"
	store := &mockStore{
		batchRows: []siteconfig.SiteConfigRow{
			{Key: "kb_header", Value: &val},
		},
	}
	h := siteconfig.NewHandler(store)

	body := strings.NewReader(`{"configs":{"kb_header":"updated"}}`)
	req := httptest.NewRequest(http.MethodPost, "/api/site-config", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.UpdateSiteConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var result map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if result["kb_header"] != "updated" {
		t.Errorf("expected kb_header=updated, got %q", result["kb_header"])
	}
}

func TestUpdateSiteConfig_MissingConfigs(t *testing.T) {
	store := &mockStore{}
	h := siteconfig.NewHandler(store)

	body := strings.NewReader(`{}`)
	req := httptest.NewRequest(http.MethodPost, "/api/site-config", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.UpdateSiteConfig(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestUpdateSiteConfig_InvalidJSON(t *testing.T) {
	store := &mockStore{}
	h := siteconfig.NewHandler(store)

	body := strings.NewReader(`not-json`)
	req := httptest.NewRequest(http.MethodPost, "/api/site-config", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.UpdateSiteConfig(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// UploadLogo tests
// ---------------------------------------------------------------------------

// buildMultipart creates a multipart body with a single file field.
func buildMultipart(t *testing.T, fieldName, filename, content string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile(fieldName, filename)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := fw.Write([]byte(content)); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	w.Close()
	return &buf, w.FormDataContentType()
}

func TestUploadLogo_ValidPNG(t *testing.T) {
	store := &mockStore{}
	h := siteconfig.NewHandler(store)

	buf, ct := buildMultipart(t, "logo", "mylogo.png", "fake-png-data")
	req := httptest.NewRequest(http.MethodPost, "/api/site-config/logo", buf)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()

	h.UploadLogo(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var result map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if result["success"] != "true" {
		t.Errorf("expected success=true, got %q", result["success"])
	}
	if !strings.HasPrefix(result["logoPath"], "/uploads/logo/logo_") {
		t.Errorf("unexpected logoPath: %q", result["logoPath"])
	}
	if !strings.HasSuffix(result["logoPath"], ".png") {
		t.Errorf("expected .png extension in logoPath: %q", result["logoPath"])
	}
}

func TestUploadLogo_ValidSVG(t *testing.T) {
	store := &mockStore{}
	h := siteconfig.NewHandler(store)

	buf, ct := buildMultipart(t, "logo", "icon.svg", "<svg/>")
	req := httptest.NewRequest(http.MethodPost, "/api/site-config/logo", buf)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()

	h.UploadLogo(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var result map[string]string
	json.NewDecoder(rec.Body).Decode(&result)
	if !strings.HasSuffix(result["logoPath"], ".svg") {
		t.Errorf("expected .svg extension in logoPath: %q", result["logoPath"])
	}
}

func TestUploadLogo_InvalidExtension(t *testing.T) {
	store := &mockStore{}
	h := siteconfig.NewHandler(store)

	buf, ct := buildMultipart(t, "logo", "evil.exe", "bad-data")
	req := httptest.NewRequest(http.MethodPost, "/api/site-config/logo", buf)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()

	h.UploadLogo(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestUploadLogo_MissingFile(t *testing.T) {
	store := &mockStore{}
	h := siteconfig.NewHandler(store)

	// Send a multipart form without the "logo" field.
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	w.WriteField("other_field", "value")
	w.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/site-config/logo", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()

	h.UploadLogo(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}
