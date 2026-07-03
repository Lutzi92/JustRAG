package vector

import "fmt"

// conditionalRedimEmbeddingSQL returns a DO block that re-dimensions the
// "embedding" column of tableName to vector(dims) ONLY when the column's
// current type differs.
//
// Why not a bare ALTER: `ALTER TABLE … SET DATA TYPE vector(N)` is not
// reliably a no-op when the column already has that exact type — observed
// 2026-07-03 in production (pg18 + pgvector), the same-typmod ALTER rewrote
// a 155k-row table including its halfvec HNSW index, under ACCESS EXCLUSIVE,
// on every worker startup: live search queries queued behind the lock and
// the k8s liveness probe killed workers mid-rewrite into a restart loop.
// The catalog guard keeps the steady-state boot path lock-free; the ALTER
// only runs on genuine dimension changes (fresh bootstrap is a match, since
// CREATE TABLE already declares vector(dims)).
//
// SQL injection invariant: tableName comes from GetVectorTableName(int) or a
// package-level constant; dims is an int. No user input reaches this string.
func conditionalRedimEmbeddingSQL(tableName string, dims int) string {
	return fmt.Sprintf(`DO $$
BEGIN
	IF EXISTS (
		SELECT 1 FROM pg_attribute
		 WHERE attrelid = '"%[1]s"'::regclass
		   AND attname = 'embedding'
		   AND NOT attisdropped
		   AND format_type(atttypid, atttypmod) <> 'vector(%[2]d)'
	) THEN
		EXECUTE 'ALTER TABLE "%[1]s" ALTER COLUMN "embedding" SET DATA TYPE vector(%[2]d)';
	END IF;
END $$`, tableName, dims)
}
