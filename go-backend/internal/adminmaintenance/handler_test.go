package adminmaintenance

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hibiken/asynq"
	"github.com/justrag/go-backend/internal/kb"
)

// fakeMemListStore is a stub MemoryListStore for tests.
type fakeMemListStore struct{ ids []string }

func (f *fakeMemListStore) ListUserIDsWithMemory(_ context.Context) ([]string, error) {
	return f.ids, nil
}

// fakeStore is a stub KBStore that returns canned responses.
type fakeStore struct {
	listAllErr error
	kbIDs      []string
	files      map[string][]kb.FileReembedRow
	listErr    map[string]error
}

func (f *fakeStore) ListAllKBIDs(ctx context.Context) ([]string, error) {
	return f.kbIDs, f.listAllErr
}

func (f *fakeStore) ListReembedableFilesByKBID(ctx context.Context, kbID string) ([]kb.FileReembedRow, error) {
	if err, ok := f.listErr[kbID]; ok {
		return nil, err
	}
	return f.files[kbID], nil
}

// fakeEnqueuer records every enqueue call. enqueueErr lets a test inject
// transient failures.
type fakeEnqueuer struct {
	enqueued   []*asynq.Task
	enqueueErr error
}

func (f *fakeEnqueuer) Enqueue(task *asynq.Task, opts ...asynq.Option) (*asynq.TaskInfo, error) {
	if f.enqueueErr != nil {
		return nil, f.enqueueErr
	}
	f.enqueued = append(f.enqueued, task)
	return &asynq.TaskInfo{ID: "stub"}, nil
}

func storagePtr(s string) *string { return &s }

func TestReembedAll_HappyPath(t *testing.T) {
	store := &fakeStore{
		kbIDs: []string{"kb-1", "kb-2"},
		files: map[string][]kb.FileReembedRow{
			"kb-1": {
				{ID: "f1", KbID: "kb-1", StoragePath: storagePtr("/a"), Name: "a.pdf", Type: "application/pdf"},
				{ID: "f2", KbID: "kb-1", StoragePath: nil}, // skipped — no storage
			},
			"kb-2": {
				{ID: "f3", KbID: "kb-2", StoragePath: storagePtr(""), Name: "x"}, // skipped — empty path
				{ID: "f4", KbID: "kb-2", StoragePath: storagePtr("/b"), Name: "b.md"},
			},
		},
	}
	enq := &fakeEnqueuer{}
	h := NewHandler(store, enq, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/admin/reembed-all", nil)
	rr := httptest.NewRecorder()
	h.ReembedAll(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rr.Code, rr.Body.String())
	}
	if got, want := len(enq.enqueued), 2; got != want {
		t.Errorf("enqueued = %d, want %d", got, want)
	}

	var resp struct {
		Success bool `json:"success"`
		Queued  int  `json:"queued"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.Success || resp.Queued != 2 {
		t.Errorf("response = %+v, want {Success:true, Queued:2}", resp)
	}
}

func TestReembedAll_PartialFailureContinues(t *testing.T) {
	store := &fakeStore{
		kbIDs: []string{"kb-good", "kb-broken"},
		files: map[string][]kb.FileReembedRow{
			"kb-good": {{ID: "f1", KbID: "kb-good", StoragePath: storagePtr("/a"), Name: "a.pdf"}},
		},
		listErr: map[string]error{"kb-broken": errors.New("db down")},
	}
	enq := &fakeEnqueuer{}
	h := NewHandler(store, enq, nil)

	rr := httptest.NewRecorder()
	h.ReembedAll(rr, httptest.NewRequest(http.MethodPost, "/api/admin/reembed-all", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (failures should not abort sweep)", rr.Code)
	}
	if got, want := len(enq.enqueued), 1; got != want {
		t.Errorf("enqueued = %d, want %d", got, want)
	}
}

func TestReembedAll_TopLevelFailureReturns500(t *testing.T) {
	store := &fakeStore{listAllErr: errors.New("db down")}
	h := NewHandler(store, &fakeEnqueuer{}, nil)

	rr := httptest.NewRecorder()
	h.ReembedAll(rr, httptest.NewRequest(http.MethodPost, "/api/admin/reembed-all", nil))

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
}

func TestUploadAgentTemplate_HappyPath(t *testing.T) {
	dir := t.TempDir()
	h := NewHandler(nil, nil, nil)
	h.templateDir = dir

	body := &strings.Builder{}
	mw := multipart.NewWriter(body)
	fw, err := mw.CreateFormFile("template", "agent.json")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := io.WriteString(fw, `{"hello":"world"}`); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/admin/agent/template", strings.NewReader(body.String()))
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rr := httptest.NewRecorder()
	h.UploadAgentTemplate(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rr.Code, rr.Body.String())
	}
	target := filepath.Join(dir, "agent.json")
	contents, err := readAll(target)
	if err != nil {
		t.Fatalf("read uploaded file: %v", err)
	}
	if string(contents) != `{"hello":"world"}` {
		t.Errorf("uploaded contents = %q, want JSON payload", string(contents))
	}
}

func TestUploadAgentTemplate_PathTraversalNeutralised(t *testing.T) {
	dir := t.TempDir()
	h := NewHandler(nil, nil, nil)
	h.templateDir = dir

	body := &strings.Builder{}
	mw := multipart.NewWriter(body)
	fw, _ := mw.CreateFormFile("template", "../../etc/passwd")
	io.WriteString(fw, "malicious")
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/admin/agent/template", strings.NewReader(body.String()))
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rr := httptest.NewRecorder()
	h.UploadAgentTemplate(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	// filepath.Base strips the ../../etc/ prefix; "passwd" is allowlist-clean
	// (alphanumeric only) so it lands inside templateDir under that name.
	if _, err := readAll(filepath.Join(dir, "passwd")); err != nil {
		t.Fatalf("expected file at base name; got %v", err)
	}
}

func TestUploadAgentTemplate_RejectsUnsafeFilename(t *testing.T) {
	cases := []struct {
		name     string
		filename string
	}{
		{"shell metacharacter", "evil;rm -rf.json"},
		{"space", "my agent.json"},
		{"unicode control", "agent\x00.json"},
		{"hidden dotfile", ".bashrc"},
		{"empty after Base", ""},
		{"only dots", ".."},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			h := NewHandler(nil, nil, nil)
			h.templateDir = dir

			body := &strings.Builder{}
			mw := multipart.NewWriter(body)
			fw, _ := mw.CreateFormFile("template", tc.filename)
			io.WriteString(fw, "data")
			mw.Close()

			req := httptest.NewRequest(http.MethodPost, "/api/admin/agent/template", strings.NewReader(body.String()))
			req.Header.Set("Content-Type", mw.FormDataContentType())
			rr := httptest.NewRecorder()
			h.UploadAgentTemplate(rr, req)

			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 for %q (body=%q)", rr.Code, tc.filename, rr.Body.String())
			}

			entries, _ := os.ReadDir(dir)
			if len(entries) != 0 {
				t.Fatalf("expected no files written for rejected upload; got %d", len(entries))
			}
		})
	}
}

func TestUploadAgentTemplate_RejectsOverlongFilename(t *testing.T) {
	dir := t.TempDir()
	h := NewHandler(nil, nil, nil)
	h.templateDir = dir

	long := strings.Repeat("a", maxTemplateFilenameLen+1) + ".json"
	body := &strings.Builder{}
	mw := multipart.NewWriter(body)
	fw, _ := mw.CreateFormFile("template", long)
	io.WriteString(fw, "data")
	mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/admin/agent/template", strings.NewReader(body.String()))
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rr := httptest.NewRecorder()
	h.UploadAgentTemplate(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%q", rr.Code, rr.Body.String())
	}
}

func TestReembedUserMemory_EnqueuesPerUser(t *testing.T) {
	enq := &fakeEnqueuer{}
	h := NewHandler(&fakeStore{}, enq, &fakeMemListStore{ids: []string{"u1", "u2", "u3"}})

	req := httptest.NewRequest(http.MethodPost, "/api/admin/reembed-user-memory", nil)
	rec := httptest.NewRecorder()
	h.ReembedUserMemory(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got, want := len(enq.enqueued), 3; got != want {
		t.Errorf("enqueued = %d, want %d", got, want)
	}
}

func readAll(path string) ([]byte, error) {
	return os.ReadFile(path)
}
