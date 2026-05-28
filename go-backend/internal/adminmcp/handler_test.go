package adminmcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/justrag/go-backend/internal/mcp"
)

// fakeCfg lets tests pre-seed `mcp_servers` and `chat_use_mcp_tools`
// without a real DB.
type fakeCfg struct {
	values map[string]string
}

func (f *fakeCfg) GetSiteConfigValue(_ context.Context, key string) (*string, error) {
	v, ok := f.values[key]
	if !ok {
		return nil, nil
	}
	return &v, nil
}

func TestGetStatus_EmptyRegistryReturnsBuiltinsOnly(t *testing.T) {
	reg := mcp.NewRegistry()
	reg.RegisterBuiltin(mcp.Tool{Name: "kb_search", Description: "demo"})
	cfg := &fakeCfg{values: map[string]string{"chat_use_mcp_tools": "true"}}
	h := NewHandler(reg, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/mcp/status", nil)
	w := httptest.NewRecorder()
	h.GetStatus(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var resp StatusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if !resp.UseMCPTools {
		t.Error("UseMCPTools should reflect site_config")
	}
	if len(resp.Builtins) != 1 || resp.Builtins[0].Name != "kb_search" {
		t.Errorf("Builtins = %+v, want one entry kb_search", resp.Builtins)
	}
	if len(resp.Servers) != 0 {
		t.Errorf("Servers should be empty, got %d", len(resp.Servers))
	}
}

func TestPostReload_RejectsMalformedJSON(t *testing.T) {
	reg := mcp.NewRegistry()
	cfg := &fakeCfg{values: map[string]string{
		"mcp_servers": "{this is not an array",
	}}
	h := NewHandler(reg, cfg)

	req := httptest.NewRequest(http.MethodPost, "/api/admin/mcp/reload", nil)
	w := httptest.NewRecorder()
	h.PostReload(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestPostReload_AppliesValidServersAndReportsUnreachable(t *testing.T) {
	// One unreachable server (port 1 reliably refuses).
	cfg := &fakeCfg{values: map[string]string{
		"mcp_servers": `[{"name":"unreachable","url":"http://127.0.0.1:1"}]`,
	}}
	reg := mcp.NewRegistry()
	h := NewHandler(reg, cfg)

	req := httptest.NewRequest(http.MethodPost, "/api/admin/mcp/reload", nil)
	w := httptest.NewRecorder()
	h.PostReload(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s (per-server failures must NOT 5xx)", w.Code, w.Body.String())
	}
	var resp StatusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if len(resp.Servers) != 1 {
		t.Fatalf("Servers count = %d, want 1", len(resp.Servers))
	}
	if resp.Servers[0].Reachable {
		t.Errorf("server should be unreachable, got Reachable=true")
	}
	if resp.Servers[0].Error == "" {
		t.Errorf("unreachable server should carry Error message")
	}
}
