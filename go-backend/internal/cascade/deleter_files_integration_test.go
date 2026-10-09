//go:build integration

package cascade_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/cascade"
	"github.com/justrag/go-backend/internal/files"
	"github.com/justrag/go-backend/internal/storage"
	"github.com/justrag/go-backend/internal/vector"
)

type spyKGHook struct{ calls [][2]string }

func (s *spyKGHook) OnFileDeleted(_ context.Context, kbID, fileID string) {
	s.calls = append(s.calls, [2]string{kbID, fileID})
}

// seedLibraryFile inserts a user_files row for the owner plus a KB copy that
// shares its blob path, and returns (userFileID, kbCopyFileID, blobPath).
func seedLibraryFile(t *testing.T, pool *pgxpool.Pool, userID, kbID string) (ufID, fileID, blob string) {
	t.Helper()
	ctx := context.Background()
	blob = fmt.Sprintf("users/%s/lib-%d", userID, nextSerial())
	sha := strings.Repeat("a", 60) + fmt.Sprintf("%04d", nextSerial())
	if err := pool.QueryRow(ctx, `
		INSERT INTO user_files (owner_user_id, name, mime, size, sha256, storage_path)
		VALUES ($1::uuid, 'lib.txt', 'text/plain', 8, $2, $3)
		RETURNING id::text`, userID, sha, blob).Scan(&ufID); err != nil {
		t.Fatalf("insert user_files: %v", err)
	}
	// Runs before seedFixture's user cleanup (LIFO): user_files is RESTRICT.
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM files WHERE user_file_id = $1::uuid`, ufID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM user_files WHERE id = $1::uuid`, ufID)
	})
	if err := pool.QueryRow(ctx, `
		INSERT INTO files (kb_id, name, type, storage_path, user_file_id)
		VALUES ($1::uuid, 'lib.txt', 'text/plain', $2, $3::uuid)
		RETURNING id::text`, kbID, blob, ufID).Scan(&fileID); err != nil {
		t.Fatalf("insert library files row: %v", err)
	}
	return ufID, fileID, blob
}

func writeBlob(t *testing.T, stor storage.Storage, path string) {
	t.Helper()
	if err := stor.StoreFileFromReader(context.Background(), path, strings.NewReader("contents"), "text/plain"); err != nil {
		t.Fatalf("store blob %s: %v", path, err)
	}
}

func addIndex(t *testing.T, vp *pgxpool.Pool, kbID, fileID string) *vector.ChunkService {
	t.Helper()
	ctx := context.Background()
	svc := vector.NewChunkService(vp)
	emb := make([]float64, 8)
	for i := range emb {
		emb[i] = 1.0 / float64(i+1)
	}
	err := svc.AddDocumentChunks(ctx, fileID, []vector.ChunkInput{{
		Content: "hello", Embedding: emb, KbID: kbID, FileID: fileID,
		Metadata: map[string]any{"chunkIndex": 0}, NodeKind: "leaf",
	}}, 8, "simple")
	if err != nil {
		t.Fatalf("AddDocumentChunks: %v", err)
	}
	if _, err := svc.AddParentChunks(ctx, []vector.ParentChunkInput{{KbID: kbID, FileID: fileID, Content: "parent"}}); err != nil {
		t.Fatalf("AddParentChunks: %v", err)
	}
	t.Cleanup(func() {
		_ = svc.DeleteChunksByFileIDsAllDims(context.Background(), []string{fileID})
		_ = svc.DeleteParentChunksByFileID(context.Background(), fileID)
	})
	return svc
}

func vectorCount(t *testing.T, vp *pgxpool.Pool, table, fileID string) int {
	t.Helper()
	var n int
	if err := vp.QueryRow(context.Background(),
		fmt.Sprintf(`SELECT count(*) FROM %s WHERE file_id = $1::uuid`, table), fileID).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func TestDeleteFiles_RemovesFullIndexAndBlobForLegacyRow(t *testing.T) {
	mp, vp := openTestPools(t)
	_, kbID, fileIDs := seedFixture(t, mp, false)
	stor := localFS(t, mp, kbID)
	fileID := fileIDs[0]
	addIndex(t, vp, kbID, fileID)

	var path string
	if err := mp.QueryRow(context.Background(), `SELECT storage_path FROM files WHERE id=$1::uuid`, fileID).Scan(&path); err != nil {
		t.Fatal(err)
	}

	d := cascade.New(mp, vp, stor)
	spy := &spyInvalidator{}
	kg := &spyKGHook{}
	d.SetQueryCacheInvalidator(spy)
	d.SetKGFileHook(kg)

	if err := d.DeleteFiles(context.Background(), []string{fileID, "00000000-0000-0000-0000-000000000000"}); err != nil {
		t.Fatalf("DeleteFiles: %v", err)
	}

	assertCountZero(t, mp, `SELECT count(*) FROM files WHERE id = $1::uuid`, fileID)
	assertCountOne(t, mp, `SELECT count(*) FROM files WHERE id = $1::uuid`, fileIDs[1])
	if n := vectorCount(t, vp, "document_chunks_8", fileID); n != 0 {
		t.Errorf("chunks left: %d", n)
	}
	if n := vectorCount(t, vp, "document_chunk_parents", fileID); n != 0 {
		t.Errorf("parent chunks left: %d", n)
	}
	if ok, _ := stor.FileExists(context.Background(), path); ok {
		t.Errorf("legacy blob %s still exists", path)
	}
	if len(kg.calls) != 1 || kg.calls[0] != [2]string{kbID, fileID} {
		t.Errorf("kg hook calls = %v", kg.calls)
	}
	if len(spy.calls) != 1 || spy.calls[0] != kbID {
		t.Errorf("query cache calls = %v", spy.calls)
	}
}

func TestDeleteFiles_NeverDeletesSharedLibraryBlob(t *testing.T) {
	mp, vp := openTestPools(t)
	userID, kbID, _ := seedFixture(t, mp, false)
	dir := t.TempDir()
	stor, err := storage.New(storage.Config{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	_, fileID, blob := seedLibraryFile(t, mp, userID, kbID)
	writeBlob(t, stor, blob)
	addIndex(t, vp, kbID, fileID)

	d := cascade.New(mp, vp, stor)
	kg := &spyKGHook{}
	d.SetKGFileHook(kg)
	if err := d.DeleteFiles(context.Background(), []string{fileID}); err != nil {
		t.Fatalf("DeleteFiles: %v", err)
	}

	assertCountZero(t, mp, `SELECT count(*) FROM files WHERE id = $1::uuid`, fileID)
	if n := vectorCount(t, vp, "document_chunks_8", fileID); n != 0 {
		t.Errorf("chunks left: %d", n)
	}
	if n := vectorCount(t, vp, "document_chunk_parents", fileID); n != 0 {
		t.Errorf("parent chunks left: %d", n)
	}
	if ok, err := stor.FileExists(context.Background(), blob); err != nil || !ok {
		t.Errorf("shared library blob must survive DeleteFiles (exists=%v err=%v)", ok, err)
	}
	if len(kg.calls) != 1 {
		t.Errorf("kg hook calls = %v", kg.calls)
	}
}

func TestDeleteKB_SkipsLibraryBlobs(t *testing.T) {
	mp, vp := openTestPools(t)
	userID, kbID, _ := seedFixture(t, mp, false)
	dir := t.TempDir()
	stor, err := storage.New(storage.Config{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	_, fileID, blob := seedLibraryFile(t, mp, userID, kbID)
	writeBlob(t, stor, blob)
	addIndex(t, vp, kbID, fileID)

	var legacy []string
	rows, err := mp.Query(context.Background(),
		`SELECT storage_path FROM files WHERE kb_id=$1::uuid AND user_file_id IS NULL`, kbID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var p string
		_ = rows.Scan(&p)
		legacy = append(legacy, p)
		writeBlob(t, stor, p)
	}
	rows.Close()
	if len(legacy) == 0 {
		t.Fatal("fixture has no legacy rows")
	}

	// An agent chat in the KB: its ADK session is keyed by the chat id
	// without an FK (final review item 4).
	var chatID string
	if err := mp.QueryRow(context.Background(),
		`INSERT INTO chats (kb_id, user_id, title) VALUES ($1::uuid, $2::uuid, 'agent') RETURNING id::text`, kbID, userID).Scan(&chatID); err != nil {
		t.Fatal(err)
	}
	if _, err := mp.Exec(context.Background(),
		`INSERT INTO adk_sessions (app_name, user_id, id, update_time) VALUES ('agentchat', $1, $2, now())`, userID, chatID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = mp.Exec(context.Background(), `DELETE FROM adk_sessions WHERE app_name = 'agentchat' AND id = $1`, chatID)
	})

	d := cascade.New(mp, vp, stor)
	if err := d.DeleteKB(context.Background(), kbID); err != nil {
		t.Fatalf("DeleteKB: %v", err)
	}
	var sessions int
	if err := mp.QueryRow(context.Background(), `SELECT count(*) FROM adk_sessions WHERE id = $1`, chatID).Scan(&sessions); err != nil || sessions != 0 {
		t.Errorf("ADK session of a deleted KB's chat survived: %d (%v)", sessions, err)
	}
	for _, p := range legacy {
		if ok, _ := stor.FileExists(context.Background(), p); ok {
			t.Errorf("legacy blob %s survived DeleteKB", p)
		}
	}
	if ok, _ := stor.FileExists(context.Background(), blob); !ok {
		t.Errorf("library blob %s deleted by DeleteKB", blob)
	}
	if n := vectorCount(t, vp, "document_chunk_parents", fileID); n != 0 {
		t.Errorf("parent chunks left after DeleteKB: %d", n)
	}
	_ = os.Remove // keep os import used if helpers change
}

// Every delete path retires a library donor (fingerprint cleared, generation
// token bumped) BEFORE any of its vector rows is deleted, so copy mode can
// neither pick it nor keep a copy made from its vanishing index. Ordering is
// observed through the before-vector-delete seam: inside it the donor must
// already be retired while its chunks still exist.
func TestDeletePaths_RetireCopyDonorBeforeVectorDelete(t *testing.T) {
	for name, del := range map[string]func(d *cascade.Deleter, kbID, fileID string) error{
		"DeleteFiles": func(d *cascade.Deleter, _, fileID string) error {
			return d.DeleteFiles(context.Background(), []string{fileID})
		},
		"DeleteKB": func(d *cascade.Deleter, kbID, _ string) error {
			return d.DeleteKB(context.Background(), kbID)
		},
	} {
		t.Run(name, func(t *testing.T) {
			mp, vp := openTestPools(t)
			ctx := context.Background()
			userID, kbID, _ := seedFixture(t, mp, false)
			stor := localFS(t, mp, kbID)
			ufID, fileID, _ := seedLibraryFile(t, mp, userID, kbID)
			addIndex(t, vp, kbID, fileID)
			if _, err := mp.Exec(ctx,
				`UPDATE files SET status = 'completed', index_fingerprint = 'F', progress_updated_at = now() - interval '1 hour' WHERE id = $1::uuid`,
				fileID); err != nil {
				t.Fatal(err)
			}
			fstore := files.NewStore(mp)
			donor, gen, err := fstore.FindCopyDonor(ctx, ufID, "F", "00000000-0000-0000-0000-000000000000")
			if err != nil || donor != fileID {
				t.Fatalf("precondition: FindCopyDonor = %q, %v; want %q", donor, err, fileID)
			}

			hookRan := false
			restore := cascade.SetBeforeVectorDeleteHook(func(ctx context.Context, ids []string) {
				hookRan = true
				if n := vectorCount(t, vp, "document_chunks_8", fileID); n == 0 {
					t.Error("hook ran after the vector delete; ordering seam misplaced")
				}
				if d, _, err := fstore.FindCopyDonor(ctx, ufID, "F", "00000000-0000-0000-0000-000000000000"); err != nil || d != "" {
					t.Errorf("donor still selectable before the vector delete: %q, %v", d, err)
				}
				// A copy that found the donor earlier fails its recheck.
				if ok, err := fstore.DonorStillValid(ctx, fileID, gen, ufID, "F"); err != nil || ok {
					t.Errorf("DonorStillValid = %v, %v; want false (token bumped)", ok, err)
				}
			})
			defer restore()

			if err := del(cascade.New(mp, vp, stor), kbID, fileID); err != nil {
				t.Fatalf("delete: %v", err)
			}
			if !hookRan {
				t.Fatal("before-vector-delete seam never ran")
			}
		})
	}
}
