package kb_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/kb"
	"github.com/justrag/go-backend/internal/tabular"
)

func newTabularRequest(kbID, fileID string) *http.Request {
	req := withUser(httptest.NewRequest(http.MethodGet, "/api/kb/"+kbID+"/files/"+fileID+"/tabular", nil), testUser())
	req.SetPathValue("id", kbID)
	req.SetPathValue("fileId", fileID)
	return req
}

// TestGetFileTabular_OK pins the happy path: a file that belongs to the KB
// in the route, with a parse report and one catalog entry.
func TestGetFileTabular_OK(t *testing.T) {
	store := &mockStore{
		fileRef:     &kb.FileRef{ID: "file-1", KbID: "kb-1"},
		parseReport: json.RawMessage(`{"version":1,"materialised":true,"sheets":[{"name":"Sheet1"}]}`),
		catalogEntries: []tabular.CatalogEntry{
			{FileID: "file-1", SheetName: "Sheet1", TableName: "sheet_aa_0_0", SheetIndex: 0, RegionIndex: 0, HeaderRow: 0,
				Columns: []tabular.ColumnSpec{{Original: "A", Name: "a", Type: tabular.TypeText}}},
		},
	}
	h := kb.NewHandler(store)

	req := newTabularRequest("kb-1", "file-1")
	rr := httptest.NewRecorder()
	h.GetFileTabular(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if store.gotFileID != "file-1" {
		t.Errorf("store called with fileID %q, want %q", store.gotFileID, "file-1")
	}

	var got tabular.FileTabularDTO
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Report == nil || got.Report.Version != 1 {
		t.Errorf("expected report to decode, got %+v", got.Report)
	}
	if len(got.Tables) != 1 || got.Tables[0].TableName != "sheet_aa_0_0" {
		t.Errorf("expected 1 table sheet_aa_0_0, got %+v", got.Tables)
	}
}

// TestGetFileTabular_ForeignFile pins the cross-KB guard: a file that exists
// but belongs to a different KB than {id} in the route must 404, not 200 or
// 403 — kbViewChain already proved the caller holds view on {id}, so leaking
// existence of a file in another KB would be the bug.
func TestGetFileTabular_ForeignFile(t *testing.T) {
	store := &mockStore{
		fileRef: &kb.FileRef{ID: "file-1", KbID: "kb-OTHER"},
	}
	h := kb.NewHandler(store)

	req := newTabularRequest("kb-1", "file-1")
	rr := httptest.NewRecorder()
	h.GetFileTabular(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for a file owned by a different KB, got %d", rr.Code)
	}
}

// TestGetFileTabular_UnknownFile pins the plain not-found case: no file row
// at all also 404s, indistinguishable from the foreign-file case (both must
// look identical from the outside).
func TestGetFileTabular_UnknownFile(t *testing.T) {
	store := &mockStore{fileRef: nil}
	h := kb.NewHandler(store)

	req := newTabularRequest("kb-1", "missing-file")
	rr := httptest.NewRecorder()
	h.GetFileTabular(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for an unknown file id, got %d", rr.Code)
	}
}

// TestGetFileTabular_NonSpreadsheetFile pins the empty-state contract: a
// file with no parse_report and no catalog rows (a PDF, say) is a normal 200
// with report:null and an empty tables array, not an error.
func TestGetFileTabular_NonSpreadsheetFile(t *testing.T) {
	store := &mockStore{
		fileRef:        &kb.FileRef{ID: "file-2", KbID: "kb-1"},
		parseReport:    nil,
		catalogEntries: nil,
	}
	h := kb.NewHandler(store)

	req := newTabularRequest("kb-1", "file-2")
	rr := httptest.NewRecorder()
	h.GetFileTabular(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"report":null`) || !strings.Contains(body, `"tables":[]`) {
		t.Fatalf("expected report:null and an empty tables array, got %s", body)
	}
}
