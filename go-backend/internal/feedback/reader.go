package feedback

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Reader aggregates per-chunk feedback from the main DB.
type Reader struct {
	pool *pgxpool.Pool
}

// NewReader builds a Reader over the main Postgres pool.
func NewReader(pool *pgxpool.Pool) *Reader { return &Reader{pool: pool} }

// netSignalsSQL sums current message feedback per chunk for a candidate set.
// +1 per positive, -1 per negative, 0 otherwise. Uses messages.feedback (the
// denormalized current truth) so toggles are reflected without delta tracking.
const netSignalsSQL = `
SELECT mc.chunk_id::text,
       SUM(CASE m.feedback
             WHEN 'positive' THEN 1
             WHEN 'negative' THEN -1
             ELSE 0 END)::int AS net
FROM message_chunks mc
JOIN messages m ON m.id = mc.message_id
WHERE mc.kb_id = $1::uuid
  AND mc.chunk_id = ANY($2::uuid[])
GROUP BY mc.chunk_id
HAVING SUM(CASE m.feedback WHEN 'positive' THEN 1 WHEN 'negative' THEN -1 ELSE 0 END) <> 0`

// NetSignals returns chunkID -> net signal for the given candidates. Chunks
// with zero net are omitted. Empty input returns an empty map (no query).
func (r *Reader) NetSignals(ctx context.Context, kbID string, chunkIDs []string) (map[string]int, error) {
	out := make(map[string]int)
	if r == nil || r.pool == nil || len(chunkIDs) == 0 || kbID == "" {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, netSignalsSQL, kbID, chunkIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var net int
		if err := rows.Scan(&id, &net); err != nil {
			return nil, err
		}
		out[id] = net
	}
	return out, rows.Err()
}

// NegativeChunk is one row of the admin review list.
type NegativeChunk struct {
	ChunkID     string `json:"chunk_id"`
	KbID        string `json:"kb_id"`
	NetSignal   int    `json:"net_signal"`
	SignalCount int    `json:"signal_count"`
}

const topNegativeSQL = `
SELECT mc.chunk_id::text, mc.kb_id::text,
       SUM(CASE m.feedback WHEN 'positive' THEN 1 WHEN 'negative' THEN -1 ELSE 0 END)::int AS net,
       COUNT(*) FILTER (WHERE m.feedback IS NOT NULL)::int AS cnt
FROM message_chunks mc
JOIN messages m ON m.id = mc.message_id
WHERE ($1::uuid IS NULL OR mc.kb_id = $1::uuid)
GROUP BY mc.chunk_id, mc.kb_id
HAVING SUM(CASE m.feedback WHEN 'positive' THEN 1 WHEN 'negative' THEN -1 ELSE 0 END) < 0
ORDER BY net ASC
LIMIT $2`

// TopNegative returns the most net-negative chunks, optionally scoped to a KB
// (pass "" for all KBs).
func (r *Reader) TopNegative(ctx context.Context, kbID string, limit int) ([]NegativeChunk, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	var kbArg any = nil
	if kbID != "" {
		kbArg = kbID
	}
	rows, err := r.pool.Query(ctx, topNegativeSQL, kbArg, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NegativeChunk
	for rows.Next() {
		var nc NegativeChunk
		if err := rows.Scan(&nc.ChunkID, &nc.KbID, &nc.NetSignal, &nc.SignalCount); err != nil {
			return nil, err
		}
		out = append(out, nc)
	}
	return out, rows.Err()
}
