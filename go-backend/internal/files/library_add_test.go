package files_test

import (
	"context"
	"errors"
	"testing"

	"github.com/justrag/go-backend/internal/files"
	"github.com/justrag/go-backend/internal/userfiles"
)

func TestAddLibraryFiles_NotOwnedIsErrorWithoutRow(t *testing.T) {
	store := &mockStore{}
	enq := &optEnqueuer{}
	lib := &fakeLibrary{known: map[string]*userfiles.UserFile{}} // "theirs" is not the caller's
	h := files.NewHandlerWithEnqueuer(store, &mockStorage{}, noopChunks(), enq)
	h.SetLibrary(lib)

	res, err := h.AddLibraryFiles(context.Background(), "user-1", "kb-1", false, []string{"theirs"})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(res) != 1 || res[0] != (files.AddResult{UserFileID: "theirs", Status: "error", Error: "not_found"}) {
		t.Fatalf("results = %+v", res)
	}
	if len(store.created) != 0 || len(enq.tasks) != 0 {
		t.Fatalf("side effects: created=%d tasks=%d", len(store.created), len(enq.tasks))
	}
	if len(lib.getUsers) != 1 || lib.getUsers[0] != "user-1" {
		t.Fatalf("library lookups must be owner-scoped: %v", lib.getUsers)
	}
}

func TestAddLibraryFiles_StatusesAndGlobalCap(t *testing.T) {
	store := &mockStore{copies: map[string]string{"dup": "row"}, kbFileLimits: &files.KBFileLimits{FileCount: 500}}
	h := files.NewHandlerWithEnqueuer(store, &mockStorage{}, noopChunks(), &optEnqueuer{})
	h.SetLibrary(&fakeLibrary{known: map[string]*userfiles.UserFile{"a": libFile("a"), "dup": libFile("dup")}})

	// isGlobal=true lifts the cap to 1000, so 500 existing files still fit.
	res, err := h.AddLibraryFiles(context.Background(), "user-1", "kb-9", true, []string{"a", "dup"})
	if err != nil {
		t.Fatal(err)
	}
	want := []files.AddResult{
		{UserFileID: "a", FileID: "new-file-id", Status: "added"},
		{UserFileID: "dup", Status: "duplicate"},
	}
	if len(res) != 2 || res[0] != want[0] || res[1] != want[1] {
		t.Fatalf("results = %+v", res)
	}
	if store.created[0].KbID != "kb-9" {
		t.Fatalf("kb = %q", store.created[0].KbID)
	}

	// Same state, user KB: the 500 cap is reached.
	store2 := &mockStore{kbFileLimits: &files.KBFileLimits{FileCount: 500}}
	h2 := files.NewHandlerWithEnqueuer(store2, &mockStorage{}, noopChunks(), &optEnqueuer{})
	h2.SetLibrary(&fakeLibrary{known: map[string]*userfiles.UserFile{"a": libFile("a")}})
	res, err = h2.AddLibraryFiles(context.Background(), "user-1", "kb-9", false, []string{"a"})
	if err != nil || len(res) != 1 || res[0] != (files.AddResult{UserFileID: "a", Status: "error", Error: "kb_full"}) {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

func TestAddLibraryFiles_HardErrorIsReturned(t *testing.T) {
	h := files.NewHandlerWithEnqueuer(&mockStore{copyErr: errors.New("db")}, &mockStorage{}, noopChunks(), &optEnqueuer{})
	h.SetLibrary(&fakeLibrary{known: map[string]*userfiles.UserFile{"a": libFile("a")}})
	if _, err := h.AddLibraryFiles(context.Background(), "user-1", "kb-1", false, []string{"a"}); err == nil {
		t.Fatal("want error")
	}
}

func TestAddLibraryFiles_Bounds(t *testing.T) {
	h := files.NewHandlerWithEnqueuer(&mockStore{}, &mockStorage{}, noopChunks(), &optEnqueuer{})
	if _, err := h.AddLibraryFiles(context.Background(), "user-1", "kb-1", false, []string{"a"}); !errors.Is(err, files.ErrLibraryUnavailable) {
		t.Fatalf("no library: err = %v", err)
	}
	h.SetLibrary(&fakeLibrary{})
	if _, err := h.AddLibraryFiles(context.Background(), "user-1", "kb-1", false, nil); err == nil {
		t.Fatal("empty ids: want error")
	}
	if _, err := h.AddLibraryFiles(context.Background(), "user-1", "kb-1", false, make([]string, 101)); err == nil {
		t.Fatal("101 ids: want error")
	}
}

func TestAddLibraryFiles_EmptyUserID(t *testing.T) {
	store := &mockStore{}
	lib := &fakeLibrary{known: map[string]*userfiles.UserFile{"a": libFile("a")}}
	h := files.NewHandlerWithEnqueuer(store, &mockStorage{}, noopChunks(), &optEnqueuer{})
	h.SetLibrary(lib)
	res, err := h.AddLibraryFiles(context.Background(), "", "kb-1", false, []string{"a"})
	if !errors.Is(err, files.ErrInvalidUser) || res != nil {
		t.Fatalf("res=%+v err=%v, want ErrInvalidUser", res, err)
	}
	if len(lib.getUsers) != 0 || len(store.created) != 0 {
		t.Fatalf("side effects: lookups=%v created=%d", lib.getUsers, len(store.created))
	}
}
