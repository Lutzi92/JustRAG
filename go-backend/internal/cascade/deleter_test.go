//go:build integration

// Cascade-delete tests require a live Postgres (knowledge_bases, files,
// chats, etc.). Gated by the `integration` build tag so `go test ./...`
// without the tag stays runnable on machines without docker.

package cascade_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/cascade"
	"github.com/justrag/go-backend/internal/storage"
	"github.com/justrag/go-backend/internal/tabular"
)

// ---------------------------------------------------------------------------
// Test infrastructure — connects to the test main + vector DBs the same way
// internal/kg and internal/vector do. Skipped when the env vars are unset
// (e.g. `go test ./...` on a dev machine without the docker compose stack).
// ---------------------------------------------------------------------------

func openTestPools(t *testing.T) (mainPool *pgxpool.Pool, vectorPool *pgxpool.Pool) {
	t.Helper()
	mainPool = openPool(t, "DB_HOST", "DB_PORT", "DB_USER", "DB_PASSWORD", "DB_NAME")
	if mainPool == nil {
		t.Skip("cascade DB tests require DB_* env (main Postgres)")
	}
	vectorPool = openPool(t, "VECTOR_DB_HOST", "VECTOR_DB_PORT", "VECTOR_DB_USER", "VECTOR_DB_PASSWORD", "VECTOR_DB_NAME")
	if vectorPool == nil {
		// Fall back to the main pool — ChunkService only needs information_schema
		// access to enumerate dimension tables; on a single-DB dev setup this works.
		vectorPool = mainPool
	}
	return mainPool, vectorPool
}

func openPool(t *testing.T, hostEnv, portEnv, userEnv, passEnv, nameEnv string) *pgxpool.Pool {
	t.Helper()
	host, port, name := os.Getenv(hostEnv), os.Getenv(portEnv), os.Getenv(nameEnv)
	if host == "" || port == "" || name == "" {
		return nil
	}
	user := os.Getenv(userEnv)
	pass := os.Getenv(passEnv)
	dsn := fmt.Sprintf("postgres://%s:%s@%s:%s/%s",
		url.QueryEscape(user), url.QueryEscape(pass), host, port, name)
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New (%s): %v", nameEnv, err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// seedFixture creates a unique user + KB + a few files/chats/generated_content
// rows. Returns the IDs and a cleanup func that nukes the user (FK cascades
// take care of any leftovers if the test fails mid-flight).
func seedFixture(t *testing.T, pool *pgxpool.Pool, kbIsGlobal bool) (userID, kbID string, fileIDs []string) {
	t.Helper()
	ctx := context.Background()

	username := fmt.Sprintf("cascade-test-%d-%d", os.Getpid(), nextSerial())
	err := pool.QueryRow(ctx, `
		INSERT INTO users (username, password_hash) VALUES ($1, 'x')
		RETURNING id::text`, username).Scan(&userID)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() {
		// Best-effort: the cascade test should already have deleted the user.
		// If not, the FK cascade on users → KBs → files cleans up leftovers.
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1::uuid`, userID)
	})

	// Two KBs per fixture so DeleteUser tests can verify multi-KB cleanup.
	err = pool.QueryRow(ctx, `
		INSERT INTO knowledge_bases (name, user_id, description, visibility)
		VALUES ($1, $2::uuid, 'fixture', CASE WHEN $3 THEN 'public' ELSE 'private' END)
		RETURNING id::text`, "cascade-kb-"+username, userID, kbIsGlobal).Scan(&kbID)
	if err != nil {
		t.Fatalf("insert kb: %v", err)
	}

	// Insert 2 files with storage paths to verify storage cleanup.
	for i := range 2 {
		var id string
		err = pool.QueryRow(ctx, `
			INSERT INTO files (kb_id, name, type, storage_path)
			VALUES ($1::uuid, $2, 'text/plain', $3)
			RETURNING id::text`,
			kbID, fmt.Sprintf("file-%d.txt", i),
			fmt.Sprintf("%s/%s/file-%d.txt", username, kbID, i)).Scan(&id)
		if err != nil {
			t.Fatalf("insert file: %v", err)
		}
		fileIDs = append(fileIDs, id)
	}

	// Add a chat — exercises the chats delete step and proves messages are
	// FK-cascaded out.
	_, err = pool.Exec(ctx, `
		INSERT INTO chats (kb_id, user_id, title) VALUES ($1::uuid, $2::uuid, 'fixture')`,
		kbID, userID)
	if err != nil {
		t.Fatalf("insert chat: %v", err)
	}

	// generated_content + share so we test those steps too.
	_, err = pool.Exec(ctx, `
		INSERT INTO generated_content (kb_id, user_id, type, title, content)
		VALUES ($1::uuid, $2::uuid, 'note', 't', '{}'::jsonb)`, kbID, userID)
	if err != nil {
		t.Fatalf("insert generated_content: %v", err)
	}

	// Need a second user to share with — knowledge_base_shares has user_id FK.
	var shareeID string
	shareeName := username + "-sharee"
	err = pool.QueryRow(ctx, `
		INSERT INTO users (username, password_hash) VALUES ($1, 'x')
		RETURNING id::text`, shareeName).Scan(&shareeID)
	if err != nil {
		t.Fatalf("insert sharee: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1::uuid`, shareeID)
	})
	_, err = pool.Exec(ctx, `
		INSERT INTO knowledge_base_shares (kb_id, user_id, permission)
		VALUES ($1::uuid, $2::uuid, 'view')`, kbID, shareeID)
	if err != nil {
		t.Fatalf("insert share: %v", err)
	}

	if kbIsGlobal {
		_, err = pool.Exec(ctx, `
			INSERT INTO global_kb_editors (kb_id, user_id) VALUES ($1::uuid, $2::uuid)`,
			kbID, shareeID)
		if err != nil {
			t.Fatalf("insert global_kb_editor: %v", err)
		}
	}

	return userID, kbID, fileIDs
}

var serialCounter uint64

func nextSerial() uint64 { return atomic.AddUint64(&serialCounter, 1) }

// localFS returns a Storage rooted in t.TempDir() and pre-populates it with
// the two storage paths the fixture inserts into files. The deleter must
// remove these.
func localFS(t *testing.T, mainPool *pgxpool.Pool, kbID string) storage.Storage {
	t.Helper()
	dir := t.TempDir()
	stor, err := storage.New(storage.Config{DataDir: dir})
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	// Mirror the storage layout the fixture promised by inserting empty
	// files at each storage_path. Then DeleteFile will actually delete
	// something instead of silently no-op'ing.
	rows, err := mainPool.Query(context.Background(),
		`SELECT storage_path FROM files WHERE kb_id = $1::uuid AND storage_path IS NOT NULL`, kbID)
	if err != nil {
		t.Fatalf("read storage paths: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatalf("scan path: %v", err)
		}
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte("contents"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	return stor
}

// spyInvalidator records every call to InvalidateQueryCache so tests can
// assert the cascade hook fired.
type spyInvalidator struct {
	calls []string
	err   error
}

func (s *spyInvalidator) InvalidateQueryCache(_ context.Context, kbID string) error {
	s.calls = append(s.calls, kbID)
	return s.err
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestDeleteKB_RemovesAllAssets(t *testing.T) {
	mainPool, vectorPool := openTestPools(t)
	userID, kbID, _ := seedFixture(t, mainPool, false)
	stor := localFS(t, mainPool, kbID)

	d := cascade.New(mainPool, vectorPool, stor)
	spy := &spyInvalidator{}
	d.SetQueryCacheInvalidator(spy)

	if err := d.DeleteKB(context.Background(), kbID); err != nil {
		t.Fatalf("DeleteKB: %v", err)
	}

	assertCountZero(t, mainPool, `SELECT count(*) FROM knowledge_bases WHERE id = $1::uuid`, kbID)
	assertCountZero(t, mainPool, `SELECT count(*) FROM files WHERE kb_id = $1::uuid`, kbID)
	assertCountZero(t, mainPool, `SELECT count(*) FROM chats WHERE kb_id = $1::uuid`, kbID)
	assertCountZero(t, mainPool, `SELECT count(*) FROM generated_content WHERE kb_id = $1::uuid`, kbID)
	assertCountZero(t, mainPool, `SELECT count(*) FROM knowledge_base_shares WHERE kb_id = $1::uuid`, kbID)
	// User must remain (we deleted a single KB, not the user).
	assertCountOne(t, mainPool, `SELECT count(*) FROM users WHERE id = $1::uuid`, userID)

	if len(spy.calls) != 1 || spy.calls[0] != kbID {
		t.Errorf("query cache invalidator: calls=%v, want [%s]", spy.calls, kbID)
	}
}

// Best-effort: even if the invalidator returns an error, DeleteKB must
// still succeed. The cache-invalidation hook is documented as fail-safe.
func TestDeleteKB_InvalidatorErrorIsNonFatal(t *testing.T) {
	mainPool, vectorPool := openTestPools(t)
	_, kbID, _ := seedFixture(t, mainPool, false)
	stor := localFS(t, mainPool, kbID)

	d := cascade.New(mainPool, vectorPool, stor)
	d.SetQueryCacheInvalidator(&spyInvalidator{err: errors.New("redis down")})

	if err := d.DeleteKB(context.Background(), kbID); err != nil {
		t.Fatalf("DeleteKB should swallow invalidator error, got %v", err)
	}
	assertCountZero(t, mainPool, `SELECT count(*) FROM knowledge_bases WHERE id = $1::uuid`, kbID)
}

// Nil invalidator is a no-op. The KB still gets torn down.
func TestDeleteKB_NilInvalidatorIsNoOp(t *testing.T) {
	mainPool, vectorPool := openTestPools(t)
	_, kbID, _ := seedFixture(t, mainPool, false)
	stor := localFS(t, mainPool, kbID)

	d := cascade.New(mainPool, vectorPool, stor)
	// Intentionally do NOT call SetQueryCacheInvalidator.

	if err := d.DeleteKB(context.Background(), kbID); err != nil {
		t.Fatalf("DeleteKB: %v", err)
	}
	assertCountZero(t, mainPool, `SELECT count(*) FROM knowledge_bases WHERE id = $1::uuid`, kbID)
}

// DeleteUser must cascade through every KB the user owns, plus drop the
// user's row, plus invalidate every owned KB's query cache.
func TestDeleteUser_RemovesAllOwnedKBs(t *testing.T) {
	mainPool, vectorPool := openTestPools(t)
	userID, kb1, _ := seedFixture(t, mainPool, false)

	// Add a second KB to the same user (different fixture would create a new
	// user, so insert directly here).
	var kb2 string
	err := mainPool.QueryRow(context.Background(),
		`INSERT INTO knowledge_bases (name, user_id, description)
		 VALUES ($1, $2::uuid, 'fixture-2') RETURNING id::text`,
		"second-kb-"+userID, userID).Scan(&kb2)
	if err != nil {
		t.Fatalf("second kb: %v", err)
	}

	stor := localFS(t, mainPool, kb1)
	d := cascade.New(mainPool, vectorPool, stor)
	spy := &spyInvalidator{}
	d.SetQueryCacheInvalidator(spy)

	if err := d.DeleteUser(context.Background(), userID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}

	assertCountZero(t, mainPool, `SELECT count(*) FROM users WHERE id = $1::uuid`, userID)
	assertCountZero(t, mainPool, `SELECT count(*) FROM knowledge_bases WHERE id = $1::uuid`, kb1)
	assertCountZero(t, mainPool, `SELECT count(*) FROM knowledge_bases WHERE id = $1::uuid`, kb2)

	// Both KBs should have triggered an invalidation. Order isn't
	// guaranteed; check set membership.
	got := map[string]bool{}
	for _, c := range spy.calls {
		got[c] = true
	}
	if len(got) != 2 || !got[kb1] || !got[kb2] {
		t.Errorf("invalidator calls = %v, want both %s and %s", spy.calls, kb1, kb2)
	}
}

func TestDeleteGlobalKB(t *testing.T) {
	mainPool, vectorPool := openTestPools(t)
	_, kbID, _ := seedFixture(t, mainPool, true) // is_global = true
	stor := localFS(t, mainPool, kbID)

	d := cascade.New(mainPool, vectorPool, stor)
	if err := d.DeleteGlobalKB(context.Background(), kbID); err != nil {
		t.Fatalf("DeleteGlobalKB: %v", err)
	}
	assertCountZero(t, mainPool, `SELECT count(*) FROM knowledge_bases WHERE id = $1::uuid`, kbID)
	assertCountZero(t, mainPool, `SELECT count(*) FROM global_kb_editors WHERE kb_id = $1::uuid`, kbID)
}

// DeleteGlobalKB must NOT delete a non-global KB even if the ID matches —
// the SQL is guarded by `WHERE id = $1 AND visibility = 'public'`. A bug that
// drops that guard would let any caller delete any KB by id.
func TestDeleteGlobalKB_RefusesNonGlobalKB(t *testing.T) {
	mainPool, vectorPool := openTestPools(t)
	_, kbID, _ := seedFixture(t, mainPool, false) // is_global = false
	stor := localFS(t, mainPool, kbID)

	d := cascade.New(mainPool, vectorPool, stor)
	// DeleteGlobalKB runs the file/chunk/storage cleanup before the
	// guarded DB transaction, so files + storage CAN be removed even if
	// the KB row stays. The contract we're pinning is: the
	// knowledge_bases row remains.
	_ = d.DeleteGlobalKB(context.Background(), kbID)

	assertCountOne(t, mainPool, `SELECT count(*) FROM knowledge_bases WHERE id = $1::uuid`, kbID)
}

// TestCascadeDropsTabularTables verifies that when a KB is deleted, the
// per-sheet tabular tables created for its files are physically dropped.
// tabular_catalog rows cascade via FK, but the DDL tables must be dropped
// explicitly — this test guards against regressions.
func TestCascadeDropsTabularTables(t *testing.T) {
	mainPool, vectorPool := openTestPools(t)

	// Seed user + KB + files via the shared fixture. We'll reuse the first
	// seeded file ID for the tabular table.
	_, kbID, fileIDs := seedFixture(t, mainPool, false)
	fileID := fileIDs[0]

	// Write a tiny CSV to a temp path and materialize it.
	dir := t.TempDir()
	csvPath := dir + "/data.csv"
	if err := os.WriteFile(csvPath, []byte("Product,Units\nAlpha,10\nBeta,20\n"), 0o600); err != nil {
		t.Fatalf("write temp CSV: %v", err)
	}

	ctx := context.Background()
	m := tabular.NewMaterializer(mainPool)
	res, err := m.Materialize(ctx, csvPath, "data.csv", fileID, kbID, tabular.SemanticOptions{})
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if len(res.Sheets) == 0 {
		t.Fatal("expected at least one sheet in materialization result")
	}
	tableName := res.Sheets[0].TableName

	// Confirm the physical table exists before deletion.
	var existsBefore bool
	err = mainPool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables
		 WHERE table_schema='tabular' AND table_name=$1)`,
		tableName).Scan(&existsBefore)
	if err != nil {
		t.Fatalf("pre-delete existence check: %v", err)
	}
	if !existsBefore {
		t.Fatalf("tabular table %q should exist before KB deletion", tableName)
	}

	// Delete the KB via the cascade Deleter (same path as TestDeleteKB_RemovesAllAssets).
	stor := localFS(t, mainPool, kbID)
	d := cascade.New(mainPool, vectorPool, stor)
	if err := d.DeleteKB(ctx, kbID); err != nil {
		t.Fatalf("DeleteKB: %v", err)
	}

	// After cascade-deleting the KB, the per-sheet table must not exist.
	var existsAfter bool
	err = mainPool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables
		 WHERE table_schema='tabular' AND table_name=$1)`,
		tableName).Scan(&existsAfter)
	if err != nil {
		t.Fatalf("post-delete existence check: %v", err)
	}
	if existsAfter {
		t.Fatalf("tabular table %q should have been dropped on KB delete", tableName)
	}
}

// Ein Einladungslink haengt an der KB und muss mit ihr verschwinden. Der FK
// wuerde kaskadieren, aber deleteKBTransaction zaehlt jede KB-gebundene
// Tabelle explizit auf — dieser Test haelt die Aufzaehlung vollstaendig.
func TestDeleteKB_RemovesInviteLinks(t *testing.T) {
	mainPool, vectorPool := openTestPools(t)
	_, kbID, _ := seedFixture(t, mainPool, false)
	stor := localFS(t, mainPool, kbID)

	ctx := context.Background()
	if _, err := mainPool.Exec(ctx, `
		INSERT INTO kb_invite_links (kb_id, token, role)
		VALUES ($1::uuid, 'cascade-test-token', 'view')`, kbID); err != nil {
		t.Fatalf("insert invite link: %v", err)
	}

	d := cascade.New(mainPool, vectorPool, stor)
	if err := d.DeleteKB(ctx, kbID); err != nil {
		t.Fatalf("DeleteKB: %v", err)
	}

	assertCountZero(t, mainPool, `SELECT count(*) FROM kb_invite_links WHERE kb_id = $1::uuid`, kbID)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func assertCountZero(t *testing.T, pool *pgxpool.Pool, query string, args ...any) {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 rows for %q, got %d", query, n)
	}
}

func assertCountOne(t *testing.T, pool *pgxpool.Pool, query string, args ...any) {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 row for %q, got %d", query, n)
	}
}
