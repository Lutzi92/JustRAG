//go:build integration

// Transaction-level tests for the superadmin owner transfer. Require a live
// main Postgres; skipped when DB_* env is unset.

package adminkboverview_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/adminkboverview"
)

func openMainPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	host, port, name := os.Getenv("DB_HOST"), os.Getenv("DB_PORT"), os.Getenv("DB_NAME")
	if host == "" || port == "" || name == "" {
		t.Skip("KB owner-transfer tests require DB_* env (main Postgres)")
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

// seedUser inserts a throwaway user and returns its id.
func seedUser(t *testing.T, pool *pgxpool.Pool, username string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	err := pool.QueryRow(ctx, `
		INSERT INTO users (username, password_hash, role)
		VALUES ($1, 'x-not-a-real-hash', 'user')
		RETURNING id::text`, username).Scan(&id)
	if err != nil {
		t.Fatalf("insert user %s: %v", username, err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM users WHERE id = $1::uuid`, id) //nolint:errcheck
	})
	return id
}

// seedPersonalKB inserts a personal KB owned by ownerID ("" for ownerless).
func seedPersonalKB(t *testing.T, pool *pgxpool.Pool, ownerID string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	var owner any
	if ownerID != "" {
		owner = ownerID
	}
	err := pool.QueryRow(ctx, `
		INSERT INTO knowledge_bases (name, description, is_global, user_id)
		VALUES ('kb-transfer-test', 'fixture', false, $1::uuid)
		RETURNING id::text`, owner).Scan(&id)
	if err != nil {
		t.Fatalf("insert kb: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM knowledge_bases WHERE id = $1::uuid`, id) //nolint:errcheck
	})
	return id
}

// readShare returns the permission of (kbID, userID), or "" when absent.
func readShare(t *testing.T, pool *pgxpool.Pool, kbID, userID string) string {
	t.Helper()
	var perm string
	err := pool.QueryRow(context.Background(),
		`SELECT permission FROM knowledge_base_shares WHERE kb_id = $1::uuid AND user_id = $2::uuid`,
		kbID, userID).Scan(&perm)
	if err != nil {
		return ""
	}
	return perm
}

// readOwner returns knowledge_bases.user_id as text, or "" when NULL.
func readOwner(t *testing.T, pool *pgxpool.Pool, kbID string) string {
	t.Helper()
	var owner *string
	if err := pool.QueryRow(context.Background(),
		`SELECT user_id::text FROM knowledge_bases WHERE id = $1::uuid`, kbID).Scan(&owner); err != nil {
		t.Fatalf("read owner: %v", err)
	}
	if owner == nil {
		return ""
	}
	return *owner
}

func TestTransferKBOwner_DemotesPreviousOwnerToEditor(t *testing.T) {
	pool := openMainPool(t)
	store := adminkboverview.NewStore(pool)

	oldOwner := seedUser(t, pool, "transfer-old-owner")
	newOwner := seedUser(t, pool, "transfer-new-owner")
	kbID := seedPersonalKB(t, pool, oldOwner)

	if err := store.TransferKBOwner(context.Background(), kbID, newOwner, &oldOwner); err != nil {
		t.Fatalf("TransferKBOwner: %v", err)
	}

	if got := readOwner(t, pool, kbID); got != newOwner {
		t.Errorf("owner = %q, want %q", got, newOwner)
	}
	if got := readShare(t, pool, kbID, oldOwner); got != "edit" {
		t.Errorf("previous owner share = %q, want edit", got)
	}
}

func TestTransferKBOwner_DropsNewOwnerStaleShare(t *testing.T) {
	pool := openMainPool(t)
	store := adminkboverview.NewStore(pool)
	ctx := context.Background()

	oldOwner := seedUser(t, pool, "transfer-stale-old")
	newOwner := seedUser(t, pool, "transfer-stale-new")
	kbID := seedPersonalKB(t, pool, oldOwner)

	// The new owner already had a view share before the transfer.
	if _, err := pool.Exec(ctx,
		`INSERT INTO knowledge_base_shares (kb_id, user_id, permission) VALUES ($1::uuid, $2::uuid, 'view')`,
		kbID, newOwner); err != nil {
		t.Fatalf("seed share: %v", err)
	}

	if err := store.TransferKBOwner(ctx, kbID, newOwner, &oldOwner); err != nil {
		t.Fatalf("TransferKBOwner: %v", err)
	}

	if got := readShare(t, pool, kbID, newOwner); got != "" {
		t.Errorf("new owner still has share %q, want none (ownership supersedes)", got)
	}
}

func TestTransferKBOwner_OwnerlessKBCreatesNoShare(t *testing.T) {
	pool := openMainPool(t)
	store := adminkboverview.NewStore(pool)

	newOwner := seedUser(t, pool, "transfer-orphan-new")
	kbID := seedPersonalKB(t, pool, "")

	if err := store.TransferKBOwner(context.Background(), kbID, newOwner, nil); err != nil {
		t.Fatalf("TransferKBOwner: %v", err)
	}

	if got := readOwner(t, pool, kbID); got != newOwner {
		t.Errorf("owner = %q, want %q", got, newOwner)
	}
	var shareCount int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*)::int FROM knowledge_base_shares WHERE kb_id = $1::uuid`, kbID).Scan(&shareCount); err != nil {
		t.Fatalf("count shares: %v", err)
	}
	if shareCount != 0 {
		t.Errorf("share count = %d, want 0", shareCount)
	}
}
