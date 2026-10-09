//go:build integration

package userfiles_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/justrag/go-backend/internal/storage"
	"github.com/justrag/go-backend/internal/userfiles"
)

func TestOrphanSweepAgainstPostgres(t *testing.T) {
	pool := openMainPool(t)
	ctx := context.Background()
	uid := seedUser(t, pool)
	dir := t.TempDir()
	stor, err := storage.New(storage.Config{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}

	refUFID := uuid.NewString()
	refKey := "users/" + uid + "/" + refUFID
	kbCopyKey := "users/" + uid + "/" + uuid.NewString() // referenced only by a files row
	orphanKey := "users/" + uid + "/" + uuid.NewString()
	goneUFID := uuid.NewString()
	goneCache := libCache(uid, goneUFID)
	liveCache := libCache(uid, refUFID)

	if _, err := pool.Exec(ctx, `
		INSERT INTO user_files (id, owner_user_id, name, mime, size, sha256, storage_path)
		VALUES ($1::uuid, $2::uuid, 'a.txt', 'text/plain', 3, 'abc', $3)`, refUFID, uid, refKey); err != nil {
		t.Fatalf("insert user_files: %v", err)
	}
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM user_files WHERE owner_user_id = $1::uuid`, uid) }) //nolint:errcheck

	kbID := seedKBNamed(t, pool, "orphan-sweep", "private")
	var fid string
	if err := pool.QueryRow(ctx, `
		INSERT INTO files (kb_id, name, type, size, status, origin, storage_path, uploaded_by)
		VALUES ($1::uuid, 'b.txt', 'text/plain', 3, 'completed', 'upload', $2, $3::uuid) RETURNING id::text`,
		kbID, kbCopyKey, uid).Scan(&fid); err != nil {
		t.Fatalf("insert files: %v", err)
	}
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM files WHERE id = $1::uuid`, fid) }) //nolint:errcheck

	old := time.Now().Add(-48 * time.Hour)
	for _, k := range []string{refKey, kbCopyKey, orphanKey, goneCache, liveCache} {
		if err := stor.StoreFile(ctx, k, []byte("x"), "text/plain"); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(filepath.Join(dir, filepath.FromSlash(k)), old, old); err != nil {
			t.Fatal(err)
		}
	}
	youngKey := "users/" + uid + "/" + uuid.NewString()
	if err := stor.StoreFile(ctx, youngKey, []byte("x"), "text/plain"); err != nil {
		t.Fatal(err)
	}

	st := userfiles.NewOrphanStore(pool)
	n, err := userfiles.NewOrphanSweeper(st, stor).RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if n != 2 {
		t.Fatalf("deleted %d, want 2 (orphan blob + cache of missing file)", n)
	}
	for k, want := range map[string]bool{
		refKey: true, kbCopyKey: true, orphanKey: false, goneCache: false, liveCache: true, youngKey: true,
	} {
		got, err := stor.FileExists(ctx, k)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("exists(%s)=%v want %v", k, got, want)
		}
	}

	files, bytes, err := st.LibraryTotals(ctx)
	if err != nil || files < 1 || bytes < 3 {
		t.Fatalf("LibraryTotals=%d,%d err=%v", files, bytes, err)
	}
}

func libCache(uid, ufid string) string {
	return "users/" + uid + "/parses/" + ufid + "/chat-text.json"
}
