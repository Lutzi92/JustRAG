//go:build integration

package processor

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/prompts"
)

func TestPGKGCache_RoundTripMissesAndCascade(t *testing.T) {
	pool := openKGStoreTestPool(t)
	ctx := context.Background()

	var userID, ufID string
	username := fmt.Sprintf("kgcache-%d", time.Now().UnixNano())
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (username, password_hash, role) VALUES ($1, 'x', 'user') RETURNING id::text`,
		username).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() { pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1::uuid`, userID) }) //nolint:errcheck
	if err := pool.QueryRow(ctx, `
		INSERT INTO user_files (owner_user_id, name, mime, size, sha256, storage_path)
		VALUES ($1::uuid, 'a.txt', 'text/plain', 3, repeat('c', 64), 'users/x/y') RETURNING id::text`,
		userID).Scan(&ufID); err != nil {
		t.Fatalf("insert user_file: %v", err)
	}
	t.Cleanup(func() { pool.Exec(context.Background(), `DELETE FROM user_files WHERE id=$1::uuid`, ufID) }) //nolint:errcheck

	cache := newPGKGCache(pool)
	ext := ai.KGExtraction{
		Entities:  []ai.KGEntity{{Name: "X", Type: "concept"}, {Name: "Y", Type: "concept"}},
		Relations: []ai.KGRelation{{Src: "X", Dst: "Y", Rel: "rel", Evidence: "X rel Y"}},
	}
	if err := cache.Put(ctx, ufID, "h1", "m1", "de", ext); err != nil {
		t.Fatalf("put: %v", err)
	}
	// Idempotent re-put.
	if err := cache.Put(ctx, ufID, "h1", "m1", "de", ext); err != nil {
		t.Fatalf("re-put: %v", err)
	}
	got, ok, err := cache.Get(ctx, ufID, "h1", "m1", "de")
	if err != nil || !ok {
		t.Fatalf("get hit: ok=%v err=%v", ok, err)
	}
	if len(got.Entities) != 2 || len(got.Relations) != 1 || got.Relations[0].Rel != "rel" {
		t.Errorf("round trip mismatch: %+v", got)
	}

	for name, args := range map[string][4]string{
		"model": {"h1", "m2", "de", ""},
		"lang":  {"h1", "m1", "en", ""},
		"hash":  {"h2", "m1", "de", ""},
	} {
		if _, ok, err := cache.Get(ctx, ufID, args[0], args[1], args[2]); err != nil || ok {
			t.Errorf("different %s must miss: ok=%v err=%v", name, ok, err)
		}
	}

	// A row written under another prompt version must miss.
	if _, err := pool.Exec(ctx, `
		INSERT INTO kg_extraction_cache (user_file_id, content_hash, model, prompt_version, lang, extraction)
		VALUES ($1::uuid, 'h3', 'm1', $2, 'de', '{}'::jsonb)`, ufID, prompts.KGPromptVersion+1); err != nil {
		t.Fatalf("seed other-version row: %v", err)
	}
	if _, ok, _ := cache.Get(ctx, ufID, "h3", "m1", "de"); ok {
		t.Error("a different prompt_version must miss")
	}

	if _, err := pool.Exec(ctx, `DELETE FROM user_files WHERE id=$1::uuid`, ufID); err != nil {
		t.Fatalf("delete user_file: %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM kg_extraction_cache WHERE user_file_id=$1::uuid`, ufID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("cache rows must cascade-delete with the user file, found %d", n)
	}
}
