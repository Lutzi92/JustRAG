package files_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/justrag/go-backend/internal/files"
	"github.com/justrag/go-backend/internal/kbaccess"
)

// callLog records the ORDER of the two calls that C1 is about.
type callLog struct{ calls []string }

// orderedStore wraps mockStore so DeleteFileRecord lands in the shared log.
type orderedStore struct {
	*mockStore
	log *callLog
}

func (s *orderedStore) DeleteFileRecord(ctx context.Context, id string) error {
	s.log.calls = append(s.log.calls, "delete_file_record:"+id)
	return s.mockStore.DeleteFileRecord(ctx, id)
}

type fakeDropper struct {
	log *callLog
	err error
}

func (d *fakeDropper) DropTablesForFile(_ context.Context, fileID string) error {
	d.log.calls = append(d.log.calls, "drop_tables:"+fileID)
	return d.err
}

var _ files.TableDropper = (*fakeDropper)(nil)

func deleteHandlerFixture(t *testing.T, dropErr error) (*httptest.ResponseRecorder, *callLog) {
	t.Helper()
	ownerID := "user-1"
	fileInfo := &files.FileInfo{
		ID:          "file-abc",
		KbID:        "kb-1",
		Name:        "ledger.xlsx",
		StoragePath: strPtr("user-1/kb-1/ledger.xlsx"),
	}
	kb := &kbaccess.KnowledgeBase{ID: "kb-1", UserID: &ownerID, IsGlobal: false}

	log := &callLog{}
	store := &orderedStore{mockStore: &mockStore{file: fileInfo, kb: kb, role: kbaccess.RoleOwner}, log: log}
	h := files.NewHandler(store, &mockStorage{}, noopChunks())
	h.SetTableDropper(&fakeDropper{log: log, err: dropErr})

	req := newRequest(http.MethodDelete, "/api/files/file-abc")
	req.SetPathValue("id", "file-abc")
	req = withUser(req, ownerUser())

	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	return rr, log
}

// TestDelete_DropsTabularTablesBeforeTheFileRow pins C1/R20. tabular_catalog
// is keyed on the file id and is the ONLY index from a file to the physical
// `tabular.sheet_*` tables it materialised; those tables live outside the
// `files` foreign-key graph, so deleting the files row does not cascade them
// away — it makes them unreachable. The drop therefore has to happen BEFORE
// the row goes, not merely at some point during the handler.
func TestDelete_DropsTabularTablesBeforeTheFileRow(t *testing.T) {
	rr, log := deleteHandlerFixture(t, nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rr.Code)
	}
	want := []string{"drop_tables:file-abc", "delete_file_record:file-abc"}
	if len(log.calls) != len(want) {
		t.Fatalf("calls = %v, want %v", log.calls, want)
	}
	for i := range want {
		if log.calls[i] != want[i] {
			t.Fatalf("calls = %v, want %v", log.calls, want)
		}
	}
}

// A dropper failure must not abort the delete: the chunks and the blob are
// already gone by then, so refusing to remove the files row would strand the
// file in the UI with nothing behind it.
func TestDelete_TableDropFailureDoesNotBlockDelete(t *testing.T) {
	rr, log := deleteHandlerFixture(t, errors.New("boom"))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204 despite the drop failure, got %d", rr.Code)
	}
	if len(log.calls) != 2 || log.calls[1] != "delete_file_record:file-abc" {
		t.Fatalf("delete must still run after a drop failure: %v", log.calls)
	}
}

// The dropper is optional — a handler without one behaves exactly as before.
func TestDelete_WithoutTableDropperStillDeletes(t *testing.T) {
	ownerID := "user-1"
	store := &mockStore{
		file: &files.FileInfo{ID: "file-abc", KbID: "kb-1", Name: "x.pdf", StoragePath: strPtr("p")},
		kb:   &kbaccess.KnowledgeBase{ID: "kb-1", UserID: &ownerID},
		role: kbaccess.RoleOwner,
	}
	h := files.NewHandler(store, &mockStorage{}, noopChunks())
	req := newRequest(http.MethodDelete, "/api/files/file-abc")
	req.SetPathValue("id", "file-abc")
	req = withUser(req, ownerUser())
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rr.Code)
	}
	if len(store.deletedIDs) != 1 || store.deletedIDs[0] != "file-abc" {
		t.Fatalf("deletedIDs = %v", store.deletedIDs)
	}
}
