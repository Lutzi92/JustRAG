//go:build integration

package userfiles_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func openMainPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	host, port, name := os.Getenv("DB_HOST"), os.Getenv("DB_PORT"), os.Getenv("DB_NAME")
	if host == "" || port == "" || name == "" {
		t.Skip("userfiles schema tests require DB_* env (main Postgres)")
	}
	dsn := fmt.Sprintf("postgres://%s:%s@%s:%s/%s",
		url.QueryEscape(os.Getenv("DB_USER")), url.QueryEscape(os.Getenv("DB_PASSWORD")), host, port, name)
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func seedUser(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	ctx := context.Background()
	var id string
	username := fmt.Sprintf("userfiles-schema-%d", time.Now().UnixNano())
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, password_hash, role)
		VALUES ($1, 'x', 'user') RETURNING id::text`, username).Scan(&id); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM users WHERE id = $1::uuid`, id) }) //nolint:errcheck
	return id
}

func seedKB(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := pool.QueryRow(ctx, `
		INSERT INTO knowledge_bases (name) VALUES ('userfiles-schema-test')
		RETURNING id::text`).Scan(&id); err != nil {
		t.Fatalf("insert kb: %v", err)
	}
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM knowledge_bases WHERE id = $1::uuid`, id) }) //nolint:errcheck
	return id
}

// Pins migration 0076's contract: per-user sha256 uniqueness and the two
// RESTRICT foreign keys that make an incomplete deleter fail loudly.
func TestUserFilesSchemaContract(t *testing.T) {
	pool := openMainPool(t)
	ctx := context.Background()
	// Cleanups run LIFO: user and KB are registered first so they run last.
	userID := seedUser(t, pool)
	kbID := seedKB(t, pool)

	var ufID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO user_files (owner_user_id, name, mime, size, sha256, storage_path)
		VALUES ($1::uuid, 'a.pdf', 'application/pdf', 3, repeat('a', 64), 'users/x/y')
		RETURNING id::text`, userID).Scan(&ufID); err != nil {
		t.Fatalf("insert user_file: %v", err)
	}
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM user_files WHERE id = $1::uuid`, ufID) }) //nolint:errcheck

	// Same owner + same sha256 -> unique violation.
	_, err := pool.Exec(ctx, `
		INSERT INTO user_files (owner_user_id, name, mime, size, sha256, storage_path)
		VALUES ($1::uuid, 'b.pdf', 'application/pdf', 3, repeat('a', 64), 'users/x/z')`, userID)
	if err == nil {
		t.Fatal("duplicate (owner, sha256) must be rejected")
	}

	// A KB copy referencing it blocks deleting the user_files row (RESTRICT).
	var fileID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO files (kb_id, name, type, status, storage_path, user_file_id)
		VALUES ($1::uuid, 'a.pdf', 'application/pdf', 'completed', 'users/x/y', $2::uuid)
		RETURNING id::text`, kbID, ufID).Scan(&fileID); err != nil {
		t.Fatalf("insert files: %v", err)
	}
	t.Cleanup(func() { pool.Exec(ctx, `DELETE FROM files WHERE id = $1::uuid`, fileID) }) //nolint:errcheck
	if _, err := pool.Exec(ctx, `DELETE FROM user_files WHERE id = $1::uuid`, ufID); err == nil {
		t.Fatal("deleting a user_files row with a KB copy must fail (ON DELETE RESTRICT)")
	}

	// The same library file twice in one KB -> unique violation (0077).
	if _, err := pool.Exec(ctx, `
		INSERT INTO files (kb_id, name, type, status, storage_path, user_file_id)
		VALUES ($1::uuid, 'a.pdf', 'application/pdf', 'completed', 'users/x/y', $2::uuid)`, kbID, ufID); err == nil {
		t.Fatal("a library file may be in a KB at most once")
	}

	// The owner cannot be deleted while they own a user_files row (RESTRICT).
	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1::uuid`, userID); err == nil {
		t.Fatal("deleting a user who owns library files must fail (ON DELETE RESTRICT)")
	}
}
