package files_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/justrag/go-backend/internal/files"
	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/uploadcheck"
	"github.com/justrag/go-backend/internal/userfiles"
)

const libPath = "library/user-1"

type fakeLibrary struct {
	known     map[string]*userfiles.UserFile // id -> file (owner implicitly user-1)
	ingestErr error
	ingested  *userfiles.UserFile
	getErr    error // returned by Get for every id when set
	getUsers  []string
}

func (f *fakeLibrary) Ingest(_ context.Context, _ string, _ *uploadcheck.Upload) (*userfiles.UserFile, bool, error) {
	if f.ingestErr != nil {
		return nil, false, f.ingestErr
	}
	return f.ingested, true, nil
}

func (f *fakeLibrary) Get(_ context.Context, userID, id string) (*userfiles.UserFile, error) {
	f.getUsers = append(f.getUsers, userID)
	if f.getErr != nil {
		return nil, f.getErr
	}
	if uf, ok := f.known[id]; ok {
		return uf, nil
	}
	return nil, userfiles.ErrNotFound
}

func libFile(id string) *userfiles.UserFile {
	return &userfiles.UserFile{ID: id, Name: id + ".pdf", Mime: "application/pdf", Size: 10, StoragePath: libPath + "/" + id}
}

// recStorage records blob deletions.
type recStorage struct {
	mockStorage
	deleted []string
}

func (r *recStorage) DeleteFile(_ context.Context, p string) error {
	r.deleted = append(r.deleted, p)
	return nil
}

type optEnqueuer struct{ tasks []*asynq.Task }

func (e *optEnqueuer) Enqueue(t *asynq.Task, _ ...asynq.Option) (*asynq.TaskInfo, error) {
	e.tasks = append(e.tasks, t)
	return &asynq.TaskInfo{}, nil
}

type enqueuer interface {
	Enqueue(task *asynq.Task, opts ...asynq.Option) (*asynq.TaskInfo, error)
}

func fromLibraryReq(body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/kb/kb-1/files/from-library", strings.NewReader(body))
	req.SetPathValue("id", "kb-1")
	return withKBAccess(withUser(req, ownerUser()), defaultKB())
}

type fromLibResp struct {
	Added []struct {
		FileID     string `json:"fileId"`
		UserFileID string `json:"userFileId"`
	} `json:"added"`
	Skipped []struct {
		UserFileID string `json:"userFileId"`
		Reason     string `json:"reason"`
	} `json:"skipped"`
}

func TestAddFromLibrary_AddsAndSkips(t *testing.T) {
	store := &mockStore{copies: map[string]string{"dup": "existing-row"}}
	enq := &optEnqueuer{}
	h := files.NewHandlerWithEnqueuer(store, &mockStorage{}, noopChunks(), enq)
	h.SetLibrary(&fakeLibrary{known: map[string]*userfiles.UserFile{"a": libFile("a"), "dup": libFile("dup")}})

	rr := httptest.NewRecorder()
	h.AddFromLibrary(rr, fromLibraryReq(`{"userFileIds":["a","foreign","dup"]}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	var resp fromLibResp
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Added) != 1 || resp.Added[0].UserFileID != "a" || resp.Added[0].FileID == "" {
		t.Fatalf("added = %+v", resp.Added)
	}
	reasons := map[string]string{}
	for _, s := range resp.Skipped {
		reasons[s.UserFileID] = s.Reason
	}
	if reasons["foreign"] != "not_found" || reasons["dup"] != "already_in_kb" || len(reasons) != 2 {
		t.Fatalf("skipped = %+v", resp.Skipped)
	}
	if len(store.created) != 1 {
		t.Fatalf("created = %+v", store.created)
	}
	c := store.created[0]
	if c.UserFileID != "a" || c.StoragePath != libPath+"/a" || c.Origin != "upload" || c.UploadedBy != "user-1" || c.Name != "a.pdf" || c.Type != "application/pdf" {
		t.Fatalf("CreateFile = %+v", c)
	}
	if len(enq.tasks) != 1 {
		t.Fatalf("enqueued %d", len(enq.tasks))
	}
	var p jobs.FileProcessingPayload
	if err := json.Unmarshal(enq.tasks[0].Payload(), &p); err != nil {
		t.Fatal(err)
	}
	if p.FilePath != libPath+"/a" || p.KbID != "kb-1" || p.OriginalName != "a.pdf" {
		t.Fatalf("payload = %+v", p)
	}
	if p.UserFileID != "a" {
		t.Fatalf("payload UserFileID = %q, want %q", p.UserFileID, "a")
	}
}

func TestAddFromLibrary_KBFull(t *testing.T) {
	store := &mockStore{kbFileLimits: &files.KBFileLimits{FileCount: 500}}
	h := files.NewHandlerWithEnqueuer(store, &mockStorage{}, noopChunks(), &optEnqueuer{})
	h.SetLibrary(&fakeLibrary{known: map[string]*userfiles.UserFile{"a": libFile("a")}})
	rr := httptest.NewRecorder()
	h.AddFromLibrary(rr, fromLibraryReq(`{"userFileIds":["a"]}`))
	var resp fromLibResp
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if rr.Code != 200 || len(resp.Added) != 0 || len(resp.Skipped) != 1 || resp.Skipped[0].Reason != "kb_full" {
		t.Fatalf("%d %s", rr.Code, rr.Body.String())
	}
	if len(store.created) != 0 {
		t.Fatal("must not create")
	}
}

func TestAddFromLibrary_IDCountBounds(t *testing.T) {
	h := files.NewHandlerWithEnqueuer(&mockStore{}, &mockStorage{}, noopChunks(), &optEnqueuer{})
	h.SetLibrary(&fakeLibrary{})
	ids := make([]string, 101)
	for i := range ids {
		ids[i] = "x"
	}
	big, _ := json.Marshal(map[string]any{"userFileIds": ids})
	for _, body := range []string{`{"userFileIds":[]}`, `{}`, string(big)} {
		rr := httptest.NewRecorder()
		h.AddFromLibrary(rr, fromLibraryReq(body))
		if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "userFileIds must contain 1-100 ids") {
			t.Fatalf("body %.30s: %d %s", body, rr.Code, rr.Body.String())
		}
	}
}

func TestAddFromLibrary_UniqueViolationIsAlreadyInKB(t *testing.T) {
	store := &mockStore{createErr: &pgconn.PgError{Code: "23505"}}
	h := files.NewHandlerWithEnqueuer(store, &mockStorage{}, noopChunks(), &optEnqueuer{})
	h.SetLibrary(&fakeLibrary{known: map[string]*userfiles.UserFile{"a": libFile("a")}})
	rr := httptest.NewRecorder()
	h.AddFromLibrary(rr, fromLibraryReq(`{"userFileIds":["a"]}`))
	var resp fromLibResp
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if rr.Code != 200 || len(resp.Skipped) != 1 || resp.Skipped[0].Reason != "already_in_kb" {
		t.Fatalf("%d %s", rr.Code, rr.Body.String())
	}
}

func TestAddFromLibrary_EnqueueFailureUsesDeleterNotBlob(t *testing.T) {
	store := &mockStore{}
	stor := &recStorage{}
	h := files.NewHandlerWithEnqueuer(store, stor, noopChunks(), &failingEnqueuer{})
	fd := &fakeFileDeleter{}
	h.SetFileDeleter(fd)
	h.SetLibrary(&fakeLibrary{known: map[string]*userfiles.UserFile{"a": libFile("a")}})
	rr := httptest.NewRecorder()
	h.AddFromLibrary(rr, fromLibraryReq(`{"userFileIds":["a"]}`))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status %d", rr.Code)
	}
	if len(fd.got) != 1 || len(fd.got[0]) != 1 || fd.got[0][0] != "new-file-id" {
		t.Fatalf("deleter calls = %+v", fd.got)
	}
	if len(stor.deleted) != 0 {
		t.Fatalf("blob deleted: %v", stor.deleted)
	}
}

func TestAddFromLibrary_LongMimeFallsBack(t *testing.T) {
	store := &mockStore{}
	h := files.NewHandlerWithEnqueuer(store, &mockStorage{}, noopChunks(), &optEnqueuer{})
	uf := libFile("a")
	uf.Mime = strings.Repeat("x", 51)
	h.SetLibrary(&fakeLibrary{known: map[string]*userfiles.UserFile{"a": uf}})
	h.AddFromLibrary(httptest.NewRecorder(), fromLibraryReq(`{"userFileIds":["a"]}`))
	if len(store.created) != 1 || store.created[0].Type != "application/octet-stream" {
		t.Fatalf("%+v", store.created)
	}
}

// ---- Upload reroute ----

func libUploadHandler(store *mockStore, stor *recStorage, enq enqueuer, lib *fakeLibrary) *files.Handler {
	h := files.NewHandlerWithEnqueuer(store, stor, noopChunks(), enq)
	h.SetLibrary(lib)
	return h
}

func doLibUpload(h *files.Handler) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "document.pdf")
	_, _ = fw.Write([]byte("PDF content"))
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/kb/kb-1/files", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.SetPathValue("id", "kb-1")
	req = withKBAccess(withUser(req, ownerUser()), defaultKB())
	rr := httptest.NewRecorder()
	h.Upload(rr, req)
	return rr
}

func TestUpload_ViaLibrary_Success(t *testing.T) {
	store := &mockStore{}
	stor := &recStorage{}
	enq := &optEnqueuer{}
	h := libUploadHandler(store, stor, enq, &fakeLibrary{ingested: libFile("a")})
	rr := doLibUpload(h)
	if rr.Code != http.StatusCreated {
		t.Fatalf("%d %s", rr.Code, rr.Body.String())
	}
	if len(store.created) != 1 {
		t.Fatalf("created %+v", store.created)
	}
	c := store.created[0]
	if c.UserFileID != "a" || c.StoragePath != libPath+"/a" || c.Name != "document.pdf" || c.UploadedBy != "user-1" || c.Origin != "upload" {
		t.Fatalf("CreateFile = %+v", c)
	}
	var out map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	if out["userFileId"] != "a" {
		t.Fatalf("response lacks userFileId: %v", out)
	}
	var p jobs.FileProcessingPayload
	_ = json.Unmarshal(enq.tasks[0].Payload(), &p)
	if p.FilePath != libPath+"/a" || p.OriginalName != "document.pdf" {
		t.Fatalf("payload %+v", p)
	}
	if p.UserFileID != "a" {
		t.Fatalf("payload UserFileID = %q, want %q", p.UserFileID, "a")
	}
	if len(stor.deleted) != 0 {
		t.Fatal("deleted something")
	}
}

func TestUpload_ViaLibrary_AlreadyInKB(t *testing.T) {
	store := &mockStore{copies: map[string]string{"a": "existing-row"}}
	h := libUploadHandler(store, &recStorage{}, &optEnqueuer{}, &fakeLibrary{ingested: libFile("a")})
	rr := doLibUpload(h)
	var out map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	if rr.Code != http.StatusConflict || out["error"] != "already_in_kb" || out["fileId"] != "existing-row" {
		t.Fatalf("%d %s", rr.Code, rr.Body.String())
	}
	if len(store.created) != 0 {
		t.Fatal("created a row")
	}
}

func TestUpload_ViaLibrary_Quota(t *testing.T) {
	h := libUploadHandler(&mockStore{}, &recStorage{}, &optEnqueuer{},
		&fakeLibrary{ingestErr: &userfiles.QuotaError{UsedBytes: 5, QuotaBytes: 9}})
	rr := doLibUpload(h)
	var out map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	if rr.Code != http.StatusRequestEntityTooLarge || out["error"] != "quota_exceeded" || out["usedBytes"] != float64(5) || out["quotaBytes"] != float64(9) {
		t.Fatalf("%d %s", rr.Code, rr.Body.String())
	}
}

func TestUpload_ViaLibrary_EnqueueFailureKeepsBlob(t *testing.T) {
	store := &mockStore{}
	stor := &recStorage{}
	h := libUploadHandler(store, stor, &failingEnqueuer{}, &fakeLibrary{ingested: libFile("a")})
	rr := doLibUpload(h)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("%d", rr.Code)
	}
	if len(stor.deleted) != 0 {
		t.Fatalf("library blob deleted: %v", stor.deleted)
	}
	if len(store.deletedIDs) != 1 {
		t.Fatalf("files row not removed: %v", store.deletedIDs)
	}
}

func TestUpload_ViaLibrary_IngestFailure500(t *testing.T) {
	h := libUploadHandler(&mockStore{}, &recStorage{}, &optEnqueuer{}, &fakeLibrary{ingestErr: errors.New("boom")})
	if rr := doLibUpload(h); rr.Code != http.StatusInternalServerError {
		t.Fatalf("%d", rr.Code)
	}
}
