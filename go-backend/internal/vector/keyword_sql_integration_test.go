//go:build integration

// buildKeywordSQL's BM25 scoring mode reads real per-KB corpus statistics
// (bm25_kb_stats_<dim> / bm25_term_stats_<dim>) that only a live Postgres
// with the migrations applied can provide, so this is an integration test
// gated the same way as bm25_stats_integration_test.go: build tag
// `integration`, skipped when DB_*/VECTOR_DB_* env vars are unset.

package vector

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// TestBuildKeywordSQL_BM25RanksByIDF seeds a KB with 4 chunks in
// document_chunks_768 — "Rechenzentrum" appears in ALL FOUR (so it barely
// discriminates), "Stud.IP" appears in exactly ONE. The three non-target
// chunks each repeat "Rechenzentrum" many times (high raw term frequency)
// so that a pure term-frequency ranking (no IDF) would rank one of THEM
// above the target — isolating the effect Task 6 actually adds: BM25's
// IDF factor, which rewards "Stud.IP"'s rarity (df=1 of 4) far more than
// "Rechenzentrum"'s ubiquity (df=4 of 4) discounts it.
//
// Named mutation (per the brief): remove the `ln(1 + (N-n+0.5)/(n+0.5))`
// IDF factor from bm25ArmCTE's sc CTE (i.e. score purely on tf saturation)
// -> the "bm25 ranks the Stud.IP chunk strictly first" assertion below
// fails, because the three high-repetition chunks' pure-TF score on
// "Rechenzentrum" alone exceeds the Stud.IP chunk's two-term, low-TF sum
// (verified by hand: TF_sat(1)+TF_sat(1) ≈ 2.0 < TF_sat(20) ≈ 2.08 at
// k1=1.2, dl≈avgdl — see the design rationale in the task-6 report).
func TestBuildKeywordSQL_BM25RanksByIDF(t *testing.T) {
	mainPool, vectorPool := openBM25TestPools(t)
	ctx := context.Background()

	// Self-contained: don't rely on bm25_stats_integration_test.go's
	// TestBM25StatsRefreshAndStaleness having run first (and having left
	// the tables behind) to create these tables — EnsureBM25StatsTables
	// is idempotent, so calling it here makes this test runnable alone
	// (e.g. -run TestBuildKeywordSQL_BM25RanksByIDF) against a fresh DB.
	if err := EnsureBM25StatsTables(ctx, PgxpoolExec{Pool: vectorPool}, 768); err != nil {
		t.Fatalf("EnsureBM25StatsTables(768): %v", err)
	}

	kbID := uuid.New()
	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = vectorPool.Exec(cctx, `DELETE FROM document_chunks_768 WHERE kb_id = $1`, kbID)
		_, _ = vectorPool.Exec(cctx, `DELETE FROM bm25_kb_stats_768 WHERE kb_id = $1`, kbID)
		_, _ = vectorPool.Exec(cctx, `DELETE FROM bm25_term_stats_768 WHERE kb_id = $1`, kbID)
	})

	var targetChunkID uuid.UUID
	insertChunk := func(content string, isTarget bool) uuid.UUID {
		t.Helper()
		id := uuid.New()
		_, err := vectorPool.Exec(ctx, `
			INSERT INTO document_chunks_768 (id, kb_id, file_id, content, vector_index, vector_index_simple)
			VALUES ($1, $2, $3, $4, to_tsvector('german', $4), to_tsvector('simple', $4))
		`, id, kbID, uuid.New(), content)
		if err != nil {
			t.Fatalf("insert chunk: %v", err)
		}
		if isTarget {
			targetChunkID = id
		}
		return id
	}

	// Target: both query terms, each appearing once.
	insertChunk("Das Stud.IP Portal und das Rechenzentrum sind heute erreichbar.", true)
	// Three chunks: only the ubiquitous term, repeated heavily (high raw
	// TF) so a TF-only ranking would favor them over the target.
	repeated := "Rechenzentrum Rechenzentrum Rechenzentrum Rechenzentrum Rechenzentrum " +
		"Rechenzentrum Rechenzentrum Rechenzentrum Rechenzentrum Rechenzentrum " +
		"Rechenzentrum Rechenzentrum Rechenzentrum Rechenzentrum Rechenzentrum " +
		"Rechenzentrum Rechenzentrum Rechenzentrum Rechenzentrum Rechenzentrum ist heute erreichbar."
	insertChunk(repeated, false)
	insertChunk(repeated, false)
	insertChunk(repeated, false)

	refresher := NewBM25StatsRefresher(vectorPool, mainPool)
	if err := refresher.RefreshKB(ctx, kbID, 768); err != nil {
		t.Fatalf("RefreshKB: %v", err)
	}

	pgConfig := PgTextSearchConfig("de")
	baseInput := keywordSQLInput{
		TableName: "document_chunks_768",
		Query:     "Stud.IP Rechenzentrum",
		KbID:      kbID.String(),
		PgConfig:  pgConfig,
		Limit:     10,
	}

	run := func(mode KeywordScoringMode) []rawRow {
		t.Helper()
		in := baseInput
		in.Mode = mode
		if mode == KeywordScoringBM25 {
			in.Dim = 768
			in.K1, in.B = 1.2, 0.75
		}
		sql, args, ok := buildKeywordSQL(in)
		if !ok {
			t.Fatalf("buildKeywordSQL(%s) returned ok=false", mode)
		}
		rows, err := vectorPool.Query(ctx, sql, args...)
		if err != nil {
			t.Fatalf("query (%s): %v", mode, err)
		}
		results, err := collectRawRows(rows, 10)
		if err != nil {
			t.Fatalf("collectRawRows (%s): %v", mode, err)
		}
		return results
	}

	tsRankRows := run(KeywordScoringTsRank)
	bm25Rows := run(KeywordScoringBM25)

	// Candidate set identity: same chunk IDs regardless of scoring mode
	// (only the score formula differs). All four chunks match the
	// OR-of-tokens recall floor ("rechenzentrum" alone), so both modes
	// must return all four.
	idSet := func(rows []rawRow) map[string]bool {
		m := make(map[string]bool, len(rows))
		for _, r := range rows {
			m[r.ID] = true
		}
		return m
	}
	tsIDs, bmIDs := idSet(tsRankRows), idSet(bm25Rows)
	if len(tsIDs) != 4 || len(bmIDs) != 4 {
		t.Fatalf("expected 4 candidates in both modes, got ts_rank=%d bm25=%d", len(tsIDs), len(bmIDs))
	}
	for id := range tsIDs {
		if !bmIDs[id] {
			t.Errorf("candidate set mismatch: chunk %s present in ts_rank but not bm25", id)
		}
	}
	for id := range bmIDs {
		if !tsIDs[id] {
			t.Errorf("candidate set mismatch: chunk %s present in bm25 but not ts_rank", id)
		}
	}

	// BM25 must rank the Stud.IP chunk strictly above every other chunk —
	// this is the IDF effect: "Stud.IP" (df=1/4) far outweighs
	// "Rechenzentrum"'s (df=4/4) near-zero discriminative power, even
	// though the other three chunks have vastly higher raw term
	// frequency for "Rechenzentrum".
	var targetScore float64
	var found bool
	maxOtherScore := -1.0
	for _, r := range bm25Rows {
		if r.ID == targetChunkID.String() {
			targetScore = r.Score
			found = true
			continue
		}
		if r.Score > maxOtherScore {
			maxOtherScore = r.Score
		}
	}
	if !found {
		t.Fatalf("target chunk %s not present in bm25 results: %+v", targetChunkID, bm25Rows)
	}
	if targetScore <= maxOtherScore {
		t.Errorf("bm25: target (Stud.IP) chunk score %v is not strictly greater than the best of the other three %v", targetScore, maxOtherScore)
	}
}
