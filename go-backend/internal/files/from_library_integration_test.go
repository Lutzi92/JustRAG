//go:build integration

// Library-backed files rows: CreateFile writes user_file_id, GetKBCopy finds
// it, and files_user_file_kb_uidx rejects a second copy in the same KB.
// Pool helper is shared with store_pg_integration_test.go.

package files_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/justrag/go-backend/internal/files"
	"github.com/justrag/go-backend/internal/pgxutil"
	"github.com/justrag/go-backend/internal/userfiles"
)

func TestCreateFile_LibraryBacked_RoundTrip(t *testing.T) {
	pool := openMainPool(t)
	ctx := context.Background()
	store := files.NewStore(pool)

	var userID, kbID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (username, password_hash, role) VALUES ($1, 'x', 'user') RETURNING id::text`,
		"fromlib-"+uuid.NewString()).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM users WHERE id = $1::uuid`, userID) }) //nolint:errcheck
	if err := pool.QueryRow(ctx,
		`INSERT INTO knowledge_bases (name, visibility) VALUES ('from-library-test', 'private') RETURNING id::text`).Scan(&kbID); err != nil {
		t.Fatalf("seed kb: %v", err)
	}
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM knowledge_bases WHERE id = $1::uuid`, kbID) }) //nolint:errcheck

	ufID := uuid.NewString()
	uf, _, err := userfiles.NewStore(pool).Insert(ctx, userfiles.NewUserFile{
		ID: ufID, OwnerUserID: userID, Name: "a.pdf", Mime: "application/pdf", Size: 3,
		SHA256: strings.Repeat(strings.ReplaceAll(uuid.NewString(), "-", ""), 2), StoragePath: "users/" + userID + "/" + ufID,
	})
	if err != nil {
		t.Fatalf("insert user file: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM files WHERE user_file_id = $1::uuid`, uf.ID) //nolint:errcheck
		pool.Exec(ctx, `DELETE FROM user_files WHERE id = $1::uuid`, uf.ID)      //nolint:errcheck
	})

	if got, err := store.GetKBCopy(ctx, kbID, uf.ID); err != nil || got != "" {
		t.Fatalf("GetKBCopy before = %q, %v; want empty", got, err)
	}

	data := files.CreateFileData{
		KbID: kbID, Name: "a.pdf", Type: "application/pdf", Size: 3, Origin: "upload",
		StoragePath: uf.StoragePath, UploadedBy: userID, UserFileID: uf.ID,
	}
	rec, err := store.CreateFile(ctx, data)
	if err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	if rec.UserFileID != uf.ID {
		t.Errorf("record UserFileID = %q, want %q", rec.UserFileID, uf.ID)
	}
	if got, err := store.GetKBCopy(ctx, kbID, uf.ID); err != nil || got != rec.ID {
		t.Fatalf("GetKBCopy = %q, %v; want %q", got, err, rec.ID)
	}

	if _, err := store.CreateFile(ctx, data); !pgxutil.IsUniqueViolation(err) {
		t.Fatalf("second CreateFile err = %v, want unique violation", err)
	}

	// A plain upload (no library link) still writes NULL.
	plain := data
	plain.UserFileID = ""
	plain.Name = "plain.pdf"
	rec2, err := store.CreateFile(ctx, plain)
	if err != nil {
		t.Fatalf("plain CreateFile: %v", err)
	}
	var isNull bool
	if err := pool.QueryRow(ctx, `SELECT user_file_id IS NULL FROM files WHERE id = $1::uuid`, rec2.ID).Scan(&isNull); err != nil || !isNull {
		t.Fatalf("plain row user_file_id NULL = %v, %v", isNull, err)
	}
}
