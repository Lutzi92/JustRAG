//go:build integration

package processor

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/ai"
)

func openKGStoreTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("JUSTRAG_KG_TEST_DSN")
	if dsn == "" {
		host, port := os.Getenv("DB_HOST"), os.Getenv("DB_PORT")
		user, pass, name := os.Getenv("DB_USER"), os.Getenv("DB_PASSWORD"), os.Getenv("DB_NAME")
		if host == "" || port == "" || name == "" {
			t.Skip("kg store DB test requires JUSTRAG_KG_TEST_DSN or DB_HOST/DB_PORT/DB_NAME")
		}
		dsn = fmt.Sprintf("postgres://%s:%s@%s:%s/%s", url.QueryEscape(user), url.QueryEscape(pass), host, port, name)
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestPersistKGExtraction_StampsFileLinkage(t *testing.T) {
	pool := openKGStoreTestPool(t)
	ctx := context.Background()

	var kb string
	if err := pool.QueryRow(ctx, `
		INSERT INTO knowledge_bases (name, user_id, description)
		VALUES ('kg-persist-test', NULL, 'persist test') RETURNING id::text`).Scan(&kb); err != nil {
		t.Fatalf("insert KB: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM knowledge_bases WHERE id=$1::uuid`, kb) })

	var fileID, chunkID string
	_ = pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&fileID)
	_ = pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&chunkID)

	store := newKGStore(pool)
	ext := ai.KGExtraction{
		Entities:  []ai.KGEntity{{Name: "Alice", Type: "person"}, {Name: "PPM", Type: "org"}},
		Relations: []ai.KGRelation{{Src: "Alice", Dst: "PPM", Rel: "works_in", Evidence: "Alice works in PPM"}},
	}
	if _, _, _, err := store.persistKGExtraction(ctx, kb, fileID, chunkID, ext); err != nil {
		t.Fatalf("persistKGExtraction: %v", err)
	}

	var edgeFile int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM kg_edges WHERE kb_id=$1::uuid AND file_id=$2::uuid`, kb, fileID).Scan(&edgeFile); err != nil {
		t.Fatal(err)
	}
	if edgeFile != 1 {
		t.Errorf("expected 1 edge stamped with file_id, got %d", edgeFile)
	}

	var links int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM kg_entity_files WHERE kb_id=$1::uuid AND file_id=$2::uuid`, kb, fileID).Scan(&links); err != nil {
		t.Fatal(err)
	}
	if links != 2 {
		t.Errorf("expected 2 entity-file links, got %d", links)
	}
}
