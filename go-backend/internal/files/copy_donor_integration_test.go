//go:build integration

// Copy-mode donor lookup and terminal write (P2-R3/R6) against the live main
// DB. Pool helper is shared with store_pg_integration_test.go.

package files_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/files"
	"github.com/justrag/go-backend/internal/userfiles"
)

// seedLibraryKBs seeds a user, one library file and n private KBs. Cleanup is
// registered on t.
func seedLibraryKBs(t *testing.T, pool *pgxpool.Pool, n int) (userID, ufID string, kbIDs []string) {
	t.Helper()
	ctx := context.Background()
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (username, password_hash, role) VALUES ($1, 'x', 'user') RETURNING id::text`,
		"copydonor-"+uuid.NewString()).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() { pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1::uuid`, userID) }) //nolint:errcheck
	for i := 0; i < n; i++ {
		var kb string
		if err := pool.QueryRow(ctx,
			`INSERT INTO knowledge_bases (name, visibility) VALUES ('copy-donor-test', 'private') RETURNING id::text`).Scan(&kb); err != nil {
			t.Fatalf("seed kb: %v", err)
		}
		kbIDs = append(kbIDs, kb)
		t.Cleanup(func() { pool.Exec(context.Background(), `DELETE FROM knowledge_bases WHERE id = $1::uuid`, kb) }) //nolint:errcheck
	}
	ufID = uuid.NewString()
	if _, _, err := userfiles.NewStore(pool).Insert(ctx, userfiles.NewUserFile{
		ID: ufID, OwnerUserID: userID, Name: "a.pdf", Mime: "application/pdf", Size: 3,
		SHA256: strings.Repeat(strings.ReplaceAll(uuid.NewString(), "-", ""), 2), StoragePath: "users/" + userID + "/" + ufID,
	}); err != nil {
		t.Fatalf("insert user file: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM files WHERE user_file_id = $1::uuid`, ufID) //nolint:errcheck
		pool.Exec(context.Background(), `DELETE FROM user_files WHERE id = $1::uuid`, ufID)      //nolint:errcheck
	})
	return userID, ufID, kbIDs
}

func seedCopy(t *testing.T, store *files.PGStore, kbID, userID, ufID string) string {
	t.Helper()
	rec, err := store.CreateFile(context.Background(), files.CreateFileData{
		KbID: kbID, Name: "a.pdf", Type: "application/pdf", Size: 3, Origin: "upload",
		StoragePath: "users/" + userID + "/" + ufID, UploadedBy: userID, UserFileID: ufID,
	})
	if err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	return rec.ID
}

func TestFindCopyDonor(t *testing.T) {
	pool := openMainPool(t)
	ctx := context.Background()
	store := files.NewStore(pool)
	userID, ufID, kbs := seedLibraryKBs(t, pool, 4)

	target := seedCopy(t, store, kbs[0], userID, ufID)
	older := seedCopy(t, store, kbs[1], userID, ufID)
	newer := seedCopy(t, store, kbs[2], userID, ufID)
	partial := seedCopy(t, store, kbs[3], userID, ufID)
	set := func(id, status, fp, age string) {
		t.Helper()
		if _, err := pool.Exec(ctx,
			`UPDATE files SET status = $2, index_fingerprint = NULLIF($3, ''), created_at = now() - $4::interval WHERE id = $1::uuid`,
			id, status, fp, age); err != nil {
			t.Fatal(err)
		}
	}
	set(target, "completed", "F", "1 minute") // the target itself never counts
	set(older, "completed", "F", "2 hours")
	set(newer, "completed", "F", "1 hour")
	set(partial, "partial", "F", "1 second") // only 'completed' qualifies

	got, gen, err := store.FindCopyDonor(ctx, ufID, "F", target)
	if err != nil || got != newer {
		t.Fatalf("FindCopyDonor = %q, %v; want the most recent completed copy %q", got, err, newer)
	}
	if got, _, err := store.FindCopyDonor(ctx, ufID, "OTHER", target); err != nil || got != "" {
		t.Fatalf("fingerprint mismatch = %q, %v; want empty", got, err)
	}
	if got, _, err := store.FindCopyDonor(ctx, uuid.NewString(), "F", target); err != nil || got != "" {
		t.Fatalf("other user file = %q, %v; want empty", got, err)
	}
	set(newer, "processing", "F", "1 hour")
	set(older, "error", "F", "2 hours")
	if got, _, err := store.FindCopyDonor(ctx, ufID, "F", target); err != nil || got != "" {
		t.Fatalf("no completed donor left = %q, %v; want empty", got, err)
	}
	// The generation token is progress_updated_at's text form.
	var want string
	if err := pool.QueryRow(ctx, `SELECT COALESCE(progress_updated_at::text, '') FROM files WHERE id = $1::uuid`, newer).Scan(&want); err != nil {
		t.Fatal(err)
	}
	if gen != want {
		t.Errorf("generation = %q, want %q", gen, want)
	}
}

func TestDonorStillValid(t *testing.T) {
	pool := openMainPool(t)
	ctx := context.Background()
	store := files.NewStore(pool)
	userID, ufID, kbs := seedLibraryKBs(t, pool, 2)
	donor := seedCopy(t, store, kbs[0], userID, ufID)
	other := seedCopy(t, store, kbs[1], userID, ufID)
	set := func(id, status, fp string) {
		t.Helper()
		if _, err := pool.Exec(ctx,
			`UPDATE files SET status = $2, index_fingerprint = NULLIF($3, '') WHERE id = $1::uuid`, id, status, fp); err != nil {
			t.Fatal(err)
		}
	}
	set(donor, "completed", "F")
	set(other, "completed", "F") // a second (e.g. newer) donor is irrelevant
	if err := store.UpdateFileProgress(ctx, donor, 100); err != nil {
		t.Fatal(err)
	}
	generation := func() string {
		t.Helper()
		id, gen, err := store.FindCopyDonor(ctx, ufID, "F", other)
		if err != nil || id != donor || gen == "" {
			t.Fatalf("FindCopyDonor = %q, %q, %v; want donor with a generation", id, gen, err)
		}
		return gen
	}
	gen := generation()
	check := func(name, uf, fp, g string, want bool) {
		t.Helper()
		got, err := store.DonorStillValid(ctx, donor, g, uf, fp)
		if err != nil || got != want {
			t.Errorf("%s: DonorStillValid = %v, %v; want %v", name, got, err, want)
		}
	}
	check("valid", ufID, "F", gen, true)
	check("other fingerprint", ufID, "G", gen, false)
	check("other user file", uuid.NewString(), "F", gen, false)
	check("other generation", ufID, "F", gen+"x", false)

	// A progress heartbeat alone (status and fingerprint untouched) bumps
	// the generation.
	time.Sleep(2 * time.Millisecond)
	if err := store.UpdateFileProgress(ctx, donor, 100); err != nil {
		t.Fatal(err)
	}
	check("after progress bump", ufID, "F", gen, false)

	// A full unchanged-settings re-ingest: 'processing' (clears the
	// fingerprint, bumps the generation), then completed with the SAME
	// fingerprint re-stamped. Status and fingerprint look untouched; only
	// the generation reveals the rebuild.
	gen = generation()
	time.Sleep(2 * time.Millisecond)
	if err := store.UpdateFileStatus(ctx, donor, "processing"); err != nil {
		t.Fatal(err)
	}
	check("re-ingesting", ufID, "F", gen, false)
	set(donor, "completed", "F")
	check("re-stamped the same fingerprint", ufID, "F", gen, false)
	check("new generation", ufID, "F", generation(), true)
}

func TestLibraryLink(t *testing.T) {
	pool := openMainPool(t)
	ctx := context.Background()
	store := files.NewStore(pool)
	userID, ufID, kbs := seedLibraryKBs(t, pool, 1)
	lib := seedCopy(t, store, kbs[0], userID, ufID)
	uf, owner, err := store.LibraryLink(ctx, lib)
	if err != nil || uf != ufID || owner != userID {
		t.Fatalf("library copy: LibraryLink = %q, %q, %v; want %q, %q", uf, owner, err, ufID, userID)
	}
	plain, err := store.CreateFile(ctx, files.CreateFileData{
		KbID: kbs[0], Name: "b.txt", Type: "text/plain", Size: 1, Origin: "upload",
		StoragePath: "kb/b.txt", UploadedBy: userID,
	})
	if err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	t.Cleanup(func() { pool.Exec(context.Background(), `DELETE FROM files WHERE id = $1::uuid`, plain.ID) }) //nolint:errcheck
	if uf, owner, err := store.LibraryLink(ctx, plain.ID); err != nil || uf != "" || owner != "" {
		t.Fatalf("non-library file: LibraryLink = %q, %q, %v; want empty", uf, owner, err)
	}
	if _, _, err := store.LibraryLink(ctx, uuid.NewString()); err == nil {
		t.Fatal("missing row: want an error")
	}
	if p, err := store.CurrentStoragePath(ctx, plain.ID); err != nil || p != "kb/b.txt" {
		t.Fatalf("CurrentStoragePath = %q, %v; want kb/b.txt", p, err)
	}
	if _, err := store.CurrentStoragePath(ctx, uuid.NewString()); err == nil {
		t.Fatal("CurrentStoragePath on a missing row: want an error")
	}
}

func TestMarkCopied(t *testing.T) {
	pool := openMainPool(t)
	ctx := context.Background()
	store := files.NewStore(pool)
	userID, ufID, kbs := seedLibraryKBs(t, pool, 1)
	id := seedCopy(t, store, kbs[0], userID, ufID)
	if _, err := pool.Exec(ctx, `UPDATE files SET status = 'processing', progress = 5,
		error_stage = 'parse', error_message = 'old', current_stage = 'embed', stage_index = 2,
		stage_total = 4, stage_detail = 'x' WHERE id = $1::uuid`, id); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkCopied(ctx, id, "F"); err != nil {
		t.Fatal(err)
	}
	var status, fp string
	var progress int
	var dirty bool
	if err := pool.QueryRow(ctx, `SELECT status, progress, COALESCE(index_fingerprint, ''),
		(error_stage IS NOT NULL OR error_message IS NOT NULL OR current_stage IS NOT NULL
		 OR stage_index IS NOT NULL OR stage_total IS NOT NULL OR stage_detail IS NOT NULL)
		FROM files WHERE id = $1::uuid`, id).Scan(&status, &progress, &fp, &dirty); err != nil {
		t.Fatal(err)
	}
	if status != "completed" || progress != 100 || fp != "F" || dirty {
		t.Fatalf("status=%s progress=%d fp=%q dirty=%v", status, progress, fp, dirty)
	}
}

// A file going back to 'processing' (re-ingest, re-embed, copy) is no longer
// a valid donor: its fingerprint is cleared with the status flip.
func TestUpdateFileStatusProcessingClearsFingerprint(t *testing.T) {
	pool := openMainPool(t)
	ctx := context.Background()
	store := files.NewStore(pool)
	userID, ufID, kbs := seedLibraryKBs(t, pool, 1)
	id := seedCopy(t, store, kbs[0], userID, ufID)
	read := func() string {
		var fp string
		if err := pool.QueryRow(ctx, `SELECT COALESCE(index_fingerprint, '') FROM files WHERE id = $1::uuid`, id).Scan(&fp); err != nil {
			t.Fatal(err)
		}
		return fp
	}
	if err := store.MarkCopied(ctx, id, "F"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateFileStatus(ctx, id, "completed"); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != "F" {
		t.Fatalf("a non-processing transition must keep the fingerprint, got %q", got)
	}
	if err := store.UpdateFileStatus(ctx, id, "processing"); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != "" {
		t.Fatalf("processing must clear the fingerprint, got %q", got)
	}
}
