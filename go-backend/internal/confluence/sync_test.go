package confluence

import (
	"context"
	"io"
	"reflect"
	"testing"

	"github.com/justrag/go-backend/internal/storage"
)

// ---------------------------------------------------------------------------
// Fakes for deleteConfluenceFiles.
// ---------------------------------------------------------------------------

// fakeConfluenceStore implements ConfluenceStore; only DeleteFilesByIDs is
// exercised by deleteConfluenceFiles, but Go requires every method.
type fakeConfluenceStore struct {
	events     *[]string
	deletedIDs []string
}

var _ ConfluenceStore = (*fakeConfluenceStore)(nil)

func (s *fakeConfluenceStore) GetConfluenceConnectionByUserID(context.Context, string) (*ConfluenceConnectionRow, error) {
	return nil, nil
}
func (s *fakeConfluenceStore) GetConfluenceConnectionByID(context.Context, string) (*ConfluenceConnectionRow, error) {
	return nil, nil
}
func (s *fakeConfluenceStore) DeleteConfluenceConnection(context.Context, string) error { return nil }
func (s *fakeConfluenceStore) CreateConfluenceConnection(context.Context, string, string, *string) (*ConfluenceConnectionRow, error) {
	return nil, nil
}
func (s *fakeConfluenceStore) UpdateConfluenceConnection(context.Context, string, ConfluenceConnectionUpdate) (*ConfluenceConnectionRow, error) {
	return nil, nil
}
func (s *fakeConfluenceStore) CreateConfluenceSource(context.Context, string, string, string, *string, *string, bool, string) (*ConfluenceSourceRow, error) {
	return nil, nil
}
func (s *fakeConfluenceStore) ListConfluenceSources(context.Context, string) ([]ConfluenceSourceRow, error) {
	return nil, nil
}
func (s *fakeConfluenceStore) GetConfluenceSourceByID(context.Context, string) (*ConfluenceSourceRow, error) {
	return nil, nil
}
func (s *fakeConfluenceStore) UpdateConfluenceSource(context.Context, string, ConfluenceSourceUpdate) (*ConfluenceSourceRow, error) {
	return nil, nil
}
func (s *fakeConfluenceStore) DeleteConfluenceSource(context.Context, string) error { return nil }
func (s *fakeConfluenceStore) CreateConfluenceFile(context.Context, CreateConfluenceFileData) (*ConfluenceFileRow, error) {
	return nil, nil
}
func (s *fakeConfluenceStore) GetFilesByConfluenceSourceID(context.Context, string) ([]ConfluenceFileRow, error) {
	return nil, nil
}
func (s *fakeConfluenceStore) GetConfluenceSourceIDForFile(context.Context, string) (string, error) {
	return "", nil
}
func (s *fakeConfluenceStore) DeleteFilesByIDs(_ context.Context, ids []string) error {
	if s.events != nil {
		*s.events = append(*s.events, "delete")
	}
	s.deletedIDs = append(s.deletedIDs, ids...)
	return nil
}
func (s *fakeConfluenceStore) GetConfluenceSourceFileProgress(context.Context, string) (int, int, error) {
	return 0, 0, nil
}
func (s *fakeConfluenceStore) GetSiteConfigValue(context.Context, string) (*string, error) {
	return nil, nil
}

// fakeStorage implements storage.Storage as pure no-ops.
type fakeStorage struct{}

var _ storage.Storage = fakeStorage{}

func (fakeStorage) StoreFile(context.Context, string, []byte, string) error { return nil }
func (fakeStorage) StoreFileFromReader(context.Context, string, io.Reader, string) error {
	return nil
}
func (fakeStorage) ReadFile(context.Context, string) ([]byte, error)              { return nil, nil }
func (fakeStorage) ReadFileStream(context.Context, string) (io.ReadCloser, error) { return nil, nil }
func (fakeStorage) DeleteFile(context.Context, string) error                      { return nil }
func (fakeStorage) DeleteFiles(context.Context, []string) error                   { return nil }
func (fakeStorage) DeleteDirectory(context.Context, string) error                 { return nil }
func (fakeStorage) FileExists(context.Context, string) (bool, error)              { return false, nil }
func (fakeStorage) IsS3() bool                                                    { return false }

// fakeChunkDeleter implements ChunkDeleter as a no-op.
type fakeChunkDeleter struct{}

func (fakeChunkDeleter) DeleteChunksByFileIDsAllDims(context.Context, []string) error { return nil }

// fakeTableDropper implements TableDropper, recording each call (and its
// position in a shared event log) so tests can assert both "called once per
// deleted id" and "before the row delete".
type fakeTableDropper struct {
	events  *[]string
	dropped []string
}

func (d *fakeTableDropper) DropTablesForFile(_ context.Context, fileID string) error {
	if d.events != nil {
		*d.events = append(*d.events, "drop:"+fileID)
	}
	d.dropped = append(d.dropped, fileID)
	return nil
}

// TestDeleteConfluenceFilesDropsTablesBeforeDeletingRows pins the Phase-3
// carry: deleteConfluenceFiles must drop a file's materialised spreadsheet
// tables for EVERY deleted id before it deletes the files rows, or the
// tabular_catalog row that indexes those tables is gone (files row deleted)
// while the tables themselves are still there, unreachably orphaned.
func TestDeleteConfluenceFilesDropsTablesBeforeDeletingRows(t *testing.T) {
	var events []string
	dropper := &fakeTableDropper{events: &events}
	store := &fakeConfluenceStore{events: &events}
	deps := SyncDeps{
		Store:        store,
		Storage:      fakeStorage{},
		ChunkService: fakeChunkDeleter{},
		TableDropper: dropper,
	}
	files := []ConfluenceFileRow{{ID: "file-1"}, {ID: "file-2"}}

	if err := deleteConfluenceFiles(context.Background(), deps, files); err != nil {
		t.Fatalf("deleteConfluenceFiles: %v", err)
	}

	wantEvents := []string{"drop:file-1", "drop:file-2", "delete"}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Errorf("event order = %v, want %v", events, wantEvents)
	}
	wantDropped := []string{"file-1", "file-2"}
	if !reflect.DeepEqual(dropper.dropped, wantDropped) {
		t.Errorf("dropped = %v, want %v (one call per deleted id)", dropper.dropped, wantDropped)
	}
	if !reflect.DeepEqual(store.deletedIDs, wantDropped) {
		t.Errorf("deletedIDs = %v, want %v", store.deletedIDs, wantDropped)
	}
}

// TestDeleteConfluenceFilesNilDropperIsNoop pins the nil-safety half: a
// deployment without a main pool (or a caller that never wired one) must
// still delete the files rows, unaffected.
func TestDeleteConfluenceFilesNilDropperIsNoop(t *testing.T) {
	store := &fakeConfluenceStore{}
	deps := SyncDeps{
		Store:        store,
		Storage:      fakeStorage{},
		ChunkService: fakeChunkDeleter{},
		// TableDropper deliberately left nil.
	}
	files := []ConfluenceFileRow{{ID: "file-1"}}

	if err := deleteConfluenceFiles(context.Background(), deps, files); err != nil {
		t.Fatalf("deleteConfluenceFiles: %v", err)
	}
	if want := []string{"file-1"}; !reflect.DeepEqual(store.deletedIDs, want) {
		t.Errorf("deletedIDs = %v, want %v", store.deletedIDs, want)
	}
}

// TestDeleteConfluenceFilesEmptyIsNoop pins the existing early return: no
// files means no dropper call and no store call.
func TestDeleteConfluenceFilesEmptyIsNoop(t *testing.T) {
	var events []string
	dropper := &fakeTableDropper{events: &events}
	store := &fakeConfluenceStore{events: &events}
	deps := SyncDeps{
		Store:        store,
		Storage:      fakeStorage{},
		ChunkService: fakeChunkDeleter{},
		TableDropper: dropper,
	}

	if err := deleteConfluenceFiles(context.Background(), deps, nil); err != nil {
		t.Fatalf("deleteConfluenceFiles: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("events = %v, want none", events)
	}
}
