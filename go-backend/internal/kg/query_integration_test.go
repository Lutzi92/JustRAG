//go:build integration

// DB-backed tests for the kg package — exercise the real recursive CTE
// against a live Postgres. Gated by the `integration` build tag so
// `go test ./...` (without the tag) compiles only the pure-unit tests in
// query_test.go and stays green on machines without docker.
//
// CI runs these via `go test -tags=integration -race ./...`.

package kg

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)


func openKGTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("JUSTRAG_KG_TEST_DSN")
	if dsn == "" {
		// Fall back to assembling from the CI env vars so the test
		// runs in CI without a dedicated variable. Unset → skip.
		host := os.Getenv("DB_HOST")
		port := os.Getenv("DB_PORT")
		user := os.Getenv("DB_USER")
		pass := os.Getenv("DB_PASSWORD")
		name := os.Getenv("DB_NAME")
		if host == "" || port == "" || name == "" {
			t.Skip("kg DB tests require JUSTRAG_KG_TEST_DSN or DB_HOST/DB_PORT/DB_NAME")
		}
		dsn = fmt.Sprintf("postgres://%s:%s@%s:%s/%s",
			url.QueryEscape(user), url.QueryEscape(pass), host, port, name)
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// seedKB inserts a fresh knowledge_bases row + a small entity graph and
// returns the KB id. Cleanup via t.Cleanup truncates the kg tables for the
// KB (the kb_id FK is ON DELETE CASCADE so deleting the KB removes
// entities + edges).
//
// Graph shape (3 hops between A and D):
//
//	A —[works_in]→ B —[reports_to]→ C —[member_of]→ D
//
// Plus a reverse edge B —[hires]→ E so traversal exercises both
// directions per hop.
func seedKB(t *testing.T, pool *pgxpool.Pool) (kbID string, ids map[string]int64) {
	t.Helper()
	ctx := context.Background()

	var kb string
	if err := pool.QueryRow(ctx, `
		INSERT INTO knowledge_bases (name, user_id, description)
		VALUES ('kg-test', NULL, 'kg unit test KB')
		RETURNING id::text
	`).Scan(&kb); err != nil {
		t.Fatalf("insert KB: %v", err)
	}
	t.Cleanup(func() {
		// Cascade removes kg_entities + kg_edges.
		_, _ = pool.Exec(context.Background(), `DELETE FROM knowledge_bases WHERE id = $1::uuid`, kb)
	})

	ids = make(map[string]int64)
	insertEnt := func(name, typ string, aliases []string) int64 {
		var id int64
		if err := pool.QueryRow(ctx, `
			INSERT INTO kg_entities (kb_id, canonical_name, type, aliases)
			VALUES ($1::uuid, $2, $3, $4)
			RETURNING id
		`, kb, name, typ, aliases).Scan(&id); err != nil {
			t.Fatalf("insert entity %q: %v", name, err)
		}
		return id
	}
	ids["A"] = insertEnt("Alice", "person", []string{"alice", "Alice K."})
	ids["B"] = insertEnt("PPM-Team", "organization", []string{"ppm", "PPM-Team"})
	ids["C"] = insertEnt("HRZ", "organization", []string{"hrz"})
	ids["D"] = insertEnt("Justus-Liebig-Universität", "organization", []string{"jlu", "Universität"})
	ids["E"] = insertEnt("Bob", "person", []string{"bob"})

	insertEdge := func(src, dst int64, rel, evidence string) {
		if _, err := pool.Exec(ctx, `
			INSERT INTO kg_edges (kb_id, src_entity_id, dst_entity_id, rel, evidence)
			VALUES ($1::uuid, $2, $3, $4, $5)
		`, kb, src, dst, rel, evidence); err != nil {
			t.Fatalf("insert edge %s→%s: %v", rel, evidence, err)
		}
	}
	insertEdge(ids["A"], ids["B"], "works_in", "Alice works in the PPM-Team")
	insertEdge(ids["B"], ids["C"], "reports_to", "PPM-Team reports to HRZ")
	insertEdge(ids["C"], ids["D"], "member_of", "HRZ is part of JLU")
	insertEdge(ids["B"], ids["E"], "hires", "PPM-Team hired Bob")

	return kb, ids
}

func TestPgStore_LookupEntityByName_DB_CanonicalMatch(t *testing.T) {
	pool := openKGTestPool(t)
	store := NewPgStore(pool)
	kb, ids := seedKB(t, pool)

	got, err := store.LookupEntityByName(context.Background(), kb, "Alice")
	if err != nil {
		t.Fatalf("LookupEntityByName: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected at least one match for canonical name 'Alice'")
	}
	if got[0].ID != ids["A"] {
		t.Errorf("canonical match should land first: got id=%d, want %d", got[0].ID, ids["A"])
	}
	if got[0].CanonicalName != "Alice" {
		t.Errorf("CanonicalName: got %q, want %q", got[0].CanonicalName, "Alice")
	}
}

func TestPgStore_LookupEntityByName_DB_AliasMatch(t *testing.T) {
	pool := openKGTestPool(t)
	store := NewPgStore(pool)
	kb, ids := seedKB(t, pool)

	got, err := store.LookupEntityByName(context.Background(), kb, "ppm")
	if err != nil {
		t.Fatalf("LookupEntityByName: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected alias 'ppm' to match PPM-Team")
	}
	if got[0].ID != ids["B"] {
		t.Errorf("got id=%d, want PPM-Team id=%d", got[0].ID, ids["B"])
	}
}

func TestPgStore_LookupEntityByName_DB_CaseInsensitive(t *testing.T) {
	pool := openKGTestPool(t)
	store := NewPgStore(pool)
	kb, _ := seedKB(t, pool)

	for _, name := range []string{"ALICE", "alice", "Alice"} {
		got, err := store.LookupEntityByName(context.Background(), kb, name)
		if err != nil {
			t.Fatalf("LookupEntityByName(%q): %v", name, err)
		}
		if len(got) == 0 {
			t.Errorf("case %q: no match — case-insensitivity broken", name)
		}
	}
}

func TestPgStore_LookupSubgraph_DB_Depth1(t *testing.T) {
	pool := openKGTestPool(t)
	store := NewPgStore(pool)
	kb, ids := seedKB(t, pool)

	// Depth 1 from B should see A (in), C (out), E (out) — three
	// immediate neighbours.
	subg, err := store.LookupSubgraph(context.Background(), kb, ids["B"], 1)
	if err != nil {
		t.Fatalf("LookupSubgraph: %v", err)
	}
	if subg.Root.ID != ids["B"] {
		t.Errorf("Root.ID: got %d, want %d", subg.Root.ID, ids["B"])
	}

	gotIDs := make(map[int64]bool)
	for _, e := range subg.Entities {
		gotIDs[e.ID] = true
	}
	for _, want := range []int64{ids["A"], ids["B"], ids["C"], ids["E"]} {
		if !gotIDs[want] {
			t.Errorf("depth-1 from B should include entity id=%d, missing", want)
		}
	}
	// D is two hops away — must NOT appear at depth 1.
	if gotIDs[ids["D"]] {
		t.Error("depth-1 from B incorrectly includes D (2 hops away)")
	}
}

func TestPgStore_LookupSubgraph_DB_Depth2WalksBothDirections(t *testing.T) {
	pool := openKGTestPool(t)
	store := NewPgStore(pool)
	kb, ids := seedKB(t, pool)

	subg, err := store.LookupSubgraph(context.Background(), kb, ids["B"], 2)
	if err != nil {
		t.Fatalf("LookupSubgraph: %v", err)
	}

	// At depth 2 from B, we walk forward to D (B→C→D) and the
	// reverse hops still confined to depth 2 — A is also a
	// neighbour. The recursive CTE walks both edge directions
	// per hop, so coverage must include reverse-edge entities.
	gotIDs := make(map[int64]bool)
	for _, e := range subg.Entities {
		gotIDs[e.ID] = true
	}
	for _, want := range []int64{ids["A"], ids["B"], ids["C"], ids["D"], ids["E"]} {
		if !gotIDs[want] {
			t.Errorf("depth-2 from B should include entity id=%d, missing", want)
		}
	}
}

func TestPgStore_LookupSubgraph_DB_DepthClamping(t *testing.T) {
	pool := openKGTestPool(t)
	store := NewPgStore(pool)
	kb, ids := seedKB(t, pool)

	// depth=0 must clamp up to 1.
	subg, err := store.LookupSubgraph(context.Background(), kb, ids["B"], 0)
	if err != nil {
		t.Fatalf("LookupSubgraph(depth=0): %v", err)
	}
	if len(subg.Entities) <= 1 {
		t.Errorf("depth=0 should clamp to 1 and include neighbours; got only %d entities",
			len(subg.Entities))
	}

	// depth=99 must clamp down to 2 (no D-via-3-hops in our graph, but
	// the query shouldn't explode).
	if _, err := store.LookupSubgraph(context.Background(), kb, ids["B"], 99); err != nil {
		t.Fatalf("LookupSubgraph(depth=99): %v", err)
	}
}

func TestPgStore_LookupSubgraph_DB_MissingRoot(t *testing.T) {
	pool := openKGTestPool(t)
	store := NewPgStore(pool)
	kb, _ := seedKB(t, pool)

	// 0 is a valid bigint but no entity has it as ID — must return
	// the zero-value Subgraph without error (call site discriminates
	// on Subgraph.Root.ID).
	subg, err := store.LookupSubgraph(context.Background(), kb, 0, 1)
	if err != nil {
		t.Fatalf("LookupSubgraph(missing): %v", err)
	}
	if subg.Root.ID != 0 || len(subg.Entities) != 0 {
		t.Errorf("expected empty Subgraph for missing root, got %+v", subg)
	}
}

func TestPgStore_MatchAliasesInTokens_DB_OverlapOrder(t *testing.T) {
	pool := openKGTestPool(t)
	store := NewPgStore(pool)
	kb, ids := seedKB(t, pool)

	// "ppm" matches PPM-Team (1 alias), "alice" matches Alice
	// (1 alias) — ranking is by overlap count, ties broken by id.
	// Both should return; the order matters less than "both present".
	got, err := store.MatchAliasesInTokens(context.Background(), kb,
		[]string{"ppm", "alice", "nothing-matches", " ", ""})
	if err != nil {
		t.Fatalf("MatchAliasesInTokens: %v", err)
	}
	gotIDs := make(map[int64]bool)
	for _, e := range got {
		gotIDs[e.ID] = true
	}
	if !gotIDs[ids["A"]] {
		t.Error("expected Alice to match alias 'alice'")
	}
	if !gotIDs[ids["B"]] {
		t.Error("expected PPM-Team to match alias 'ppm'")
	}
}

func TestPgStore_MatchAliasesInTokens_DB_DedupTokens(t *testing.T) {
	pool := openKGTestPool(t)
	store := NewPgStore(pool)
	kb, ids := seedKB(t, pool)

	// Duplicated + whitespace-padded tokens. The function dedupes
	// + trims client-side; the query must still hit PPM-Team.
	got, err := store.MatchAliasesInTokens(context.Background(), kb,
		[]string{"ppm", " ppm ", "PPM", "ppm"})
	if err != nil {
		t.Fatalf("MatchAliasesInTokens: %v", err)
	}
	found := false
	for _, e := range got {
		if e.ID == ids["B"] {
			found = true
			break
		}
	}
	if !found {
		t.Error("dedup/trim path must still match PPM-Team")
	}
}

