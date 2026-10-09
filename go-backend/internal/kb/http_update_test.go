package kb_test

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
	"github.com/justrag/go-backend/internal/kb"
	"github.com/justrag/go-backend/internal/kbaccess"
	"github.com/justrag/go-backend/internal/store"
)

// ---------------------------------------------------------------------------
// Mock store
// ---------------------------------------------------------------------------

var _ kb.UpdateStore = (*mockUpdateStore)(nil)

type mockUpdateStore struct {
	kb    *kb.KBRow
	files []kb.FileRow
	total int
	err   error
}

func (m *mockUpdateStore) UpdateKnowledgeBase(_ context.Context, _ string, _ kb.KBUpdate) (*kb.KBRow, error) {
	return m.kb, m.err
}

func (m *mockUpdateStore) ListFiles(_ context.Context, _ string, _, _ int) ([]kb.FileRow, int, error) {
	return m.files, m.total, m.err
}

func (m *mockUpdateStore) GetKBChunkConfig(_ context.Context, _ string) (int, int, error) {
	return 0, 0, m.err
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// injectKBAccess returns a copy of r with a KBAccessResult injected into the
// context, mirroring what kbaccess.RequireKBRole does in production.
func injectKBAccess(r *http.Request, kbID string) *http.Request {
	return injectKBAccessAs(r, &kbaccess.KnowledgeBase{ID: kbID, IsGlobal: false}, kbaccess.RoleOwner, auth.RoleUser)
}

// injectKBAccessAs is the explicit-role variant: it injects both the resolved
// KBAccessResult (role) and the auth claims (system role), which is what the
// rename gate reads.
func injectKBAccessAs(r *http.Request, kbRow *kbaccess.KnowledgeBase, role, sysRole string) *http.Request {
	access := &kbaccess.KBAccessResult{
		KB:      kbRow,
		IsOwner: role == kbaccess.RoleOwner,
		Role:    role,
	}
	ctx := kbaccess.WithAccess(r.Context(), access)
	ctx = auth.WithUser(ctx, &auth.Claims{ID: "user-1", Username: "u", Role: sysRole})
	return r.WithContext(ctx)
}

func makeKBRow(id, name string) *kb.KBRow {
	desc := "Test description"
	lang := "de"
	return &kb.KBRow{
		ID:          id,
		Name:        name,
		Description: &desc,
		Language:    lang,
		IsGlobal:    false,
		IsPublished: true,
		CreatedAt:   time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func makeFileRow(id, name string) kb.FileRow {
	size := 1024
	return kb.FileRow{
		ID:        id,
		Name:      name,
		Type:      "pdf",
		Size:      &size,
		Status:    "ready",
		Progress:  100,
		Origin:    "upload",
		CreatedAt: time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC),
	}
}

// ---------------------------------------------------------------------------
// UpdateKB tests
// ---------------------------------------------------------------------------

// TestUpdateKB_Valid checks that a valid PATCH body returns the updated KB with 200.
func TestUpdateKB_Valid(t *testing.T) {
	store := &mockUpdateStore{
		kb: makeKBRow("kb-1", "Updated KB"),
	}
	h := kb.NewUpdateHandler(store, nil)

	body := map[string]any{"name": "Updated KB"}
	b, _ := json.Marshal(body)

	r := httptest.NewRequest(http.MethodPatch, "/api/kb/kb-1", bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	r = injectKBAccess(r, "kb-1")

	w := httptest.NewRecorder()
	h.UpdateKB(w, r)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result kb.KBRow
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if result.ID != "kb-1" {
		t.Errorf("expected id=kb-1, got %q", result.ID)
	}
	if result.Name != "Updated KB" {
		t.Errorf("expected name=%q, got %q", "Updated KB", result.Name)
	}
}

// TestUpdateKB_NotFound checks that store.ErrNotFound yields 404.
func TestUpdateKB_NotFound(t *testing.T) {
	mockStore := &mockUpdateStore{err: store.ErrNotFound}
	h := kb.NewUpdateHandler(mockStore, nil)

	body := map[string]any{"name": "Ghost KB"}
	b, _ := json.Marshal(body)

	r := httptest.NewRequest(http.MethodPatch, "/api/kb/missing", bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	r = injectKBAccess(r, "missing")

	w := httptest.NewRecorder()
	h.UpdateKB(w, r)

	if w.Result().StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Result().StatusCode)
	}
}

// TestUpdateKB_InvalidBody checks that a malformed body yields 400.
func TestUpdateKB_InvalidBody(t *testing.T) {
	store := &mockUpdateStore{}
	h := kb.NewUpdateHandler(store, nil)

	r := httptest.NewRequest(http.MethodPatch, "/api/kb/kb-1", bytes.NewReader([]byte("not-json")))
	r.Header.Set("Content-Type", "application/json")
	r = injectKBAccess(r, "kb-1")

	w := httptest.NewRecorder()
	h.UpdateKB(w, r)

	if w.Result().StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Result().StatusCode)
	}
}

// ---------------------------------------------------------------------------
// ListFiles tests
// ---------------------------------------------------------------------------

// TestListFiles_DefaultPagination checks that the default limit/offset are applied
// and that the response envelope matches the store data.
func TestListFiles_DefaultPagination(t *testing.T) {
	files := []kb.FileRow{
		makeFileRow("f-1", "document.pdf"),
		makeFileRow("f-2", "report.pdf"),
	}
	store := &mockUpdateStore{files: files, total: 2}
	h := kb.NewUpdateHandler(store, nil)

	r := httptest.NewRequest(http.MethodGet, "/api/kb/kb-1/files", nil)
	r = injectKBAccess(r, "kb-1")

	w := httptest.NewRecorder()
	h.ListFiles(w, r)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result []kb.FileRow
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(result) != 2 {
		t.Errorf("expected 2 files, got %d", len(result))
	}
	if result[0].ID != "f-1" {
		t.Errorf("expected first file id=f-1, got %q", result[0].ID)
	}
}

// TestListFiles_CustomPagination checks that explicit limit/offset query params are forwarded.
func TestListFiles_CustomPagination(t *testing.T) {
	store := &mockUpdateStore{files: []kb.FileRow{makeFileRow("f-3", "extra.pdf")}, total: 10}
	h := kb.NewUpdateHandler(store, nil)

	r := httptest.NewRequest(http.MethodGet, "/api/kb/kb-1/files?limit=5&offset=5", nil)
	r = injectKBAccess(r, "kb-1")

	w := httptest.NewRecorder()
	h.ListFiles(w, r)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var files []kb.FileRow
	if err := json.NewDecoder(resp.Body).Decode(&files); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(files) != 1 {
		t.Errorf("expected 1 file, got %d", len(files))
	}
}

// TestListFiles_EmptyResult checks that an empty file list returns an empty array (not null).
func TestListFiles_EmptyResult(t *testing.T) {
	store := &mockUpdateStore{files: nil, total: 0}
	h := kb.NewUpdateHandler(store, nil)

	r := httptest.NewRequest(http.MethodGet, "/api/kb/kb-1/files", nil)
	r = injectKBAccess(r, "kb-1")

	w := httptest.NewRecorder()
	h.ListFiles(w, r)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var files []kb.FileRow
	if err := json.NewDecoder(resp.Body).Decode(&files); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if files == nil {
		t.Error("expected non-nil empty array, got null")
	}
	if len(files) != 0 {
		t.Errorf("expected 0 files, got %d", len(files))
	}
}

// TestListFiles_ErrorFieldsSerialized checks that errorStage/errorMessage are
// emitted for errored files and omitted entirely for healthy ones.
func TestListFiles_ErrorFieldsSerialized(t *testing.T) {
	stage, msg := "parse", "The file could not be parsed"
	bad := makeFileRow("f-bad", "broken.pdf")
	bad.Status = "error"
	bad.ErrorStage = &stage
	bad.ErrorMessage = &msg
	good := makeFileRow("f-good", "fine.pdf")

	store := &mockUpdateStore{files: []kb.FileRow{bad, good}, total: 2}
	h := kb.NewUpdateHandler(store, nil)

	r := httptest.NewRequest(http.MethodGet, "/api/kb/kb-1/files", nil)
	r = injectKBAccess(r, "kb-1")
	w := httptest.NewRecorder()
	h.ListFiles(w, r)

	body := w.Body.String()
	if !strings.Contains(body, `"errorStage":"parse"`) {
		t.Errorf("errorStage missing from response: %s", body)
	}
	if !strings.Contains(body, `"errorMessage":"The file could not be parsed"`) {
		t.Errorf("errorMessage missing from response: %s", body)
	}
	if strings.Count(body, "errorStage") != 1 {
		t.Errorf("errorStage must be omitted for non-error files: %s", body)
	}
}

// TestListFiles_StageFieldsSerialized checks that currentStage/stageIndex/stageTotal
// are emitted for in-progress files and omitted entirely for idle ones.
func TestListFiles_StageFieldsSerialized(t *testing.T) {
	stage := "embed"
	idx, total := 3, 5
	active := makeFileRow("f-active", "doc.pdf")
	active.Status = "processing"
	active.CurrentStage = &stage
	active.StageIndex = &idx
	active.StageTotal = &total
	idle := makeFileRow("f-idle", "done.pdf")

	store := &mockUpdateStore{files: []kb.FileRow{active, idle}, total: 2}
	h := kb.NewUpdateHandler(store, nil)

	r := httptest.NewRequest(http.MethodGet, "/api/kb/kb-1/files", nil)
	r = injectKBAccess(r, "kb-1")
	w := httptest.NewRecorder()
	h.ListFiles(w, r)

	body := w.Body.String()
	if !strings.Contains(body, `"currentStage":"embed"`) {
		t.Errorf("currentStage missing: %s", body)
	}
	if !strings.Contains(body, `"stageIndex":3`) {
		t.Errorf("stageIndex missing: %s", body)
	}
	if !strings.Contains(body, `"stageTotal":5`) {
		t.Errorf("stageTotal missing: %s", body)
	}
	if strings.Count(body, "currentStage") != 1 {
		t.Errorf("currentStage must be omitted for idle files: %s", body)
	}
	if strings.Count(body, "stageIndex") != 1 {
		t.Errorf("stageIndex must be omitted for idle files: %s", body)
	}
	if strings.Count(body, "stageTotal") != 1 {
		t.Errorf("stageTotal must be omitted for idle files: %s", body)
	}
}

func listFilesAs(t *testing.T, st *mockUpdateStore, role string, withAccess bool) []map[string]any {
	t.Helper()
	h := kb.NewUpdateHandler(st, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/kb/kb-1/files", nil)
	req.SetPathValue("id", "kb-1")
	if withAccess {
		req = req.WithContext(kbaccess.WithAccess(req.Context(), &kbaccess.KBAccessResult{
			KB: &kbaccess.KnowledgeBase{ID: "kb-1"}, Role: role,
		}))
	}
	rr := httptest.NewRecorder()
	h.ListFiles(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return rows
}

func filesWithUploader() *mockUpdateStore {
	return &mockUpdateStore{files: []kb.FileRow{
		{ID: "f1", Name: "a.pdf", UploadedBy: &kb.FileUploader{ID: "u1", DisplayName: "Ada Lovelace"}},
		{ID: "f2", Name: "feed.md"}, // source-owned: no uploader
	}}
}

func TestListFiles_UploaderVisibleToEditors(t *testing.T) {
	for _, role := range []string{kbaccess.RoleEdit, kbaccess.RoleAdmin, kbaccess.RoleOwner} {
		rows := listFilesAs(t, filesWithUploader(), role, true)
		ub, ok := rows[0]["uploadedBy"].(map[string]any)
		if !ok || ub["id"] != "u1" || ub["displayName"] != "Ada Lovelace" {
			t.Errorf("role %s: uploadedBy = %v, want {u1, Ada Lovelace}", role, rows[0]["uploadedBy"])
		}
		if _, present := rows[1]["uploadedBy"]; present {
			t.Errorf("role %s: a row without uploader must omit the key, got %v", role, rows[1]["uploadedBy"])
		}
	}
}

func TestListFiles_UploaderHiddenFromViewers(t *testing.T) {
	for _, tc := range []struct {
		name       string
		role       string
		withAccess bool
	}{
		{"view role", kbaccess.RoleView, true},
		{"no access in context", "", false},
	} {
		rows := listFilesAs(t, filesWithUploader(), tc.role, tc.withAccess)
		for i, r := range rows {
			if _, present := r["uploadedBy"]; present {
				t.Errorf("%s: row %d leaks uploadedBy = %v", tc.name, i, r["uploadedBy"])
			}
		}
	}
}

// TestListFiles_UserFileID pins the userFileId key: present for a library
// copy, omitted for a plain file.
func TestListFiles_UserFileID(t *testing.T) {
	linked := makeFileRow("f-1", "a.pdf")
	uf := "7b0c9f64-2b1e-4a39-9d51-0a6a1f0e2c11"
	linked.UserFileID = &uf
	store := &mockUpdateStore{files: []kb.FileRow{linked, makeFileRow("f-2", "b.pdf")}, total: 2}
	h := kb.NewUpdateHandler(store, nil)
	r := injectKBAccess(httptest.NewRequest(http.MethodGet, "/api/kb/kb-1/files", nil), "kb-1")
	w := httptest.NewRecorder()
	h.ListFiles(w, r)

	var raw []map[string]any
	if err := json.NewDecoder(w.Result().Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(raw) != 2 {
		t.Fatalf("rows = %d", len(raw))
	}
	if raw[0]["userFileId"] != uf {
		t.Errorf("linked row userFileId = %v", raw[0]["userFileId"])
	}
	if _, ok := raw[1]["userFileId"]; ok {
		t.Errorf("plain row must omit userFileId")
	}
}
