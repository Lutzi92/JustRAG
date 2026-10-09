package files_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/justrag/go-backend/internal/files"
	"github.com/justrag/go-backend/internal/kbaccess"
	"github.com/justrag/go-backend/internal/userfiles"
)

// Pinning tests for POST /api/kb/{id}/files/from-library: exact status codes
// and bodies on every path, so extracting AddLibraryFiles stays
// byte-identical over HTTP.

func TestAddFromLibrary_Pin_ExactBodies(t *testing.T) {
	two := map[string]*userfiles.UserFile{"a": libFile("a"), "b": libFile("b")}
	cases := []struct {
		name     string
		store    *mockStore
		enq      enqueuer
		lib      *fakeLibrary
		noLib    bool
		noUser   bool
		body     string
		wantCode int
		wantBody string
	}{
		{name: "success mixed", store: &mockStore{copies: map[string]string{"b": "row-b"}}, enq: &optEnqueuer{},
			lib: &fakeLibrary{known: two}, body: `{"userFileIds":["a","b","x"]}`, wantCode: 200,
			wantBody: `{"added":[{"fileId":"new-file-id","userFileId":"a"}],"skipped":[{"userFileId":"b","reason":"already_in_kb"},{"userFileId":"x","reason":"not_found"}]}`},
		{name: "nothing added", store: &mockStore{}, enq: &optEnqueuer{},
			lib: &fakeLibrary{}, body: `{"userFileIds":["x"]}`, wantCode: 200,
			wantBody: `{"added":[],"skipped":[{"userFileId":"x","reason":"not_found"}]}`},
		{name: "no user", noUser: true, store: &mockStore{}, enq: &optEnqueuer{}, lib: &fakeLibrary{},
			body: `{"userFileIds":["a"]}`, wantCode: 401, wantBody: `{"error":"Authentication required"}`},
		{name: "no library", noLib: true, store: &mockStore{}, enq: &optEnqueuer{},
			body: `{}`, wantCode: 404, wantBody: `{"error":"Not found"}`},
		{name: "limits error", store: &mockStore{kbFileLimitsErr: errors.New("db")}, enq: &optEnqueuer{},
			lib: &fakeLibrary{known: two}, body: `{"userFileIds":["a"]}`, wantCode: 500, wantBody: `{"error":"Failed to check KB limits"}`},
		{name: "library get error", store: &mockStore{}, enq: &optEnqueuer{},
			lib: &fakeLibrary{getErr: errors.New("db")}, body: `{"userFileIds":["a"]}`, wantCode: 500, wantBody: `{"error":"Internal Server Error"}`},
		{name: "kb copy error", store: &mockStore{copyErr: errors.New("db")}, enq: &optEnqueuer{},
			lib: &fakeLibrary{known: two}, body: `{"userFileIds":["a"]}`, wantCode: 500, wantBody: `{"error":"Internal Server Error"}`},
		{name: "create error", store: &mockStore{createErr: errors.New("db")}, enq: &optEnqueuer{},
			lib: &fakeLibrary{known: two}, body: `{"userFileIds":["a"]}`, wantCode: 500, wantBody: `{"error":"Failed to create file record"}`},
		{name: "enqueue error", store: &mockStore{}, enq: &failingEnqueuer{},
			lib: &fakeLibrary{known: two}, body: `{"userFileIds":["a"]}`, wantCode: 500, wantBody: `{"error":"failed to queue file for processing"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := files.NewHandlerWithEnqueuer(tc.store, &mockStorage{}, noopChunks(), tc.enq)
			if !tc.noLib {
				h.SetLibrary(tc.lib)
			}
			req := fromLibraryReq(tc.body)
			if tc.noUser {
				req = withKBAccess(httptest.NewRequest(http.MethodPost, "/api/kb/kb-1/files/from-library", req.Body), defaultKB())
				req.SetPathValue("id", "kb-1")
			}
			rr := httptest.NewRecorder()
			h.AddFromLibrary(rr, req)
			if rr.Code != tc.wantCode || rr.Body.String() != tc.wantBody+"\n" {
				t.Fatalf("got %d %q, want %d %q", rr.Code, rr.Body.String(), tc.wantCode, tc.wantBody)
			}
		})
	}
}

// A global KB gets the higher file-count cap; the KB id comes from the
// access context, not the path.
func TestAddFromLibrary_Pin_GlobalKBCapAndContextKBID(t *testing.T) {
	store := &mockStore{kbFileLimits: &files.KBFileLimits{FileCount: 500}}
	lib := &fakeLibrary{known: map[string]*userfiles.UserFile{"a": libFile("a")}}
	h := files.NewHandlerWithEnqueuer(store, &mockStorage{}, noopChunks(), &optEnqueuer{})
	h.SetLibrary(lib)
	req := fromLibraryReq(`{"userFileIds":["a"]}`)
	req.SetPathValue("id", "path-kb")
	req = withKBAccess(req, &kbaccess.KnowledgeBase{ID: "ctx-kb", IsGlobal: true})
	rr := httptest.NewRecorder()
	h.AddFromLibrary(rr, req)
	if rr.Code != 200 || len(store.created) != 1 || store.created[0].KbID != "ctx-kb" {
		t.Fatalf("%d %s created=%+v", rr.Code, rr.Body.String(), store.created)
	}
	if len(lib.getUsers) != 1 || lib.getUsers[0] != "user-1" {
		t.Fatalf("library lookups = %v", lib.getUsers)
	}
}
