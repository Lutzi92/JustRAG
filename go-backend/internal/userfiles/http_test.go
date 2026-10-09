package userfiles

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
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/cascade"
	"github.com/justrag/go-backend/internal/storage"
)

// ---- delete ---------------------------------------------------------------

type spyDeleter struct {
	calls [][2]string
	err   error
}

func (s *spyDeleter) DeleteUserFile(_ context.Context, owner, id string) error {
	s.calls = append(s.calls, [2]string{owner, id})
	return s.err
}

func deleteHarness(d FileDeleter) *harness {
	h := newHarness(0)
	hd := NewHandler(h.store, nil, h.stor, fakeLimits{}, fakeQuota(0))
	hd.SetDeleter(d)
	h.mux.HandleFunc("DELETE /api/library/files/{id}", hd.Delete)
	return h
}

func TestDelete(t *testing.T) {
	id := "11111111-1111-1111-1111-111111111111"
	cases := []struct {
		name      string
		id        string
		err       error
		want      int
		wantCalls int
	}{
		{"ok", id, nil, http.StatusNoContent, 1},
		{"not found", id, cascade.ErrUserFileNotFound, http.StatusNotFound, 1},
		{"other error", id, errors.New("boom"), http.StatusInternalServerError, 1},
		{"malformed id", "nope", nil, http.StatusNotFound, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sp := &spyDeleter{err: c.err}
			h := deleteHarness(sp)
			rec := h.do(userA(), httptest.NewRequest("DELETE", "/api/library/files/"+c.id, nil))
			if rec.Code != c.want {
				t.Fatalf("status = %d, want %d", rec.Code, c.want)
			}
			if len(sp.calls) != c.wantCalls {
				t.Fatalf("calls = %v", sp.calls)
			}
			if c.wantCalls == 1 && sp.calls[0] != [2]string{userA(), c.id} {
				t.Errorf("call = %v", sp.calls[0])
			}
		})
	}
	t.Run("unauthenticated", func(t *testing.T) {
		sp := &spyDeleter{}
		h := deleteHarness(sp)
		rec := h.do("", httptest.NewRequest("DELETE", "/api/library/files/"+id, nil))
		if rec.Code != http.StatusUnauthorized || len(sp.calls) != 0 {
			t.Fatalf("status = %d calls=%v", rec.Code, sp.calls)
		}
	})
}

// ---- fakes ----------------------------------------------------------------

type fakeStore struct {
	mu       sync.Mutex
	rows     map[string]*UserFile // by id
	override map[string]*int64
}

func newFakeStore() *fakeStore {
	return &fakeStore{rows: map[string]*UserFile{}, override: map[string]*int64{}}
}

func (s *fakeStore) Insert(_ context.Context, f NewUserFile) (*UserFile, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.rows {
		if r.OwnerUserID == f.OwnerUserID && r.SHA256 == f.SHA256 {
			c := *r
			return &c, false, nil
		}
	}
	r := &UserFile{ID: f.ID, OwnerUserID: f.OwnerUserID, Name: f.Name, Mime: f.Mime, Size: f.Size,
		SHA256: f.SHA256, StoragePath: f.StoragePath, CreatedAt: time.Now(), KBs: []KBLink{}}
	s.rows[f.ID] = r
	c := *r
	return &c, true, nil
}

func (s *fakeStore) Get(_ context.Context, owner, id string) (*UserFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[id]
	if !ok || r.OwnerUserID != owner {
		return nil, ErrNotFound
	}
	c := *r
	return &c, nil
}

func (s *fakeStore) List(_ context.Context, owner string, limit, offset int) ([]UserFile, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []UserFile
	for _, r := range s.rows {
		if r.OwnerUserID == owner {
			out = append(out, *r)
		}
	}
	total := len(out)
	if offset > len(out) {
		offset = len(out)
	}
	out = out[offset:]
	if limit < len(out) {
		out = out[:limit]
	}
	return out, total, nil
}

func (s *fakeStore) Rename(_ context.Context, owner, id, name string) (*UserFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[id]
	if !ok || r.OwnerUserID != owner {
		return nil, ErrNotFound
	}
	if strings.TrimSpace(name) == "" {
		return nil, ErrInvalidName
	}
	r.Name = name
	c := *r
	return &c, nil
}

func (s *fakeStore) Usage(_ context.Context, owner, id string) ([]KBUsage, error) {
	if _, err := s.Get(context.Background(), owner, id); err != nil {
		return nil, err
	}
	return []KBUsage{{ID: "kb1", Name: "KB", Visibility: "private", MemberCount: 1}}, nil
}

func (s *fakeStore) UsedBytes(_ context.Context, owner string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for _, r := range s.rows {
		if r.OwnerUserID == owner {
			n += r.Size
		}
	}
	return n, nil
}

func (s *fakeStore) QuotaOverride(_ context.Context, owner string) (*int64, error) {
	return s.override[owner], nil
}

func (s *fakeStore) ListIDsByOwner(context.Context, string) ([]string, error) { return nil, nil }

type fakeStorage struct {
	mu      sync.Mutex
	blobs   map[string][]byte
	stored  int
	deleted []string
}

func newFakeStorage() *fakeStorage { return &fakeStorage{blobs: map[string][]byte{}} }

func (f *fakeStorage) StoreFile(_ context.Context, p string, c []byte, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.blobs[p] = c
	f.stored++
	return nil
}
func (f *fakeStorage) StoreFileFromReader(_ context.Context, p string, r io.Reader, _ string) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	return f.StoreFile(context.Background(), p, b, "")
}
func (f *fakeStorage) ReadFile(_ context.Context, p string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.blobs[p]
	if !ok {
		return nil, errors.New("missing")
	}
	return b, nil
}
func (f *fakeStorage) ReadFileStream(ctx context.Context, p string) (io.ReadCloser, error) {
	b, err := f.ReadFile(ctx, p)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}
func (f *fakeStorage) DeleteFile(ctx context.Context, p string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.blobs, p)
	f.deleted = append(f.deleted, p)
	return nil
}
func (f *fakeStorage) DeleteFiles(ctx context.Context, ps []string) error {
	for _, p := range ps {
		_ = f.DeleteFile(ctx, p)
	}
	return nil
}
func (f *fakeStorage) DeleteDirectory(context.Context, string) error { return nil }
func (f *fakeStorage) FileExists(_ context.Context, p string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.blobs[p]
	return ok, nil
}
func (f *fakeStorage) IsS3() bool                                                 { return false }
func (f *fakeStorage) List(context.Context, string) ([]storage.ObjectInfo, error) { return nil, nil }

type fakeQuota int64

func (q fakeQuota) GlobalQuotaBytes(context.Context) int64 { return int64(q) }

type fakeLimits struct{}

func (fakeLimits) TabularMaxFileBytes(context.Context) int { return 500 << 20 }

// ---- harness --------------------------------------------------------------

type harness struct {
	store *fakeStore
	stor  *fakeStorage
	mux   *http.ServeMux
}

func newHarness(globalQuota int64) *harness {
	st, sg := newFakeStore(), newFakeStorage()
	q := fakeQuota(globalQuota)
	h := NewHandler(st, NewIngester(st, sg, q), sg, fakeLimits{}, q)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/library/files", h.List)
	mux.HandleFunc("POST /api/library/files", h.Upload)
	mux.HandleFunc("GET /api/library/files/{id}", h.Get)
	mux.HandleFunc("PATCH /api/library/files/{id}", h.Rename)
	mux.HandleFunc("GET /api/library/files/{id}/download", h.Download)
	mux.HandleFunc("GET /api/library/files/{id}/usage", h.Usage)
	mux.HandleFunc("GET /api/library/quota", h.Quota)
	return &harness{st, sg, mux}
}

func (h *harness) do(user string, req *http.Request) *httptest.ResponseRecorder {
	if user != "" {
		req = req.WithContext(auth.WithUser(req.Context(), &auth.Claims{ID: user}))
	}
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	return rec
}

func uploadReq(t *testing.T, name string, body []byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	fw.Write(body)
	mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/library/files", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

func userA() string { return "00000000-0000-0000-0000-00000000000a" }
func userB() string { return "00000000-0000-0000-0000-00000000000b" }

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("bad json %q: %v", rec.Body.String(), err)
	}
	return m
}

// ---- tests ----------------------------------------------------------------

func TestUploadCreatedThenDeduplicated(t *testing.T) {
	h := newHarness(0)
	rec := h.do(userA(), uploadReq(t, "a.txt", []byte("hello")))
	if rec.Code != http.StatusCreated {
		t.Fatalf("first upload: %d %s", rec.Code, rec.Body)
	}
	first := decode(t, rec)
	if _, ok := first["deduplicated"]; ok {
		t.Fatal("created upload must not carry deduplicated")
	}
	if len(h.stor.blobs) != 1 {
		t.Fatalf("want 1 blob, got %d", len(h.stor.blobs))
	}

	rec = h.do(userA(), uploadReq(t, "b.txt", []byte("hello")))
	if rec.Code != http.StatusOK {
		t.Fatalf("dedup upload: %d %s", rec.Code, rec.Body)
	}
	second := decode(t, rec)
	if second["deduplicated"] != true || second["id"] != first["id"] {
		t.Fatalf("want dedup of first row, got %v", second)
	}
	if len(h.stor.deleted) != 1 || len(h.stor.blobs) != 1 {
		t.Fatalf("second blob must be deleted: deleted=%v blobs=%d", h.stor.deleted, len(h.stor.blobs))
	}
}

func TestUploadRejectsDangerousExtension(t *testing.T) {
	h := newHarness(0)
	rec := h.do(userA(), uploadReq(t, "x.exe", []byte("MZ")))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code %d", rec.Code)
	}
	if got := decode(t, rec)["error"]; got != "File type not allowed" {
		t.Fatalf("error %v", got)
	}
}

func TestUnauthenticated401(t *testing.T) {
	h := newHarness(0)
	rec := h.do("", uploadReq(t, "a.txt", []byte("x")))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code %d", rec.Code)
	}
	rec = h.do("", httptest.NewRequest(http.MethodGet, "/api/library/quota", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("quota code %d", rec.Code)
	}
}

func TestUploadQuota(t *testing.T) {
	i64 := func(v int64) *int64 { return &v }
	cases := []struct {
		name     string
		global   int64
		override *int64
		used     int64
		size     int
		wantCode int
	}{
		{"global 0 unlimited", 0, nil, 1 << 40, 10, http.StatusCreated},
		{"exactly at limit", 100, nil, 60, 40, http.StatusCreated},
		{"one over", 100, nil, 60, 41, http.StatusRequestEntityTooLarge},
		{"override beats global", 100, i64(1000), 60, 500, http.StatusCreated},
		{"override 0 = unlimited", 100, i64(0), 60, 500, http.StatusCreated},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(tc.global)
			h.store.override[userA()] = tc.override
			id := uuid.NewString()
			h.store.rows[id] = &UserFile{ID: id, OwnerUserID: userA(), Size: tc.used, SHA256: "pre"}
			rec := h.do(userA(), uploadReq(t, "a.txt", bytes.Repeat([]byte("z"), tc.size)))
			if rec.Code != tc.wantCode {
				t.Fatalf("code %d body %s", rec.Code, rec.Body)
			}
			if tc.wantCode == http.StatusRequestEntityTooLarge {
				m := decode(t, rec)
				if m["error"] != "quota_exceeded" || m["usedBytes"] != float64(tc.used) || m["quotaBytes"] != float64(tc.global) {
					t.Fatalf("bad quota body %v", m)
				}
				if h.stor.stored != 0 {
					t.Fatal("blob written despite quota rejection")
				}
			}
		})
	}
}

func seed(t *testing.T, h *harness, owner string, body string) string {
	t.Helper()
	rec := h.do(owner, uploadReq(t, "doc.txt", []byte(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("seed: %d %s", rec.Code, rec.Body)
	}
	return decode(t, rec)["id"].(string)
}

func TestOwnerIsolation(t *testing.T) {
	h := newHarness(0)
	id := seed(t, h, userA(), "secret")
	base := "/api/library/files/"
	reqs := map[string]func(id string) *http.Request{
		"get":      func(id string) *http.Request { return httptest.NewRequest(http.MethodGet, base+id, nil) },
		"download": func(id string) *http.Request { return httptest.NewRequest(http.MethodGet, base+id+"/download", nil) },
		"usage":    func(id string) *http.Request { return httptest.NewRequest(http.MethodGet, base+id+"/usage", nil) },
		"patch": func(id string) *http.Request {
			return httptest.NewRequest(http.MethodPatch, base+id, strings.NewReader(`{"name":"x"}`))
		},
	}
	for name, mk := range reqs {
		if rec := h.do(userB(), mk(id)); rec.Code != http.StatusNotFound {
			t.Errorf("%s as B: %d", name, rec.Code)
		}
		if rec := h.do(userA(), mk("not-a-uuid")); rec.Code != http.StatusNotFound {
			t.Errorf("%s malformed id: %d", name, rec.Code)
		}
	}
	if rec := h.do(userA(), reqs["get"](id)); rec.Code != http.StatusOK {
		t.Errorf("owner get: %d", rec.Code)
	}
}

func TestListShapeAndClamp(t *testing.T) {
	h := newHarness(0)
	rec := h.do(userA(), httptest.NewRequest(http.MethodGet, "/api/library/files", nil))
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"items":[],"total":0}` {
		t.Fatalf("empty list: %d %s", rec.Code, rec.Body)
	}
	seed(t, h, userA(), "one")
	seed(t, h, userA(), "two")
	rec = h.do(userA(), httptest.NewRequest(http.MethodGet, "/api/library/files?limit=1&offset=0", nil))
	m := decode(t, rec)
	if len(m["items"].([]any)) != 1 || m["total"] != float64(2) {
		t.Fatalf("limit=1: %v", m)
	}
	// Out-of-range limits clamp instead of erroring.
	for _, q := range []string{"limit=99999", "limit=0", "limit=-3", "limit=abc&offset=-1"} {
		rec = h.do(userA(), httptest.NewRequest(http.MethodGet, "/api/library/files?"+q, nil))
		if rec.Code != 200 {
			t.Errorf("%s: %d", q, rec.Code)
		}
	}
}

func TestRename(t *testing.T) {
	h := newHarness(0)
	id := seed(t, h, userA(), "r")
	p := func(body string) *httptest.ResponseRecorder {
		return h.do(userA(), httptest.NewRequest(http.MethodPatch, "/api/library/files/"+id, strings.NewReader(body)))
	}
	if rec := p(`{"name":"   "}`); rec.Code != 400 || decode(t, rec)["error"] != "invalid name" {
		t.Fatalf("empty: %d %s", rec.Code, rec.Body)
	}
	if rec := p(`not json`); rec.Code != 400 {
		t.Fatalf("bad json: %d", rec.Code)
	}
	rec := p(`{"name":"new.txt"}`)
	if rec.Code != 200 || decode(t, rec)["name"] != "new.txt" {
		t.Fatalf("rename: %d %s", rec.Code, rec.Body)
	}
}

func TestDownload(t *testing.T) {
	h := newHarness(0)
	rec := h.do(userA(), uploadReq(t, `we"ird ä.txt`, []byte("payload")))
	id := decode(t, rec)["id"].(string)
	rec = h.do(userA(), httptest.NewRequest(http.MethodGet, "/api/library/files/"+id+"/download", nil))
	if rec.Code != 200 || rec.Body.String() != "payload" {
		t.Fatalf("%d %q", rec.Code, rec.Body)
	}
	cd := rec.Header().Get("Content-Disposition")
	if !strings.HasPrefix(cd, "attachment; ") || !strings.Contains(cd, `filename="we_ird ä.txt"`) ||
		!strings.Contains(cd, "filename*=UTF-8''we%22ird%20%C3%A4.txt") {
		t.Fatalf("Content-Disposition %q", cd)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("Content-Type %q", ct)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing nosniff")
	}
}

func TestUsageAndQuotaEndpoints(t *testing.T) {
	h := newHarness(100)
	id := seed(t, h, userA(), "12345")
	rec := h.do(userA(), httptest.NewRequest(http.MethodGet, "/api/library/files/"+id+"/usage", nil))
	if rec.Code != 200 || len(decode(t, rec)["kbs"].([]any)) != 1 {
		t.Fatalf("usage: %d %s", rec.Code, rec.Body)
	}
	rec = h.do(userA(), httptest.NewRequest(http.MethodGet, "/api/library/quota", nil))
	m := decode(t, rec)
	if m["usedBytes"] != float64(5) || m["quotaBytes"] != float64(100) {
		t.Fatalf("quota: %v", m)
	}
}
