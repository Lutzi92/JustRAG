package vector

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/pgxutil"
)

// bm25Lang and bm25Simple are the two BM25 scoring arms this package
// tracks statistics for, mirroring the two tsvector columns every chunk
// table carries (vector_index = language-config tsvector, vector_index_simple
// = the 'simple' config tsvector used as a stem-free fallback arm). Kept as
// named constants so Task 6's query-time reader and this refresher can't
// drift on the literal spelling.
const (
	bm25ArmLang            = "lang"
	bm25ArmSimple          = "simple"
	bm25StatsCacheTTL      = 60 * time.Second
	defaultBM25StatsMaxAge = 24 * time.Hour
)

// GetBM25KBStatsTableName returns the dim-keyed BM25 per-KB stats table
// name, mirroring GetVectorTableName / GetHyPETableName: bare for the 1536
// default, suffixed otherwise. Holds one row per (kb_id, arm) with the
// corpus doc_count and avg_len BM25 needs.
func GetBM25KBStatsTableName(dimensions int) string {
	if dimensions == 1536 {
		return "bm25_kb_stats"
	}
	return fmt.Sprintf("bm25_kb_stats_%d", dimensions)
}

// GetBM25TermStatsTableName returns the dim-keyed BM25 per-term stats table
// name, same suffixing convention. Holds one row per (kb_id, arm, lexeme)
// document-frequency count, refreshed from ts_stat().
func GetBM25TermStatsTableName(dimensions int) string {
	if dimensions == 1536 {
		return "bm25_term_stats"
	}
	return fmt.Sprintf("bm25_term_stats_%d", dimensions)
}

// EnsureBM25StatsTables creates the dim-keyed BM25 stats tables if absent.
// Same advisory-lock + transaction discipline as EnsureHyPETable (see
// hype.go) so concurrent worker replicas booting at once don't race on
// CREATE TABLE IF NOT EXISTS.
func EnsureBM25StatsTables(ctx context.Context, exec ChunkTableExec, dimensions int) error {
	kbTable := GetBM25KBStatsTableName(dimensions)
	termTable := GetBM25TermStatsTableName(dimensions)
	slog.Info("ensuring BM25 stats tables", "kb_table", kbTable, "term_table", termTable, "dimensions", dimensions)

	tx, err := exec.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx for %s: %w", kbTable, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := tx.Exec(ctx, `SET LOCAL statement_timeout = 0`); err != nil {
		return fmt.Errorf("relax statement_timeout for %s: %w", kbTable, err)
	}
	lockSQL := fmt.Sprintf(
		`SELECT pg_advisory_xact_lock(hashtext('justrag.ensure_bm25_stats_tables:%s')::bigint)`,
		kbTable,
	)
	if err := tx.Exec(ctx, lockSQL); err != nil {
		return fmt.Errorf("acquire advisory lock for %s: %w", kbTable, err)
	}

	kbSQL := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS "%s" (
			kb_id         uuid NOT NULL,
			arm           text NOT NULL CHECK (arm IN ('lang','simple')),
			doc_count     bigint NOT NULL,
			avg_len       double precision NOT NULL,
			refreshed_at  timestamptz NOT NULL DEFAULT now(),
			PRIMARY KEY (kb_id, arm)
		)`, kbTable)
	if err := tx.Exec(ctx, kbSQL); err != nil {
		return fmt.Errorf("create table %s: %w", kbTable, err)
	}

	termSQL := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS "%s" (
			kb_id     uuid NOT NULL,
			arm       text NOT NULL,
			lexeme    text NOT NULL,
			doc_count integer NOT NULL,
			PRIMARY KEY (kb_id, arm, lexeme)
		)`, termTable)
	if err := tx.Exec(ctx, termSQL); err != nil {
		return fmt.Errorf("create table %s: %w", termTable, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit ensure bm25 stats tables %s/%s: %w", kbTable, termTable, err)
	}
	slog.Info("BM25 stats tables ready", "kb_table", kbTable, "term_table", termTable)
	return nil
}

// bm25ArmColumn returns the tsvector column backing arm.
func bm25ArmColumn(arm string) string {
	if arm == bm25ArmSimple {
		return "vector_index_simple"
	}
	return "vector_index"
}

// buildTermStatsRefreshSQL returns the ts_stat(...) SQL fragment that
// yields (word, ndoc, nentry) rows for the given KB, dim table, and arm.
// kbID is a typed uuid.UUID (never caller-supplied text) and dim selects
// the table name via GetVectorTableName, so the interpolated literal is
// safe to embed directly in the surrounding INSERT ... SELECT.
func buildTermStatsRefreshSQL(kbID uuid.UUID, dim int, arm string) string {
	table := GetVectorTableName(dim)
	col := bm25ArmColumn(arm)
	return fmt.Sprintf(`ts_stat($$SELECT %s FROM "%s" WHERE kb_id = '%s'$$)`, col, table, kbID.String())
}

// ---------------------------------------------------------------------------
// BM25StatsRefresher — vector+main pool I/O for the BM25 stats tables
// ---------------------------------------------------------------------------

// BM25StatsRefresher recomputes BM25 corpus statistics (per-KB doc
// count/avg length, per-term document frequency) from a chunk table's
// tsvector columns via ts_stat(). vectorDB is where both the chunk tables
// and the stats tables live; mainDB backs the "is this KB still ingesting"
// check StaleKBs uses to avoid refreshing a moving target.
type BM25StatsRefresher struct {
	vectorDB *pgxpool.Pool
	mainDB   *pgxpool.Pool

	// StaleMaxAge overrides the default staleness threshold (W2-R5's 24h)
	// used by Sweep when calling StaleKBs. Zero uses defaultBM25StatsMaxAge.
	StaleMaxAge time.Duration
}

// NewBM25StatsRefresher creates a BM25StatsRefresher backed by the given
// vector and main connection pools.
func NewBM25StatsRefresher(vectorDB, mainDB *pgxpool.Pool) *BM25StatsRefresher {
	return &BM25StatsRefresher{vectorDB: vectorDB, mainDB: mainDB}
}

// RefreshKB recomputes and persists both arms' BM25 stats for kbID/dim, one
// transaction per (kb, arm). Fail-soft is the caller's responsibility
// (Sweep logs and continues per KB); RefreshKB itself returns the first
// error encountered.
func (r *BM25StatsRefresher) RefreshKB(ctx context.Context, kbID uuid.UUID, dim int) error {
	if r == nil || r.vectorDB == nil {
		return fmt.Errorf("bm25 refresh: vector pool not configured")
	}
	kbTable := GetBM25KBStatsTableName(dim)
	termTable := GetBM25TermStatsTableName(dim)
	chunkTable := GetVectorTableName(dim)
	if !validVectorTable.MatchString(kbTable) || !validVectorTable.MatchString(termTable) || !validVectorTable.MatchString(chunkTable) {
		return fmt.Errorf("bm25 refresh: invalid table name for dim %d", dim)
	}

	for _, arm := range []string{bm25ArmLang, bm25ArmSimple} {
		start := time.Now()
		terms, err := r.refreshArm(ctx, kbID, dim, arm, kbTable, termTable, chunkTable)
		if err != nil {
			return fmt.Errorf("bm25 refresh kb=%s arm=%s dim=%d: %w", kbID, arm, dim, err)
		}
		slog.Info("bm25.stats.refreshed",
			"kb_id", kbID.String(), "arm", arm, "dim", dim,
			"terms", terms, "ms", time.Since(start).Milliseconds())
	}
	return nil
}

// refreshArm does the (kb, arm) refresh in one transaction and returns the
// number of term_stats rows written.
func (r *BM25StatsRefresher) refreshArm(ctx context.Context, kbID uuid.UUID, dim int, arm, kbTable, termTable, chunkTable string) (int, error) {
	col := bm25ArmColumn(arm)

	tx, err := r.vectorDB.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		fmt.Sprintf(`DELETE FROM "%s" WHERE kb_id = $1 AND arm = $2`, termTable),
		kbID, arm,
	); err != nil {
		return 0, fmt.Errorf("delete term stats: %w", err)
	}

	// Skip the term-stats insert when the arm's tsvector column is entirely
	// NULL for this KB (the 'simple' arm may never have been backfilled for
	// an older ingest) — write a zero-doc kb_stats row instead so the
	// query-time reader's "stats available?" check is deterministic rather
	// than reporting a non-zero doc_count with no matching term rows.
	var hasCol bool
	checkSQL := fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM "%s" WHERE kb_id = $1 AND %s IS NOT NULL)`, chunkTable, col)
	if err := tx.QueryRow(ctx, checkSQL, kbID).Scan(&hasCol); err != nil {
		return 0, fmt.Errorf("check %s populated: %w", col, err)
	}

	var terms int
	if hasCol {
		insertTermsSQL := fmt.Sprintf(
			`INSERT INTO "%s" (kb_id, arm, lexeme, doc_count)
			 SELECT $1, $2, word, ndoc FROM %s`,
			termTable, buildTermStatsRefreshSQL(kbID, dim, arm))
		tag, err := tx.Exec(ctx, insertTermsSQL, kbID, arm)
		if err != nil {
			return 0, fmt.Errorf("insert term stats: %w", err)
		}
		terms = int(tag.RowsAffected())
	}

	var kbSQL string
	var args []any
	if hasCol {
		kbSQL = fmt.Sprintf(`
			INSERT INTO "%s" (kb_id, arm, doc_count, avg_len, refreshed_at)
			SELECT $1, $2, count(*), COALESCE(avg(length(%s)), 0), now()
			FROM "%s" WHERE kb_id = $1
			ON CONFLICT (kb_id, arm) DO UPDATE SET
				doc_count = EXCLUDED.doc_count,
				avg_len = EXCLUDED.avg_len,
				refreshed_at = EXCLUDED.refreshed_at`,
			kbTable, col, chunkTable)
		args = []any{kbID, arm}
	} else {
		kbSQL = fmt.Sprintf(`
			INSERT INTO "%s" (kb_id, arm, doc_count, avg_len, refreshed_at)
			VALUES ($1, $2, 0, 0, now())
			ON CONFLICT (kb_id, arm) DO UPDATE SET
				doc_count = EXCLUDED.doc_count,
				avg_len = EXCLUDED.avg_len,
				refreshed_at = EXCLUDED.refreshed_at`,
			kbTable)
		args = []any{kbID, arm}
	}
	if _, err := tx.Exec(ctx, kbSQL, args...); err != nil {
		return 0, fmt.Errorf("upsert kb stats: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return terms, nil
}

// StaleKBs returns the KBs (for the given dim) whose BM25 stats need a
// refresh (W2-R5): no stats row yet, stats older than the newest chunk, or
// stats older than maxAge (catches pure deletions, which don't move
// max(created_at)). Excludes KBs with an active ingestion (any files row
// in pending/processing on the main DB) — refreshing mid-ingest would just
// be redone on the next sweep once ingestion settles.
func (r *BM25StatsRefresher) StaleKBs(ctx context.Context, dim int, maxAge time.Duration) ([]uuid.UUID, error) {
	if r == nil || r.vectorDB == nil {
		return nil, fmt.Errorf("bm25 stale kbs: vector pool not configured")
	}
	chunkTable := GetVectorTableName(dim)
	kbStatsTable := GetBM25KBStatsTableName(dim)
	if !validVectorTable.MatchString(chunkTable) || !validVectorTable.MatchString(kbStatsTable) {
		return nil, fmt.Errorf("bm25 stale kbs: invalid table name for dim %d", dim)
	}

	query := fmt.Sprintf(`
		SELECT c.kb_id
		FROM (SELECT kb_id, max(created_at) AS newest FROM "%s" GROUP BY kb_id) c
		LEFT JOIN "%s" s ON s.kb_id = c.kb_id AND s.arm = '%s'
		WHERE s.kb_id IS NULL
		   OR s.refreshed_at < c.newest
		   OR s.refreshed_at < now() - ($1 * interval '1 second')
	`, chunkTable, kbStatsTable, bm25ArmLang)

	rows, err := r.vectorDB.Query(ctx, query, maxAge.Seconds())
	if err != nil {
		return nil, fmt.Errorf("query stale kbs: %w", err)
	}
	var candidates []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan stale kb: %w", err)
		}
		candidates = append(candidates, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	if len(candidates) == 0 || r.mainDB == nil {
		return candidates, nil
	}

	ids := make([]string, len(candidates))
	for i, id := range candidates {
		ids[i] = id.String()
	}
	activeRows, err := r.mainDB.Query(ctx,
		`SELECT DISTINCT kb_id FROM files WHERE status IN ('pending','processing') AND kb_id = ANY($1::uuid[])`,
		ids,
	)
	if err != nil {
		return nil, fmt.Errorf("query active-ingestion kbs: %w", err)
	}
	defer activeRows.Close()
	active := make(map[uuid.UUID]bool, len(candidates))
	for activeRows.Next() {
		var id uuid.UUID
		if err := activeRows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan active kb: %w", err)
		}
		active[id] = true
	}
	if err := activeRows.Err(); err != nil {
		return nil, err
	}

	result := candidates[:0]
	for _, id := range candidates {
		if !active[id] {
			result = append(result, id)
		}
	}
	return result, nil
}

// Sweep refreshes up to limit stale KBs (W2-R5 staleness, StaleMaxAge
// threshold) across every dim reported by ListChunkTableDimensions.
// Per-KB errors are logged and skipped so one bad KB can't stall the
// sweep; the returned count is how many KBs were actually refreshed.
func (r *BM25StatsRefresher) Sweep(ctx context.Context, limit int) (refreshed int, err error) {
	if r == nil || r.vectorDB == nil {
		return 0, nil
	}
	maxAge := r.StaleMaxAge
	if maxAge <= 0 {
		maxAge = defaultBM25StatsMaxAge
	}

	dims, err := NewChunkService(r.vectorDB).ListChunkTableDimensions(ctx)
	if err != nil {
		return 0, fmt.Errorf("bm25 sweep: list dims: %w", err)
	}

	for _, dim := range dims {
		if limit > 0 && refreshed >= limit {
			break
		}
		stale, serr := r.StaleKBs(ctx, dim, maxAge)
		if serr != nil {
			slog.Error("bm25.stats.sweep_list_failed", "dim", dim, "error", serr)
			continue
		}
		for _, kbID := range stale {
			if limit > 0 && refreshed >= limit {
				break
			}
			if rerr := r.RefreshKB(ctx, kbID, dim); rerr != nil {
				slog.Error("bm25.stats.sweep_refresh_failed", "kb_id", kbID.String(), "dim", dim, "error", rerr)
				continue
			}
			refreshed++
		}
	}
	return refreshed, nil
}

// DeleteBM25StatsForKB removes every BM25 stats row (both tables, both
// arms) for kbID across every dim in dims. Best-effort like
// HyPEStore.DeleteByFileIDsAllDims: a missing stats table (relation does
// not exist — an operator hasn't run the boot-time DDL for that dim, or the
// dim was never used) is tolerated rather than failing the whole KB delete.
func DeleteBM25StatsForKB(ctx context.Context, vectorDB *pgxpool.Pool, kbID uuid.UUID, dims []int) error {
	if vectorDB == nil {
		return nil
	}
	for _, d := range dims {
		kbTable := GetBM25KBStatsTableName(d)
		termTable := GetBM25TermStatsTableName(d)
		if !validVectorTable.MatchString(kbTable) || !validVectorTable.MatchString(termTable) {
			continue
		}
		if _, err := vectorDB.Exec(ctx, fmt.Sprintf(`DELETE FROM "%s" WHERE kb_id = $1`, termTable), kbID); err != nil {
			if !pgxutil.IsUndefinedTable(err) {
				return fmt.Errorf("delete bm25 term stats from %s: %w", termTable, err)
			}
		}
		if _, err := vectorDB.Exec(ctx, fmt.Sprintf(`DELETE FROM "%s" WHERE kb_id = $1`, kbTable), kbID); err != nil {
			if !pgxutil.IsUndefinedTable(err) {
				return fmt.Errorf("delete bm25 kb stats from %s: %w", kbTable, err)
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// bm25StatsAvailable — query-time cache consumed by Task 6
// ---------------------------------------------------------------------------

// bm25AvailEntry is one cached bm25StatsAvailable() verdict.
type bm25AvailEntry struct {
	available bool
	checkedAt time.Time
}

// bm25StatsAvailable reports whether kbID has a usable (non-empty) BM25
// stats row for the 'lang' arm in dim's stats table, so Task 6's scorer can
// fall back to ts_rank when stats are missing (fail-soft, per the global
// constraints doc) instead of erroring or scoring against an empty corpus.
// Cached for bm25StatsCacheTTL (60s) per (kbID, dim) — the stats sweep runs
// on a many-minutes cadence, so a short TTL avoids a DB round-trip on every
// search while still picking up a fresh refresh promptly.
func (s *SearchService) bm25StatsAvailable(ctx context.Context, kbID string, dim int) bool {
	if s == nil || s.vectorDB == nil {
		return false
	}
	key := kbID + ":" + strconv.Itoa(dim)
	if v, ok := s.bm25AvailCache.Load(key); ok {
		if entry, ok := v.(bm25AvailEntry); ok && time.Since(entry.checkedAt) < bm25StatsCacheTTL {
			return entry.available
		}
	}
	available := s.checkBM25StatsAvailable(ctx, kbID, dim)
	s.bm25AvailCache.Store(key, bm25AvailEntry{available: available, checkedAt: time.Now()})
	return available
}

func (s *SearchService) checkBM25StatsAvailable(ctx context.Context, kbID string, dim int) bool {
	table := GetBM25KBStatsTableName(dim)
	if !validVectorTable.MatchString(table) {
		return false
	}
	query := fmt.Sprintf(
		`SELECT EXISTS(SELECT 1 FROM "%s" WHERE kb_id = $1::uuid AND arm = '%s' AND doc_count > 0)`,
		table, bm25ArmLang)
	var exists bool
	if err := s.vectorDB.QueryRow(ctx, query, kbID).Scan(&exists); err != nil {
		if !pgxutil.IsUndefinedTable(err) {
			slog.Warn("bm25StatsAvailable check failed", "kb_id", kbID, "dim", dim, "error", err)
		}
		return false
	}
	return exists
}
