package vector

import (
	"cmp"
	"context"
	"math"
	"slices"
	"strconv"
	"time"

	"github.com/justrag/go-backend/internal/logctx"
)

// ApplyRecencyBoost adds a small, bounded score adjustment to each doc based
// on the age of its source file: weight * 2^(-ageDays/halfLifeDays), i.e.
// exponential decay with the configured half-life. A brand-new file gets the
// full weight, a file one half-life old gets weight/2, old corpus content
// converges to zero adjustment — similarity ⊕ recency fusion in the spirit
// of arXiv 2509.19376.
//
// fileTimes maps FileID → file creation time; docs whose FileID is absent
// keep their score (fail-open, mirrors ApplyFeedbackBoost). Future-dated
// timestamps (DB/app clock skew) clamp to age 0 so the adjustment never
// exceeds weight. Callers must re-sort by Score afterward. A non-positive
// weight or halfLifeDays is a no-op.
func ApplyRecencyBoost(docs []RankedDoc, fileTimes map[string]time.Time, now time.Time, halfLifeDays, weight float64) {
	if weight <= 0 || halfLifeDays <= 0 || len(fileTimes) == 0 {
		return
	}
	for i := range docs {
		ts, ok := fileTimes[docs[i].FileID]
		if !ok || ts.IsZero() {
			continue
		}
		ageDays := now.Sub(ts).Hours() / 24
		if ageDays < 0 {
			ageDays = 0
		}
		docs[i].Score += weight * math.Exp2(-ageDays/halfLifeDays)
	}
}

// applyRecencyPrior is stage 10c of Search: re-score fused by the
// exponential-decay recency prior and re-sort by the boosted score.
// Returns the number of file timestamps applied, 0 when the gate is off,
// no timestamps were found, or the files-table read failed — fail-open,
// a read error never fails the search.
func (s *SearchService) applyRecencyPrior(ctx context.Context, kbID string, fused []RankedDoc, siteCfg KBVectorConfig) int {
	if !siteCfg.RecencyBoostEnabled || len(fused) == 0 {
		return 0
	}
	fileIDs := make([]string, 0, len(fused))
	seen := make(map[string]struct{}, len(fused))
	for i := range fused {
		if _, ok := seen[fused[i].FileID]; ok || fused[i].FileID == "" {
			continue
		}
		seen[fused[i].FileID] = struct{}{}
		fileIDs = append(fileIDs, fused[i].FileID)
	}
	times, err := s.fileCreatedTimes(ctx, fileIDs)
	if err != nil {
		logctx.From(ctx).Warn("recency boost: file times", "error", err, "kb_id", kbID)
		return 0
	}
	if len(times) == 0 {
		return 0
	}
	ApplyRecencyBoost(fused, times, time.Now(), siteCfg.RecencyHalfLifeDays, siteCfg.RecencyBoostWeight)
	slices.SortStableFunc(fused, func(a, b RankedDoc) int { return cmp.Compare(b.Score, a.Score) })
	return len(times)
}

// fileCreatedTimes fetches created_at for the given file IDs from the main
// DB in one query. Used by the recency boost (stage 10c); fail-open — the
// caller treats an error as "no recency signal".
func (s *SearchService) fileCreatedTimes(ctx context.Context, fileIDs []string) (map[string]time.Time, error) {
	if len(fileIDs) == 0 || s.mainDB == nil {
		return nil, nil
	}
	rows, err := s.mainDB.Query(ctx,
		`SELECT id::text, created_at FROM files WHERE id::text = ANY($1)`, fileIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]time.Time, len(fileIDs))
	for rows.Next() {
		var id string
		var ts time.Time
		if err := rows.Scan(&id, &ts); err != nil {
			return nil, err
		}
		out[id] = ts
	}
	return out, rows.Err()
}

// effectiveDateExpr is the SQL expression for a file's effective date used
// by every date-window query. Phase 1: ingest time. Phase 2 (when a
// published_at column lands) becomes COALESCE(published_at, created_at).
const effectiveDateExpr = "created_at"

// fileIDsInDateRange returns the IDs of files in kbID whose effective date
// falls within [after, before] (either bound may be nil = unbounded).
// Queries the main DB (files lives there, not in the vector pool).
func (s *SearchService) fileIDsInDateRange(ctx context.Context, kbID string, after, before *time.Time) ([]string, error) {
	if s.mainDB == nil {
		// Unlike the recency boost (which merely skips a score adjustment on a
		// nil mainDB), a nil mainDB here empties the date-window file set and
		// short-circuits Search to zero results — indistinguishable from "no
		// documents in that window". Log it so a misconfiguration is not
		// mistaken for an empty window. Fail-open keeps the existing contract.
		logctx.From(ctx).Warn("date-window filter: main DB unavailable, returning no matches", "kb_id", kbID)
		return nil, nil
	}
	sql := `SELECT id::text FROM files WHERE kb_id = $1::uuid`
	args := []any{kbID}
	if after != nil {
		args = append(args, *after)
		sql += " AND " + effectiveDateExpr + " >= $" + strconv.Itoa(len(args))
	}
	if before != nil {
		args = append(args, *before)
		sql += " AND " + effectiveDateExpr + " <= $" + strconv.Itoa(len(args))
	}
	rows, err := s.mainDB.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// intersectFileIDs returns the set intersection of a and b, preserving a's
// order. When a is empty ("no caller restriction") the whole of b is
// returned. Used to combine a caller-supplied FileIDs allow-list with the
// date-window-resolved IDs.
func intersectFileIDs(a, b []string) []string {
	if len(a) == 0 {
		return b
	}
	inB := make(map[string]struct{}, len(b))
	for _, id := range b {
		inB[id] = struct{}{}
	}
	out := make([]string, 0, len(a))
	for _, id := range a {
		if _, ok := inB[id]; ok {
			out = append(out, id)
		}
	}
	return out
}
