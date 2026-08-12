package adminkboverview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/kbmembers"
	"github.com/justrag/go-backend/internal/pgxutil"
)

// ErrKBNotFound is returned by TransferKBOwner when the target KB no longer
// exists at UPDATE time (e.g. deleted concurrently between the handler's
// lookup and the transfer transaction). Callers should map it to 404 and
// must not write an audit row for the no-op.
var ErrKBNotFound = errors.New("knowledge base not found")

// PGStore is the Postgres-backed Store.
type PGStore struct {
	pool    *pgxpool.Pool
	members kbmembers.Store
}

// NewStore creates a PGStore over the main pool.
func NewStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool, members: kbmembers.NewStore(pool)}
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
		       kb.user_id::text                                                    AS owner_id,
		       u.username                                                          AS owner_username,
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

// ---------------------------------------------------------------------------
// ActionStore — mutating admin actions
// ---------------------------------------------------------------------------

// Compile-time interface assertion.
var _ ActionStore = (*PGStore)(nil)

// GetKBMeta returns the KB's dispatch metadata, or (nil, nil) when no KB has
// that id. kbID must already be a valid UUID; handlers validate before calling.
func (s *PGStore) GetKBMeta(ctx context.Context, kbID string) (*KBMeta, error) {
	const sql = `
		SELECT id::text      AS id,
		       name          AS name,
		       is_global     AS is_global,
		       user_id::text AS owner_id
		FROM knowledge_bases
		WHERE id = $1::uuid`
	return pgxutil.QueryOne[KBMeta](ctx, s.pool, sql, kbID)
}

// GetOwnerInfo returns the identity of a prospective owner, or (nil, nil) when
// no user has that id. display_name mirrors ListKBs' owner_name expression.
func (s *PGStore) GetOwnerInfo(ctx context.Context, userID string) (*OwnerInfo, error) {
	const sql = `
		SELECT id::text AS id,
		       username AS username,
		       COALESCE(NULLIF(TRIM(CONCAT(first_name, ' ', last_name)), ''), username) AS display_name
		FROM users
		WHERE id = $1::uuid`
	return pgxutil.QueryOne[OwnerInfo](ctx, s.pool, sql, userID)
}

// TransferKBOwner reassigns a personal KB to newOwnerID by delegating to
// kbmembers.Store.TransferOwner (Task 5) — the single transactional
// implementation of an ownership swap, shared with the member-management
// transfer endpoint (internal/kbmembers/http.go's TransferOwner). Before
// Task 7 this method wrote knowledge_bases.user_id and
// knowledge_base_shares directly and never touched kb_members at all, so a
// superadmin transfer here silently left the real authority table
// (kb_members, which internal/kbaccess actually reads) unchanged — the old
// owner kept access and the new owner got none. That bug is fixed by routing
// through the shared implementation instead of a second, divergent one.
//
// prevOwnerID is accepted only for signature compatibility with the HTTP
// handler (internal/adminkboverview/actions.go): kbmembers.TransferOwner
// determines the current owner itself (whichever kb_members row carries
// role='owner'), not from a caller-supplied hint, so the parameter is unused
// here.
//
// Behaviour change from the pre-Task-7 implementation: the previous owner
// used to be kept as an 'edit' row in knowledge_base_shares. Now they are
// demoted to 'admin' in kb_members, matching the four-role rights matrix (an
// ex-owner keeps the "co-manages this KB" tier, not merely edit access).
// knowledge_base_shares is no longer written by this path at all.
func (s *PGStore) TransferKBOwner(ctx context.Context, kbID, newOwnerID string, prevOwnerID *string) error {
	err := s.members.TransferOwner(ctx, kbID, newOwnerID)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, kbmembers.ErrNotFound):
		return ErrKBNotFound
	case isMissingKBForeignKey(err):
		return ErrKBNotFound
	default:
		return fmt.Errorf("TransferKBOwner: %w", err)
	}
}

// isMissingKBForeignKey reports whether err is the kb_members_kb_id_fkey
// violation TransferOwner's INSERT raises when kbID no longer references a
// row in knowledge_bases. This is how "the KB was deleted between the
// handler's lookup and this call" actually surfaces on this path:
// TransferOwner's INSERT ... ON CONFLICT DO UPDATE has no WHERE clause, so a
// successful insert-or-update always reports RowsAffected() > 0 — the
// RowsAffected()==0 branch behind kbmembers.ErrNotFound is unreachable for a
// missing KB specifically, and the foreign-key violation is the real signal.
func isMissingKBForeignKey(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503" && pgErr.ConstraintName == "kb_members_kb_id_fkey"
}

// LogAuditAction records an admin action in admin_audit_logs. Mirrors
// adminglobalkbs.PGStore.LogAuditAction.
func (s *PGStore) LogAuditAction(ctx context.Context, operatorID, action, targetType, targetID string, diff any) error {
	var diffJSON []byte
	if diff != nil {
		var err error
		diffJSON, err = json.Marshal(diff)
		if err != nil {
			return fmt.Errorf("LogAuditAction marshal diff: %w", err)
		}
	}

	const sql = `
		INSERT INTO admin_audit_logs (operator_id, action, target_type, target_id, diff)
		VALUES ($1, $2, $3, $4, $5)`

	_, err := s.pool.Exec(ctx, sql, operatorID, action, targetType, targetID, diffJSON)
	return err
}
