//go:build integration

// CopyFileIndex copies one file's whole vector-DB index to a new file/KB in a
// single transaction; these tests run against the live pgvector store.

package vector

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	cpDonor   = "aaaaaaaa-0000-4000-8000-0000000000d1"
	cpTarget  = "aaaaaaaa-0000-4000-8000-0000000000e1"
	cpTarget2 = "aaaaaaaa-0000-4000-8000-0000000000e2"
	cpKB1     = "aaaaaaaa-0000-4000-8000-0000000000b1"
	cpKB2     = "aaaaaaaa-0000-4000-8000-0000000000b2"
	cpEmpty   = "aaaaaaaa-0000-4000-8000-0000000000f1"
	cpDim     = 8
)

func cpEmb(seed int) string {
	s := "["
	for i := 0; i < cpDim; i++ {
		if i > 0 {
			s += ","
		}
		s += fmt.Sprintf("%g", float64(seed)/float64(i+3))
	}
	return s + "]"
}

func cpCount(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", sql, err)
	}
	return n
}

func cpCleanup(ctx context.Context, svc *ChunkService, pool *pgxpool.Pool) {
	for _, f := range []string{cpDonor, cpTarget, cpTarget2, cpEmpty} {
		_ = svc.DeleteChunksByFileIDAllDims(ctx, f)
		_ = svc.DeleteParentChunksByFileID(ctx, f)
		_, _ = pool.Exec(ctx, `DELETE FROM `+GetHyPETableName(cpDim)+` WHERE file_id = $1::uuid`, f)
	}
}

func TestCopyFileIndex(t *testing.T) {
	pool := openTestVectorPool(t)
	svc := NewChunkService(pool)
	ctx := context.Background()

	if err := EnsureChunkTable(ctx, PgxpoolExec{Pool: pool}, cpDim); err != nil {
		t.Fatal(err)
	}
	if err := EnsureHyPETable(ctx, PgxpoolExec{Pool: pool}, cpDim); err != nil {
		t.Fatal(err)
	}
	chunkT := GetVectorTableName(cpDim)
	hypeT := GetHyPETableName(cpDim)
	cpCleanup(ctx, svc, pool)
	t.Cleanup(func() { cpCleanup(context.Background(), svc, pool) })

	const (
		p1 = "bbbbbbbb-0000-4000-8000-000000000001"
		c1 = "cccccccc-0000-4000-8000-000000000001" // leaf with parent
		c2 = "cccccccc-0000-4000-8000-000000000002" // leaf with prefix, raptor child
		c3 = "cccccccc-0000-4000-8000-000000000003" // leaf, raptor child
		s1 = "cccccccc-0000-4000-8000-000000000004" // summary
	)
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed %q: %v", sql, err)
		}
	}
	mustExec(`DELETE FROM document_chunk_parents WHERE id = $1::uuid`, p1)
	for _, id := range []string{c1, c2, c3, s1} {
		mustExec(`DELETE FROM `+chunkT+` WHERE id = $1::uuid`, id)
	}
	mustExec(`INSERT INTO document_chunk_parents (id, kb_id, file_id, content, contextual_prefix, metadata, created_at)
		VALUES ($1, $2, $3, 'parent text', 'pp', '{"k":1}', now() - interval '1 hour')`, p1, cpKB1, cpDonor)
	ins := `INSERT INTO ` + chunkT + ` (id, kb_id, file_id, content, contextual_prefix, content_hash, embedding,
		metadata, parent_chunk_id, node_kind, tree_level, raptor_parent_id, created_at, vector_index, vector_index_simple)
		VALUES ($1,$2,$3,$4::text,$5::text,$6,$7::vector,'{"chunkIndex":0}',$8,$9,$10,$11, now() - ($12 || ' minutes')::interval,
		to_tsvector('german', COALESCE($5::text,'') || ' ' || $4::text), to_tsvector('simple', COALESCE($5::text,'') || ' ' || $4::text))`
	mustExec(ins, c1, cpKB1, cpDonor, "running dogs", nil, "h1", cpEmb(1), p1, "leaf", 0, nil, "30")
	mustExec(ins, c2, cpKB1, cpDonor, "jumping cats", "prefix about houses", "h2", cpEmb(2), nil, "leaf", 0, s1, "20")
	mustExec(ins, c3, cpKB1, cpDonor, "swimming fish", nil, "h3", cpEmb(3), nil, "leaf", 0, s1, "10")
	mustExec(ins, s1, cpKB1, cpDonor, "summary of things", nil, "h4", cpEmb(4), nil, "summary", 1, nil, "5")
	lowParts := make([]string, 256)
	for i := range lowParts {
		lowParts[i] = fmt.Sprintf("%d", i%7)
	}
	mustExec(`UPDATE `+chunkT+` SET embedding_low = $2::vector WHERE id = $1::uuid`, c1, "["+strings.Join(lowParts, ",")+"]")
	mustExec(`INSERT INTO `+hypeT+` (kb_id, file_id, parent_chunk_id, question, embedding)
		VALUES ($1,$2,$3,'q one?',$4::vector), ($1,$2,$3,'q two?',$5::vector)`, cpKB1, cpDonor, c1, cpEmb(5), cpEmb(6))

	ids := []string{c1, c2, c3, s1, p1}

	res, err := svc.CopyFileIndex(ctx, cpDonor, cpTarget, cpKB2, "english")
	if err != nil {
		t.Fatalf("CopyFileIndex: %v", err)
	}
	if res.Dimensions != cpDim {
		t.Errorf("Dimensions = %d, want %d", res.Dimensions, cpDim)
	}
	if len(res.ChunkIDMap) != 4 {
		t.Errorf("ChunkIDMap has %d entries, want 4", len(res.ChunkIDMap))
	}
	donorSet := map[string]bool{c1: true, c2: true, c3: true, s1: true, p1: true}
	for old, nw := range res.ChunkIDMap {
		if donorSet[nw] || old == nw {
			t.Errorf("map %s -> %s reuses a donor id", old, nw)
		}
	}

	// 1. counts
	counts := func(file string) [3]int {
		return [3]int{
			cpCount(t, pool, `SELECT count(*) FROM `+chunkT+` WHERE file_id = $1::uuid`, file),
			cpCount(t, pool, `SELECT count(*) FROM document_chunk_parents WHERE file_id = $1::uuid`, file),
			cpCount(t, pool, `SELECT count(*) FROM `+hypeT+` WHERE file_id = $1::uuid`, file),
		}
	}
	if d, g := counts(cpDonor), counts(cpTarget); d != g || d != [3]int{4, 1, 2} {
		t.Fatalf("counts donor=%v target=%v, want both [4 1 2]", d, g)
	}

	// 2. no references to donor ids
	if n := cpCount(t, pool, `SELECT count(*) FROM `+chunkT+` WHERE file_id = $1::uuid
		AND (id = ANY($2::uuid[]) OR parent_chunk_id = ANY($2::uuid[]) OR raptor_parent_id = ANY($2::uuid[]))`, cpTarget, ids); n != 0 {
		t.Errorf("%d target chunks reference donor ids", n)
	}
	if n := cpCount(t, pool, `SELECT count(*) FROM `+hypeT+` WHERE file_id = $1::uuid AND parent_chunk_id = ANY($2::uuid[])`, cpTarget, ids); n != 0 {
		t.Errorf("%d target HyPE rows reference donor chunk ids", n)
	}
	if n := cpCount(t, pool, `SELECT count(*) FROM document_chunk_parents WHERE file_id = $1::uuid AND id = ANY($2::uuid[])`, cpTarget, ids); n != 0 {
		t.Errorf("target parent reuses donor id")
	}
	if n := cpCount(t, pool, `SELECT count(*) FROM `+chunkT+` WHERE file_id = $1::uuid AND kb_id <> $2::uuid`, cpTarget, cpKB2); n != 0 {
		t.Errorf("%d target chunks in wrong kb", n)
	}
	if n := cpCount(t, pool, `SELECT count(*) FROM `+chunkT+` c WHERE c.file_id = $1::uuid AND c.parent_chunk_id IS NOT NULL
		AND NOT EXISTS (SELECT 1 FROM document_chunk_parents p WHERE p.id = c.parent_chunk_id AND p.file_id = $1::uuid AND p.kb_id = $2::uuid)`, cpTarget, cpKB2); n != 0 {
		t.Errorf("dangling target parent refs: %d", n)
	}
	if n := cpCount(t, pool, `SELECT count(*) FROM `+chunkT+` c WHERE c.file_id = $1::uuid AND c.parent_chunk_id IS NOT NULL`, cpTarget); n != 1 {
		t.Errorf("target chunks with a parent = %d, want 1", n)
	}
	if n := cpCount(t, pool, `SELECT count(*) FROM `+hypeT+` h WHERE h.file_id = $1::uuid AND h.kb_id = $3::uuid
		AND EXISTS (SELECT 1 FROM `+chunkT+` c WHERE c.id = h.parent_chunk_id AND c.file_id = $1::uuid AND c.id = $2::uuid)`,
		cpTarget, res.ChunkIDMap[c1], cpKB2); n != 2 {
		t.Errorf("HyPE rows pointing at the remapped leaf: %d, want 2", n)
	}

	// 3. RAPTOR shape
	var sumID string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM `+chunkT+` WHERE file_id=$1::uuid AND node_kind='summary'`, cpTarget).Scan(&sumID); err != nil {
		t.Fatal(err)
	}
	if n := cpCount(t, pool, `SELECT count(*) FROM `+chunkT+` WHERE file_id=$1::uuid AND raptor_parent_id=$2::uuid AND node_kind='leaf'`, cpTarget, sumID); n != 2 {
		t.Errorf("target summary has %d children, want 2", n)
	}
	if sumID != res.ChunkIDMap[s1] {
		t.Errorf("summary id %s != map[%s] %s", sumID, s1, res.ChunkIDMap[s1])
	}

	// 4. content, embeddings, created_at preserved
	if n := cpCount(t, pool, `SELECT count(*) FROM `+chunkT+` d JOIN `+chunkT+` t ON t.content = d.content
		WHERE d.file_id=$1::uuid AND t.file_id=$2::uuid AND d.embedding::text = t.embedding::text
		AND d.embedding_low::text IS NOT DISTINCT FROM t.embedding_low::text
		AND d.created_at = t.created_at AND d.content_hash = t.content_hash AND d.tree_level = t.tree_level
		AND d.node_kind = t.node_kind
		AND d.contextual_prefix IS NOT DISTINCT FROM t.contextual_prefix AND d.metadata = t.metadata`, cpDonor, cpTarget); n != 4 {
		t.Errorf("%d of 4 chunks identical in content/embedding/created_at", n)
	}
	if n := cpCount(t, pool, `SELECT count(*) FROM `+hypeT+` d JOIN `+hypeT+` t ON t.question = d.question
		WHERE d.file_id=$1::uuid AND t.file_id=$2::uuid AND d.embedding::text = t.embedding::text`, cpDonor, cpTarget); n != 2 {
		t.Errorf("%d of 2 HyPE rows identical", n)
	}

	// 5. tsvectors recomputed with the target config
	if n := cpCount(t, pool, `SELECT count(*) FROM `+chunkT+` WHERE file_id=$1::uuid
		AND vector_index = to_tsvector('english', COALESCE(contextual_prefix,'') || ' ' || content)
		AND vector_index_simple = to_tsvector('simple', COALESCE(contextual_prefix,'') || ' ' || content)`, cpTarget); n != 4 {
		t.Errorf("%d of 4 target tsvectors match english/simple recompute", n)
	}
	// 'running' stems to 'run' only under english; the donor was built with german.
	if n := cpCount(t, pool, `SELECT count(*) FROM `+chunkT+` WHERE file_id=$1::uuid AND vector_index @@ to_tsquery('english','run')`, cpTarget); n != 1 {
		t.Errorf("english stem match on target = %d, want 1", n)
	}
	if n := cpCount(t, pool, `SELECT count(*) FROM `+chunkT+` WHERE file_id=$1::uuid AND vector_index @@ to_tsquery('english','run')`, cpDonor); n != 0 {
		t.Errorf("donor unexpectedly matches the english stem: %d", n)
	}

	// 6. donor unchanged
	if n := cpCount(t, pool, `SELECT count(*) FROM `+chunkT+` WHERE id = ANY($1::uuid[]) AND file_id=$2::uuid AND kb_id=$3::uuid`, ids, cpDonor, cpKB1); n != 4 {
		t.Errorf("donor chunks changed: %d", n)
	}
	if n := cpCount(t, pool, `SELECT count(*) FROM `+chunkT+` WHERE file_id=$1::uuid AND raptor_parent_id = $2::uuid`, cpDonor, s1); n != 2 {
		t.Errorf("donor raptor children changed: %d", n)
	}

	// 7. retry is idempotent
	res2, err := svc.CopyFileIndex(ctx, cpDonor, cpTarget, cpKB2, "english")
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if g := counts(cpTarget); g != [3]int{4, 1, 2} {
		t.Errorf("after retry counts = %v", g)
	}
	if len(res2.ChunkIDMap) != 4 {
		t.Errorf("retry map size %d", len(res2.ChunkIDMap))
	}

	// 8. deleting the donor leaves the target intact
	if err := svc.DeleteChunksByFileIDAllDims(ctx, cpDonor); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteParentChunksByFileID(ctx, cpDonor); err != nil {
		t.Fatal(err)
	}
	mustExec(`DELETE FROM `+hypeT+` WHERE file_id = $1::uuid`, cpDonor)
	if g := counts(cpTarget); g != [3]int{4, 1, 2} {
		t.Errorf("target after donor delete = %v", g)
	}
	if g := counts(cpDonor); g != [3]int{0, 0, 0} {
		t.Errorf("donor after delete = %v", g)
	}

	if n := cpCount(t, pool, `SELECT count(*) FROM `+chunkT+` WHERE file_id=$1::uuid AND embedding_low IS NOT NULL`, cpTarget); n != 1 {
		t.Errorf("target embedding_low non-null rows = %d, want 1", n)
	}

	// empty pgConfig falls back to 'simple'
	if _, err := svc.CopyFileIndex(ctx, cpTarget, cpTarget2, cpKB2, ""); err != nil {
		t.Fatalf("empty pgConfig: %v", err)
	}
	if n := cpCount(t, pool, `SELECT count(*) FROM `+chunkT+` WHERE file_id=$1::uuid
		AND vector_index = to_tsvector('simple', COALESCE(contextual_prefix,'') || ' ' || content)`, cpTarget2); n != 4 {
		t.Errorf("%d of 4 tsvectors match simple fallback", n)
	}

	// 9. donor without rows; pre-existing target rows must be cleared
	if g := counts(cpTarget2); g != [3]int{4, 1, 2} {
		t.Fatalf("precondition: target2 counts = %v", g)
	}
	res3, err := svc.CopyFileIndex(ctx, cpEmpty, cpTarget2, cpKB2, "english")
	if err != nil {
		t.Fatalf("empty donor: %v", err)
	}
	if res3.Dimensions != 0 || len(res3.ChunkIDMap) != 0 {
		t.Errorf("empty donor result = %+v", res3)
	}
	if g := counts(cpTarget2); g != [3]int{0, 0, 0} {
		t.Errorf("target rows not cleared for empty donor: %v", g)
	}

	// 10. DeleteHyPEByFileIDAllDims (copy-failure cleanup) removes only the
	// named file's HyPE rows.
	if _, err := svc.CopyFileIndex(ctx, cpTarget, cpTarget2, cpKB2, "english"); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteHyPEByFileIDAllDims(ctx, cpTarget2); err != nil {
		t.Fatalf("DeleteHyPEByFileIDAllDims: %v", err)
	}
	if g := counts(cpTarget2); g != [3]int{4, 1, 0} {
		t.Errorf("target2 after hype delete = %v, want [4 1 0]", g)
	}
	if g := counts(cpTarget); g != [3]int{4, 1, 2} {
		t.Errorf("other file's hype touched: %v", g)
	}
}

// A donor row that vanishes between chunk_map and the chunk insert (a
// concurrent re-ingest under READ COMMITTED) must fail the copy and roll it
// back, never commit a partial index. The seam deletes one donor chunk inside
// the copy's transaction at exactly that point.
func TestCopyFileIndex_DonorRowsVanishMidCopyFails(t *testing.T) {
	pool := openTestVectorPool(t)
	svc := NewChunkService(pool)
	ctx := context.Background()
	if err := EnsureChunkTable(ctx, PgxpoolExec{Pool: pool}, cpDim); err != nil {
		t.Fatal(err)
	}
	chunkT := GetVectorTableName(cpDim)
	cpCleanup(ctx, svc, pool)
	t.Cleanup(func() { cpCleanup(context.Background(), svc, pool) })

	const (
		v1 = "dddddddd-0000-4000-8000-000000000001"
		v2 = "dddddddd-0000-4000-8000-000000000002"
	)
	ins := `INSERT INTO ` + chunkT + ` (id, kb_id, file_id, content, content_hash, embedding, metadata, node_kind, tree_level,
		vector_index, vector_index_simple)
		VALUES ($1,$2,$3,$4::text,$5,$6::vector,'{}','leaf',0, to_tsvector('simple',$4::text), to_tsvector('simple',$4::text))`
	for i, id := range []string{v1, v2} {
		if _, err := pool.Exec(ctx, `DELETE FROM `+chunkT+` WHERE id = $1::uuid`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, ins, id, cpKB1, cpDonor, fmt.Sprintf("text %d", i), fmt.Sprintf("hv%d", i), cpEmb(i+1)); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	copyAfterChunkMapHook = func(ctx context.Context, tx pgx.Tx, table string) error {
		_, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE id = $1::uuid`, v2)
		return err
	}
	t.Cleanup(func() { copyAfterChunkMapHook = nil })

	_, err := svc.CopyFileIndex(ctx, cpDonor, cpTarget, cpKB2, "simple")
	if err == nil || !strings.Contains(err.Error(), "donor changed during copy") {
		t.Fatalf("CopyFileIndex err = %v, want a donor-changed error", err)
	}
	if n := cpCount(t, pool, `SELECT count(*) FROM `+chunkT+` WHERE file_id = $1::uuid`, cpTarget); n != 0 {
		t.Errorf("target rows = %d, want 0 (rolled back)", n)
	}
	// The hook's delete ran in the copy's (rolled-back) transaction.
	if n := cpCount(t, pool, `SELECT count(*) FROM `+chunkT+` WHERE file_id = $1::uuid`, cpDonor); n != 2 {
		t.Errorf("donor rows = %d, want 2", n)
	}
}
