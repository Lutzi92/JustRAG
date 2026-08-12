package files_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/files"
	"github.com/justrag/go-backend/internal/kbaccess"
	"github.com/justrag/go-backend/internal/storage"
)

// ---------------------------------------------------------------------------
// Mock store
// ---------------------------------------------------------------------------

var (
	_ files.Store        = (*mockStore)(nil)
	_ storage.Storage    = (*mockStorage)(nil)
	_ files.ChunkDeleter = (*mockChunkDeleter)(nil)
)

type mockStore struct {
	file            *files.FileInfo
	fileErr         error
	kb              *kbaccess.KnowledgeBase
	kbErr           error
	role            string
	roleErr         error
	deleteErr       error
	deletedIDs      []string // records every ID passed to DeleteFileRecord
	createFile      *files.FileRecord
	createErr       error
	kbFileLimits    *files.KBFileLimits
	kbFileLimitsErr error
	resetOK         bool
	resetCalled     bool
	resetCount      int
	resetErr        error
	errorFiles      []*files.FileInfo
	markedStage     string
	markedMsg       string
}

func (m *mockStore) ResetFileForRetry(_ context.Context, _ string) (bool, error) {
	m.resetCalled = true
	m.resetCount++
	return m.resetOK, m.resetErr
}

func (m *mockStore) ListErrorFiles(_ context.Context, _ string) ([]*files.FileInfo, error) {
	return m.errorFiles, nil
}

func (m *mockStore) MarkFileError(_ context.Context, _ string, stage, message string) error {
	m.markedStage = stage
	m.markedMsg = message
	return nil
}

func (m *mockStore) GetFileByID(_ context.Context, _ string) (*files.FileInfo, error) {
	return m.file, m.fileErr
}

func (m *mockStore) DeleteFileRecord(_ context.Context, id string) error {
	m.deletedIDs = append(m.deletedIDs, id)
	return m.deleteErr
}

func (m *mockStore) GetKBByID(_ context.Context, _ string) (*kbaccess.KnowledgeBase, error) {
	return m.kb, m.kbErr
}

func (m *mockStore) GetKBRole(_ context.Context, _, _ string) (string, error) {
	return m.role, m.roleErr
}

func (m *mockStore) GetKBFileLimits(_ context.Context, _ string) (*files.KBFileLimits, error) {
	if m.kbFileLimitsErr != nil {
		return nil, m.kbFileLimitsErr
	}
	if m.kbFileLimits != nil {
		return m.kbFileLimits, nil
	}
	return &files.KBFileLimits{FileCount: 0, TotalSize: 0}, nil
}

func (m *mockStore) CreateFile(_ context.Context, data files.CreateFileData) (*files.FileRecord, error) {
	if m.createErr != nil {
		return nil, m.createErr
	}
	if m.createFile != nil {
		return m.createFile, nil
	}
	size := data.Size
	sp := data.StoragePath
	return &files.FileRecord{
		ID:          "new-file-id",
		KbID:        data.KbID,
		Name:        data.Name,
		Type:        data.Type,
		Size:        &size,
		Status:      "pending",
		Progress:    0,
		Origin:      data.Origin,
		StoragePath: &sp,
		CreatedAt:   time.Now(),
	}, nil
}

// ---------------------------------------------------------------------------
// Mock storage
// ---------------------------------------------------------------------------

type mockStorage struct {
	stream    io.ReadCloser
	streamErr error
	deleteErr error
}

func (m *mockStorage) StoreFile(_ context.Context, _ string, _ []byte, _ string) error { return nil }
func (m *mockStorage) StoreFileFromReader(_ context.Context, _ string, _ io.Reader, _ string) error {
	return nil
}
func (m *mockStorage) ReadFile(_ context.Context, _ string) ([]byte, error) { return nil, nil }
func (m *mockStorage) ReadFileStream(_ context.Context, _ string) (io.ReadCloser, error) {
	return m.stream, m.streamErr
}
func (m *mockStorage) DeleteFile(_ context.Context, _ string) error         { return m.deleteErr }
func (m *mockStorage) DeleteFiles(_ context.Context, _ []string) error      { return m.deleteErr }
func (m *mockStorage) DeleteDirectory(_ context.Context, _ string) error    { return nil }
func (m *mockStorage) FileExists(_ context.Context, _ string) (bool, error) { return true, nil }
func (m *mockStorage) IsS3() bool                                           { return false }

// ---------------------------------------------------------------------------
// Mock ChunkDeleter
// ---------------------------------------------------------------------------

type mockChunkDeleter struct{}

func (m *mockChunkDeleter) DeleteChunksByFileIDAllDims(_ context.Context, _ string) error {
	return nil
}

func noopChunks() files.ChunkDeleter { return &mockChunkDeleter{} }

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func strPtr(s string) *string { return &s }

func ownerUser() *auth.Claims {
	return &auth.Claims{ID: "user-1", Username: "alice", Role: "user"}
}

func superadminUser() *auth.Claims {
	return &auth.Claims{ID: "admin-1", Username: "admin", Role: "superadmin"}
}

func withUser(r *http.Request, user *auth.Claims) *http.Request {
	ctx := auth.WithUser(r.Context(), user)
	return r.WithContext(ctx)
}

func newRequest(method, target string) *http.Request {
	return httptest.NewRequest(method, target, nil)
}

// ---------------------------------------------------------------------------
// Download tests
// ---------------------------------------------------------------------------

func TestDownload_Success(t *testing.T) {
	ownerID := "user-1"
	fileInfo := &files.FileInfo{
		ID:          "file-abc",
		KbID:        "kb-1",
		Name:        "report.pdf",
		Type:        "pdf",
		StoragePath: strPtr("user-1/kb-1/report.pdf"),
	}
	kb := &kbaccess.KnowledgeBase{ID: "kb-1", UserID: &ownerID, IsGlobal: false}

	// role mirrors the kb_members row a real owner has (migration 0064's
	// owner-mirror trigger) — kb.UserID alone no longer grants access.
	store := &mockStore{file: fileInfo, kb: kb, role: kbaccess.RoleOwner}
	stor := &mockStorage{stream: io.NopCloser(strings.NewReader("PDF content"))}
	h := files.NewHandler(store, stor, noopChunks())

	req := newRequest(http.MethodGet, "/api/files/file-abc/download")
	req.SetPathValue("id", "file-abc")
	req = withUser(req, ownerUser())

	rr := httptest.NewRecorder()
	h.Download(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if cd := rr.Header().Get("Content-Disposition"); !strings.Contains(cd, "report.pdf") {
		t.Errorf("expected Content-Disposition to contain filename, got %q", cd)
	}
	if ct := rr.Header().Get("Content-Type"); ct == "" {
		t.Error("expected Content-Type header to be set")
	}
	if body := rr.Body.String(); body != "PDF content" {
		t.Errorf("expected body %q, got %q", "PDF content", body)
	}
}

func TestDownload_FileNotFound(t *testing.T) {
	store := &mockStore{file: nil, fileErr: nil}
	stor := &mockStorage{}
	h := files.NewHandler(store, stor, noopChunks())

	req := newRequest(http.MethodGet, "/api/files/nonexistent/download")
	req.SetPathValue("id", "nonexistent")
	req = withUser(req, ownerUser())

	rr := httptest.NewRecorder()
	h.Download(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

func TestDownload_StoreError(t *testing.T) {
	store := &mockStore{file: nil, fileErr: errors.New("db error")}
	stor := &mockStorage{}
	h := files.NewHandler(store, stor, noopChunks())

	req := newRequest(http.MethodGet, "/api/files/file-abc/download")
	req.SetPathValue("id", "file-abc")
	req = withUser(req, ownerUser())

	rr := httptest.NewRecorder()
	h.Download(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rr.Code)
	}
}

func TestDownload_Unauthenticated(t *testing.T) {
	store := &mockStore{}
	stor := &mockStorage{}
	h := files.NewHandler(store, stor, noopChunks())

	req := newRequest(http.MethodGet, "/api/files/file-abc/download")
	req.SetPathValue("id", "file-abc")
	// No user in context.

	rr := httptest.NewRecorder()
	h.Download(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestDownload_AccessDenied(t *testing.T) {
	otherOwnerID := "other-user"
	fileInfo := &files.FileInfo{
		ID:          "file-abc",
		KbID:        "kb-1",
		Name:        "secret.pdf",
		StoragePath: strPtr("other-user/kb-1/secret.pdf"),
	}
	kb := &kbaccess.KnowledgeBase{ID: "kb-1", UserID: &otherOwnerID, IsGlobal: false}

	// No kb_members row returned.
	store := &mockStore{file: fileInfo, kb: kb}
	stor := &mockStorage{}
	h := files.NewHandler(store, stor, noopChunks())

	req := newRequest(http.MethodGet, "/api/files/file-abc/download")
	req.SetPathValue("id", "file-abc")
	req = withUser(req, ownerUser()) // user-1 is not the owner

	rr := httptest.NewRecorder()
	h.Download(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// Delete tests
// ---------------------------------------------------------------------------

func TestDelete_Success(t *testing.T) {
	ownerID := "user-1"
	fileInfo := &files.FileInfo{
		ID:          "file-abc",
		KbID:        "kb-1",
		Name:        "report.pdf",
		StoragePath: strPtr("user-1/kb-1/report.pdf"),
	}
	kb := &kbaccess.KnowledgeBase{ID: "kb-1", UserID: &ownerID, IsGlobal: false}

	// role mirrors the kb_members row a real owner has (migration 0064's
	// owner-mirror trigger) — kb.UserID alone no longer grants access.
	store := &mockStore{file: fileInfo, kb: kb, role: kbaccess.RoleOwner}
	stor := &mockStorage{}
	h := files.NewHandler(store, stor, noopChunks())

	req := newRequest(http.MethodDelete, "/api/files/file-abc")
	req.SetPathValue("id", "file-abc")
	req = withUser(req, ownerUser())

	rr := httptest.NewRecorder()
	h.Delete(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rr.Code)
	}
}

func TestDelete_NoAccess(t *testing.T) {
	otherOwnerID := "other-user"
	fileInfo := &files.FileInfo{
		ID:          "file-abc",
		KbID:        "kb-1",
		Name:        "secret.pdf",
		StoragePath: strPtr("other-user/kb-1/secret.pdf"),
	}
	kb := &kbaccess.KnowledgeBase{ID: "kb-1", UserID: &otherOwnerID, IsGlobal: false}

	// user-1 has a view-only kb_members row.
	store := &mockStore{
		file: fileInfo,
		kb:   kb,
		role: kbaccess.RoleView,
	}
	stor := &mockStorage{}
	h := files.NewHandler(store, stor, noopChunks())

	req := newRequest(http.MethodDelete, "/api/files/file-abc")
	req.SetPathValue("id", "file-abc")
	req = withUser(req, ownerUser()) // user-1 only has view

	rr := httptest.NewRecorder()
	h.Delete(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rr.Code)
	}
}

func TestDelete_FileNotFound(t *testing.T) {
	store := &mockStore{file: nil}
	stor := &mockStorage{}
	h := files.NewHandler(store, stor, noopChunks())

	req := newRequest(http.MethodDelete, "/api/files/nonexistent")
	req.SetPathValue("id", "nonexistent")
	req = withUser(req, ownerUser())

	rr := httptest.NewRecorder()
	h.Delete(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

func TestDelete_Superadmin_CanDeleteAnyFile(t *testing.T) {
	otherOwnerID := "other-user"
	fileInfo := &files.FileInfo{
		ID:          "file-abc",
		KbID:        "kb-1",
		Name:        "report.pdf",
		StoragePath: strPtr("other-user/kb-1/report.pdf"),
	}
	kb := &kbaccess.KnowledgeBase{ID: "kb-1", UserID: &otherOwnerID, IsGlobal: false}

	store := &mockStore{file: fileInfo, kb: kb}
	stor := &mockStorage{}
	h := files.NewHandler(store, stor, noopChunks())

	req := newRequest(http.MethodDelete, "/api/files/file-abc")
	req.SetPathValue("id", "file-abc")
	req = withUser(req, superadminUser())

	rr := httptest.NewRecorder()
	h.Delete(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// Upload tests
// ---------------------------------------------------------------------------

// buildMultipartRequest creates a multipart/form-data POST request with a
// file field named "file". Pass content == nil to omit the field entirely.
func buildMultipartRequest(t *testing.T, filename string, content []byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	if content != nil {
		fw, err := w.CreateFormFile("file", filename)
		if err != nil {
			t.Fatalf("create form file: %v", err)
		}
		if _, err := fw.Write(content); err != nil {
			t.Fatalf("write form file: %v", err)
		}
	}
	w.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/kb/kb-1/files", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.SetPathValue("id", "kb-1")
	return req
}

func withKBAccess(r *http.Request, kb *kbaccess.KnowledgeBase) *http.Request {
	result := &kbaccess.KBAccessResult{KB: kb, Role: kbaccess.RoleEdit}
	return r.WithContext(kbaccess.WithAccess(r.Context(), result))
}

func TestUpload_Success(t *testing.T) {
	ownerID := "user-1"
	kb := &kbaccess.KnowledgeBase{ID: "kb-1", UserID: &ownerID, IsGlobal: false}

	store := &mockStore{}
	stor := &mockStorage{}
	h := files.NewHandler(store, stor, noopChunks())

	req := buildMultipartRequest(t, "document.pdf", []byte("PDF content"))
	req = withUser(req, ownerUser())
	req = withKBAccess(req, kb)

	rr := httptest.NewRecorder()
	h.Upload(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}

	var record files.FileRecord
	if err := json.NewDecoder(rr.Body).Decode(&record); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if record.ID == "" {
		t.Error("expected non-empty file ID")
	}
	if record.Status != "pending" {
		t.Errorf("expected status 'pending', got %q", record.Status)
	}
	if record.Origin != "upload" {
		t.Errorf("expected origin 'upload', got %q", record.Origin)
	}
}

func TestUpload_MissingFileField(t *testing.T) {
	store := &mockStore{}
	stor := &mockStorage{}
	h := files.NewHandler(store, stor, noopChunks())

	// Build a multipart request with no file field.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/kb/kb-1/files", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.SetPathValue("id", "kb-1")
	req = withUser(req, ownerUser())

	rr := httptest.NewRecorder()
	h.Upload(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestUpload_DangerousExtension(t *testing.T) {
	ownerID := "user-1"
	kb := &kbaccess.KnowledgeBase{ID: "kb-1", UserID: &ownerID, IsGlobal: false}

	store := &mockStore{}
	stor := &mockStorage{}
	h := files.NewHandler(store, stor, noopChunks())

	req := buildMultipartRequest(t, "malware.exe", []byte("MZ payload"))
	req = withUser(req, ownerUser())
	req = withKBAccess(req, kb)

	rr := httptest.NewRecorder()
	h.Upload(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "not allowed") {
		t.Errorf("expected 'not allowed' in body, got %q", body)
	}
}

func TestUpload_UnsupportedExtension(t *testing.T) {
	ownerID := "user-1"
	kb := &kbaccess.KnowledgeBase{ID: "kb-1", UserID: &ownerID, IsGlobal: false}

	store := &mockStore{}
	stor := &mockStorage{}
	h := files.NewHandler(store, stor, noopChunks())

	// Legacy .doc has no parser and is not in the dangerous blocklist; it must
	// be rejected up front rather than ingested as garbage / left as an error.
	req := buildMultipartRequest(t, "report.doc", []byte("legacy word doc"))
	req = withUser(req, ownerUser())
	req = withKBAccess(req, kb)

	rr := httptest.NewRecorder()
	h.Upload(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "not supported") {
		t.Errorf("expected 'not supported' in body, got %q", body)
	}
}

func TestUpload_SupportedNonPDF(t *testing.T) {
	ownerID := "user-1"
	kb := &kbaccess.KnowledgeBase{ID: "kb-1", UserID: &ownerID, IsGlobal: false}

	store := &mockStore{}
	stor := &mockStorage{}
	h := files.NewHandler(store, stor, noopChunks())

	// A supported, non-PDF type must still pass the new allowlist check.
	req := buildMultipartRequest(t, "notes.md", []byte("# notes"))
	req = withUser(req, ownerUser())
	req = withKBAccess(req, kb)

	rr := httptest.NewRecorder()
	h.Upload(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestUpload_EmptyFile(t *testing.T) {
	ownerID := "user-1"
	kb := &kbaccess.KnowledgeBase{ID: "kb-1", UserID: &ownerID, IsGlobal: false}

	store := &mockStore{}
	stor := &mockStorage{}
	h := files.NewHandler(store, stor, noopChunks())

	req := buildMultipartRequest(t, "empty.pdf", []byte{})
	req = withUser(req, ownerUser())
	req = withKBAccess(req, kb)

	rr := httptest.NewRecorder()
	h.Upload(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "empty") {
		t.Errorf("expected 'empty' in body, got %q", body)
	}
}

func TestUpload_Unauthenticated(t *testing.T) {
	store := &mockStore{}
	stor := &mockStorage{}
	h := files.NewHandler(store, stor, noopChunks())

	req := buildMultipartRequest(t, "doc.pdf", []byte("data"))
	// No user in context.

	rr := httptest.NewRecorder()
	h.Upload(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}
