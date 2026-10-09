//go:build integration

package userfiles_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/storage"
	"github.com/justrag/go-backend/internal/userfiles"
)

type adoptQuota int64

func (q adoptQuota) GlobalQuotaBytes(context.Context) int64 { return int64(q) }

type adoptLimits struct{}

func (adoptLimits) TabularMaxFileBytes(context.Context) int { return 500 << 20 }

func TestAdoptLegacyUploadEndToEnd(t *testing.T) {
	pool := openMainPool(t)
	ctx := context.Background()
	uid := seedUser(t, pool)
	kbID := seedKBNamed(t, pool, "adopt-e2e", "private")

	dir := t.TempDir()
	stor, err := storage.New(storage.Config{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	oldPath := "legacyuser/" + kbID + "/doc.txt"
	if err := stor.StoreFile(ctx, oldPath, []byte("legacy bytes"), "text/plain"); err != nil {
		t.Fatal(err)
	}
	var fileID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO files (kb_id, name, type, size, status, origin, storage_path, uploaded_by)
		VALUES ($1::uuid, 'doc.txt', 'text/plain', 12, 'completed', 'upload', $2, $3::uuid)
		RETURNING id::text`, kbID, oldPath, uid).Scan(&fileID); err != nil {
		t.Fatalf("insert legacy file: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM files WHERE id = $1::uuid`, fileID)              //nolint:errcheck
		pool.Exec(ctx, `DELETE FROM user_files WHERE owner_user_id = $1::uuid`, uid) //nolint:errcheck
	})

	store := userfiles.NewStore(pool)
	quota := adoptQuota(0)
	ad := userfiles.NewAdopter(userfiles.NewAdoptStore(pool), store, stor, quota)

	res, err := ad.Adopt(ctx, kbID, uid, []string{fileID})
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if len(res.Adopted) != 1 || len(res.Skipped) != 0 {
		t.Fatalf("res = %+v", res)
	}
	ufID := res.Adopted[0].UserFileID

	var gotUF, gotPath string
	if err := pool.QueryRow(ctx, `SELECT user_file_id::text, storage_path FROM files WHERE id = $1::uuid`, fileID).Scan(&gotUF, &gotPath); err != nil {
		t.Fatal(err)
	}
	if gotUF != ufID || gotPath != "users/"+uid+"/"+ufID {
		t.Fatalf("files row: user_file_id=%q path=%q", gotUF, gotPath)
	}
	b, err := stor.ReadFile(ctx, gotPath)
	if err != nil || string(b) != "legacy bytes" {
		t.Fatalf("new blob = %q, %v", b, err)
	}
	if ok, _ := stor.FileExists(ctx, oldPath); ok {
		t.Error("old blob should be gone")
	}
	if _, err := os.Stat(filepath.Join(dir, oldPath)); err == nil {
		t.Error("old path still on disk")
	}

	// Second adoption of the same row: already_library, nothing changes.
	res, err = ad.Adopt(ctx, kbID, uid, []string{fileID})
	if err != nil || len(res.Skipped) != 1 || res.Skipped[0].Reason != "already_library" {
		t.Fatalf("re-adopt: %+v, %v", res, err)
	}

	// The library lists it with the KB copy.
	h := userfiles.NewHandler(store, userfiles.NewIngester(store, stor, quota), stor, adoptLimits{}, quota)
	req := httptest.NewRequest(http.MethodGet, "/api/library/files", nil)
	req = req.WithContext(auth.WithUser(req.Context(), &auth.Claims{ID: uid}))
	rec := httptest.NewRecorder()
	h.List(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status %d: %s", rec.Code, rec.Body)
	}
	var out struct {
		Items []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			KBs  []struct {
				KBID   string `json:"kbId"`
				FileID string `json:"fileId"`
			} `json:"kbs"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Items) != 1 || out.Items[0].ID != ufID || out.Items[0].Name != "doc.txt" ||
		len(out.Items[0].KBs) != 1 || out.Items[0].KBs[0].KBID != kbID || out.Items[0].KBs[0].FileID != fileID {
		t.Fatalf("library listing = %s", rec.Body)
	}
}

func TestAdoptBusyAndGuardedUpdateIntegration(t *testing.T) {
	pool := openMainPool(t)
	ctx := context.Background()
	uid := seedUser(t, pool)
	kbID := seedKBNamed(t, pool, "adopt-busy", "private")
	stor, err := storage.New(storage.Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	oldPath := "legacyuser/" + kbID + "/busy.txt"
	if err := stor.StoreFile(ctx, oldPath, []byte("busy bytes"), "text/plain"); err != nil {
		t.Fatal(err)
	}
	var fileID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO files (kb_id, name, type, size, status, origin, storage_path, uploaded_by)
		VALUES ($1::uuid, 'busy.txt', 'text/plain', 10, 'processing', 'upload', $2, $3::uuid)
		RETURNING id::text`, kbID, oldPath, uid).Scan(&fileID); err != nil {
		t.Fatalf("insert: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM files WHERE id = $1::uuid`, fileID)              //nolint:errcheck
		pool.Exec(ctx, `DELETE FROM user_files WHERE owner_user_id = $1::uuid`, uid) //nolint:errcheck
	})
	as := userfiles.NewAdoptStore(pool)
	ad := userfiles.NewAdopter(as, userfiles.NewStore(pool), stor, adoptQuota(0))
	res, err := ad.Adopt(ctx, kbID, uid, []string{fileID})
	if err != nil || len(res.Skipped) != 1 || res.Skipped[0].Reason != userfiles.AdoptSkipBusy {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	// The guarded UPDATE alone must also refuse a busy row.
	ok, err := as.LinkFile(ctx, fileID, "00000000-0000-0000-0000-000000000001", "x", uid)
	if err != nil || ok {
		t.Fatalf("LinkFile on busy row: ok=%v err=%v", ok, err)
	}
	if ex, _ := stor.FileExists(ctx, oldPath); !ex {
		t.Error("legacy blob must be untouched")
	}
}
