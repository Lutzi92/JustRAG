//go:build integration

package cascade_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/cascade"
	"github.com/justrag/go-backend/internal/storage"
)

type libFixture struct {
	userA, userB, kbA, kbB string
	ufID, fileA, fileB     string
	blob                   string
	stor                   storage.Storage
}

// seedCrossUser builds: user A owns library file uf1 and kbA; user B owns
// kbB; uf1 has a chunked copy in both KBs.
func seedCrossUser(t *testing.T, mp, vp *pgxpool.Pool) libFixture {
	t.Helper()
	var f libFixture
	f.userA, f.kbA, _ = seedFixture(t, mp, false)
	f.userB, f.kbB, _ = seedFixture(t, mp, false)
	f.ufID, f.fileA, f.blob = seedLibraryFile(t, mp, f.userA, f.kbA)
	if err := mp.QueryRow(context.Background(), `
		INSERT INTO files (kb_id, name, type, storage_path, user_file_id)
		VALUES ($1::uuid, 'lib.txt', 'text/plain', $2, $3::uuid)
		RETURNING id::text`, f.kbB, f.blob, f.ufID).Scan(&f.fileB); err != nil {
		t.Fatalf("insert second copy: %v", err)
	}
	var err error
	f.stor, err = storage.New(storage.Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	writeBlob(t, f.stor, f.blob)
	addIndex(t, vp, f.kbA, f.fileA)
	addIndex(t, vp, f.kbB, f.fileB)
	return f
}

func TestDeleteUserFile_OwnerScopedAndRemovesEveryCopy(t *testing.T) {
	mp, vp := openTestPools(t)
	f := seedCrossUser(t, mp, vp)
	d := cascade.New(mp, vp, f.stor)
	ctx := context.Background()

	if err := d.DeleteUserFile(ctx, f.userB, f.ufID); !errors.Is(err, cascade.ErrUserFileNotFound) {
		t.Fatalf("foreign owner: err = %v, want ErrUserFileNotFound", err)
	}
	assertCountOne(t, mp, `SELECT count(*) FROM user_files WHERE id = $1::uuid`, f.ufID)
	assertCountOne(t, mp, `SELECT count(*) FROM files WHERE id = $1::uuid`, f.fileA)
	assertCountOne(t, mp, `SELECT count(*) FROM files WHERE id = $1::uuid`, f.fileB)
	if ok, _ := f.stor.FileExists(ctx, f.blob); !ok {
		t.Fatal("blob deleted by foreign owner call")
	}

	if err := d.DeleteUserFile(ctx, f.userA, "not-a-uuid"); !errors.Is(err, cascade.ErrUserFileNotFound) {
		t.Fatalf("malformed id: err = %v", err)
	}

	parseKey := "users/" + f.userA + "/parses/" + f.ufID + "/x.json"
	if err := f.stor.StoreFile(ctx, parseKey, []byte(`{}`), "application/json"); err != nil {
		t.Fatalf("seed parse cache: %v", err)
	}
	if err := d.DeleteUserFile(ctx, f.userA, f.ufID); err != nil {
		t.Fatalf("DeleteUserFile: %v", err)
	}
	if ok, _ := f.stor.FileExists(ctx, parseKey); ok {
		t.Error("parse cache object still exists")
	}
	assertCountZero(t, mp, `SELECT count(*) FROM user_files WHERE id = $1::uuid`, f.ufID)
	assertCountZero(t, mp, `SELECT count(*) FROM files WHERE user_file_id = $1::uuid`, f.ufID)
	for _, id := range []string{f.fileA, f.fileB} {
		if n := vectorCount(t, vp, "document_chunks_8", id); n != 0 {
			t.Errorf("chunks left for %s: %d", id, n)
		}
	}
	if ok, _ := f.stor.FileExists(ctx, f.blob); ok {
		t.Error("blob still exists")
	}
}

func TestDeleteUser_RemovesLibraryAndLeavesOtherOwnersKB(t *testing.T) {
	mp, vp := openTestPools(t)
	f := seedCrossUser(t, mp, vp)
	d := cascade.New(mp, vp, f.stor)
	ctx := context.Background()

	if err := d.DeleteUser(ctx, f.userA); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	assertCountZero(t, mp, `SELECT count(*) FROM users WHERE id = $1::uuid`, f.userA)
	assertCountZero(t, mp, `SELECT count(*) FROM knowledge_bases WHERE id = $1::uuid`, f.kbA)
	assertCountOne(t, mp, `SELECT count(*) FROM knowledge_bases WHERE id = $1::uuid`, f.kbB)
	assertCountZero(t, mp, `SELECT count(*) FROM user_files WHERE id = $1::uuid`, f.ufID)
	assertCountZero(t, mp, `SELECT count(*) FROM files WHERE id = $1::uuid`, f.fileB)
	if n := vectorCount(t, vp, "document_chunks_8", f.fileB); n != 0 {
		t.Errorf("kbB copy chunks left: %d", n)
	}
	if ok, _ := f.stor.FileExists(ctx, f.blob); ok {
		t.Error("blob still exists")
	}
}
