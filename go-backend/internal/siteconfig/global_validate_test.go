package siteconfig_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/siteconfig"
)

// TestValidateGlobalValues walks the hook's whole decision table: a key it
// does not validate is passed through, a nil/empty value clears the key and is
// always valid, and a malformed document is rejected with the key named — an
// unnamed error would be useless in a batch save of forty keys.
func TestValidateGlobalValues(t *testing.T) {
	t.Parallel()
	empty := ""
	blank := "   "
	goodPolicy := `[{"when":{"query_type":["lookup"]},"orchestrator":"supervisor","mode":"prefer"}]`
	badPolicy := `[{"when":{},"orchestrator":"nope","mode":"force"}]`
	goodTools := `{"lookup":["kb_search","chunk_read"]}`
	badTools := `{"lookup":["kb_serch"]}`
	other := `not json at all`

	cases := []struct {
		name    string
		updates []siteconfig.KeyValue
		wantErr string // "" = expect no error; otherwise a required substring
	}{
		{"no updates", nil, ""},
		{"unvalidated key passes any value", []siteconfig.KeyValue{{Key: "kb_header", Value: &other}}, ""},
		{"nil value clears the policy", []siteconfig.KeyValue{{Key: "chat_orchestrator_policy", Value: nil}}, ""},
		{"empty value clears the policy", []siteconfig.KeyValue{{Key: "chat_orchestrator_policy", Value: &empty}}, ""},
		{"blank value clears the policy", []siteconfig.KeyValue{{Key: "chat_orchestrator_policy", Value: &blank}}, ""},
		{"valid policy", []siteconfig.KeyValue{{Key: "chat_orchestrator_policy", Value: &goodPolicy}}, ""},
		{"invalid policy", []siteconfig.KeyValue{{Key: "chat_orchestrator_policy", Value: &badPolicy}}, "chat_orchestrator_policy"},
		{"nil value clears the tool map", []siteconfig.KeyValue{{Key: "chat_answer_tools_by_route", Value: nil}}, ""},
		{"valid tool map", []siteconfig.KeyValue{{Key: "chat_answer_tools_by_route", Value: &goodTools}}, ""},
		{"invalid tool map", []siteconfig.KeyValue{{Key: "chat_answer_tools_by_route", Value: &badTools}}, "chat_answer_tools_by_route"},
		{
			"one bad key in an otherwise valid batch",
			[]siteconfig.KeyValue{
				{Key: "kb_header", Value: &other},
				{Key: "chat_orchestrator_policy", Value: &goodPolicy},
				{Key: "chat_answer_tools_by_route", Value: &badTools},
			},
			"chat_answer_tools_by_route",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := siteconfig.ValidateGlobalValues(tc.updates)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not name the key %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// TestValidateGlobalValues_ErrorCarriesTheReason — the operator needs to see
// WHY, not just which key.
func TestValidateGlobalValues_ErrorCarriesTheReason(t *testing.T) {
	t.Parallel()
	bad := `[{"when":{},"orchestrator":"nope","mode":"force"}]`
	err := siteconfig.ValidateGlobalValues([]siteconfig.KeyValue{{Key: "chat_orchestrator_policy", Value: &bad}})
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "unknown orchestrator") {
		t.Fatalf("error %q should carry the parser's reason", err.Error())
	}
}

// TestUpdateSiteConfig_RejectsInvalidPolicy is the wiring test: without the
// hook call in the handler, this posts 200 and the store receives the broken
// document.
func TestUpdateSiteConfig_RejectsInvalidPolicy(t *testing.T) {
	store := &mockStore{}
	h := siteconfig.NewHandler(store)

	body := strings.NewReader(`{"configs":{"chat_orchestrator_policy":"[{\"orchestrator\":\"nope\"}]"}}`)
	req := httptest.NewRequest(http.MethodPost, "/api/site-config", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.UpdateSiteConfig(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if store.lastBatch != nil {
		t.Fatalf("store was written despite the validation failure: %+v", store.lastBatch)
	}
	if !strings.Contains(rec.Body.String(), "chat_orchestrator_policy") {
		t.Fatalf("response %q should name the offending key", rec.Body.String())
	}
}

// TestUpdateSiteConfig_RejectsInvalidToolMap — the second validated key.
func TestUpdateSiteConfig_RejectsInvalidToolMap(t *testing.T) {
	store := &mockStore{}
	h := siteconfig.NewHandler(store)

	body := strings.NewReader(`{"configs":{"chat_answer_tools_by_route":"{\"lookup\":[\"kb_serch\"]}"}}`)
	req := httptest.NewRequest(http.MethodPost, "/api/site-config", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.UpdateSiteConfig(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if store.lastBatch != nil {
		t.Fatalf("store was written despite the validation failure: %+v", store.lastBatch)
	}
}

// TestUpdateSiteConfig_AcceptsValidPolicy proves the hook is a gate, not a
// wall: a well-formed document still reaches the store.
func TestUpdateSiteConfig_AcceptsValidPolicy(t *testing.T) {
	store := &mockStore{}
	h := siteconfig.NewHandler(store)

	body := strings.NewReader(`{"configs":{"chat_orchestrator_policy":"[{\"when\":{},\"orchestrator\":\"supervisor\",\"mode\":\"prefer\"}]"}}`)
	req := httptest.NewRequest(http.MethodPost, "/api/site-config", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.UpdateSiteConfig(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(store.lastBatch) != 1 || store.lastBatch[0].Key != "chat_orchestrator_policy" {
		t.Fatalf("store did not receive the policy: %+v", store.lastBatch)
	}
}
