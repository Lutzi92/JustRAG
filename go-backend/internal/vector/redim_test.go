package vector

import (
	"strings"
	"testing"
)

// The unconditional `ALTER TABLE … SET DATA TYPE vector(N)` in the ensure
// paths is NOT reliably a no-op when the column already matches: observed
// 2026-07-03 in prod (pg18 + pgvector), a same-typmod ALTER rewrote a
// 155k-row table including a halfvec HNSW rebuild under ACCESS EXCLUSIVE on
// EVERY worker startup — blocking live search and getting liveness-killed
// into a restart loop. The redim SQL must therefore guard the ALTER behind a
// catalog check so the steady-state startup path never takes the exclusive
// lock.
func TestConditionalRedimSQL_GuardsAlterBehindTypeCheck(t *testing.T) {
	sql := conditionalRedimEmbeddingSQL("document_chunks_2048", 2048)

	for _, want := range []string{
		"pg_attribute",               // catalog check, not blind DDL
		"format_type",                // compares the full rendered type
		"'vector(2048)'",             // target type literal
		"attname = 'embedding'",      // right column
		"document_chunks_2048",       // right table
		"ALTER TABLE",                // the guarded statement
		"SET DATA TYPE vector(2048)", // right target
		"NOT attisdropped",           // ignore dropped columns
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("redim SQL missing %q:\n%s", want, sql)
		}
	}

	// The ALTER must be inside the conditional branch (EXECUTEd by the DO
	// block), not a bare top-level statement.
	if !strings.Contains(sql, "DO $$") || !strings.Contains(sql, "IF EXISTS") {
		t.Errorf("redim SQL must wrap the ALTER in a DO block with an IF EXISTS guard:\n%s", sql)
	}
}

func TestConditionalRedimSQL_OtherDims(t *testing.T) {
	sql := conditionalRedimEmbeddingSQL("chunk_hype_questions", 1536)
	if !strings.Contains(sql, "'vector(1536)'") || !strings.Contains(sql, "chunk_hype_questions") {
		t.Errorf("redim SQL not parameterised correctly:\n%s", sql)
	}
}
