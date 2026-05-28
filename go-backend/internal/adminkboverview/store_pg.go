package adminkboverview

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/pgxutil"
)

// PGStore is the Postgres-backed Store.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewStore creates a PGStore over the main pool.
func NewStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

// Compile-time interface assertion.
var _ Store = (*PGStore)(nil)

// ListKBs returns one base row per knowledge base. owner_name is derived from
// first/last name, falling back to username; NULL for KBs with no user_id.
func (s *PGStore) ListKBs(ctx context.Context) ([]KBBase, error) {
	const sql = `
		SELECT kb.id::text                                                         AS id,
		       kb.name                                                             AS name,
		       COALESCE(NULLIF(TRIM(CONCAT(u.first_name, ' ', u.last_name)), ''), u.username) AS owner_name,
		       kb.is_global                                                        AS is_global,
		       kb.is_published                                                     AS is_published,
		       to_char(kb.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"') AS created_at
		FROM knowledge_bases kb
		LEFT JOIN users u ON u.id = kb.user_id
		ORDER BY kb.name`
	return pgxutil.QueryRows[KBBase](ctx, s.pool, sql)
}

// fileStatRow scans the file aggregate.
type fileStatRow struct {
	KbID                string  `db:"kb_id"`
	FileCount           int     `db:"file_count"`
	TotalSizeBytes      int64   `db:"total_size_bytes"`
	FailedFileCount     int     `db:"failed_file_count"`
	ProcessingFileCount int     `db:"processing_file_count"`
	LastFileUploadAt    *string `db:"last_file_upload_at"`
}

// FileStatsByKB returns per-KB file aggregates keyed by kb_id (text).
func (s *PGStore) FileStatsByKB(ctx context.Context) (map[string]FileStats, error) {
	const sql = `
		SELECT kb_id::text                                                          AS kb_id,
		       COUNT(*)::int                                                        AS file_count,
		       COALESCE(SUM(size), 0)::bigint                                       AS total_size_bytes,
		       COUNT(*) FILTER (WHERE status IN ('error','partial'))::int          AS failed_file_count,
		       COUNT(*) FILTER (WHERE status IN ('pending','processing'))::int      AS processing_file_count,
		       to_char(MAX(created_at) AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"') AS last_file_upload_at
		FROM files
		GROUP BY kb_id`
	rows, err := pgxutil.QueryRows[fileStatRow](ctx, s.pool, sql)
	if err != nil {
		return nil, err
	}
	out := make(map[string]FileStats, len(rows))
	for _, r := range rows {
		out[r.KbID] = FileStats{
			FileCount:           r.FileCount,
			TotalSizeBytes:      r.TotalSizeBytes,
			FailedFileCount:     r.FailedFileCount,
			ProcessingFileCount: r.ProcessingFileCount,
			LastFileUploadAt:    r.LastFileUploadAt,
		}
	}
	return out, nil
}

// msgStatRow scans the message/chat aggregate.
type msgStatRow struct {
	KbID          string  `db:"kb_id"`
	MessageCount  int     `db:"message_count"`
	ChatCount     int     `db:"chat_count"`
	LastMessageAt *string `db:"last_message_at"`
}

// MessageStatsByKB returns per-KB message + chat aggregates keyed by kb_id (text).
func (s *PGStore) MessageStatsByKB(ctx context.Context) (map[string]MessageStats, error) {
	const sql = `
		SELECT c.kb_id::text                                                        AS kb_id,
		       COUNT(m.id)::int                                                     AS message_count,
		       COUNT(DISTINCT m.chat_id)::int                                       AS chat_count,
		       to_char(MAX(m.created_at) AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"') AS last_message_at
		FROM messages m
		JOIN chats c ON c.id = m.chat_id
		GROUP BY c.kb_id`
	rows, err := pgxutil.QueryRows[msgStatRow](ctx, s.pool, sql)
	if err != nil {
		return nil, err
	}
	out := make(map[string]MessageStats, len(rows))
	for _, r := range rows {
		out[r.KbID] = MessageStats{
			MessageCount:  r.MessageCount,
			ChatCount:     r.ChatCount,
			LastMessageAt: r.LastMessageAt,
		}
	}
	return out, nil
}
