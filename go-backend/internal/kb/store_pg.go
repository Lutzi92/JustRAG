package kb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/kbaccess"
	"github.com/justrag/go-backend/internal/pgxutil"
	"github.com/justrag/go-backend/internal/store"
)

// PGStore is a PostgreSQL-backed implementation of kb.Store, kb.UpdateStore,
// and kb.ShareStore.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewStore creates a new PGStore backed by pool.
func NewStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

// Compile-time interface assertions.
var (
	_ Store       = (*PGStore)(nil)
	_ UpdateStore = (*PGStore)(nil)
	_ ShareStore  = (*PGStore)(nil)
)

// ---------------------------------------------------------------------------
// Internal scan structs
// ---------------------------------------------------------------------------

// kbFullRow is an internal struct with db tags for scanning full knowledge_bases rows.
type kbFullRow struct {
	ID             string          `db:"id"`
	Name           string          `db:"name"`
	UserID         *string         `db:"user_id"`
	Description    *string         `db:"description"`
	IsGlobal       bool            `db:"is_global"`
	IsPublished    bool            `db:"is_published"`
	Language       string          `db:"language"`
	AIConfigID     *string         `db:"ai_config_id"`
	ChatModel      *string         `db:"chat_model"`
	EmbeddingModel *string         `db:"embedding_model"`
	RerankModel    *string         `db:"rerank_model"`
	TTSModel       *string         `db:"tts_model"`
	SttModel       *string         `db:"stt_model"`
	SystemPrompt   *string         `db:"system_prompt"`
	HeaderText     *string         `db:"header_text"`
	ExamplePrompts *string         `db:"example_prompts"`
	StudioConfig   json.RawMessage `db:"studio_config"`
	ChunkSize      *int            `db:"chunk_size"`
	ChunkOverlap   *int            `db:"chunk_overlap"`
	CreatedAt      time.Time       `db:"created_at"`
	// Optional owner attribution — only populated by queries that LEFT JOIN
	// the users table (e.g. ListKnowledgeBases). Other queries leave these nil.
	OwnerFirstName *string `db:"owner_first_name"`
	OwnerLastName  *string `db:"owner_last_name"`
	OwnerUsername  *string `db:"owner_username"`
}

const kbSelectCols = `kb.id, kb.name, kb.user_id, kb.description, kb.is_global, kb.is_published,
       kb.language, kb.ai_config_id, kb.chat_model, kb.embedding_model, kb.rerank_model, kb.tts_model, kb.stt_model,
       kb.system_prompt, kb.header_text, kb.example_prompts, kb.studio_config,
       kb.chunk_size, kb.chunk_overlap, kb.created_at`

// kbStatsCols / kbStatsJoins add per-KB card metadata (file + message counts,
// last activity) for the list endpoints. The aggregates run as correlated
// LATERAL subqueries evaluated once per returned row (≤ limit), so no N+1 and
// no full-table GROUP BY. Mirrors what /api/admin/kb-overview computes. The
// outer query must alias knowledge_bases as `kb`.
const kbStatsCols = `,
       COALESCE(fs.file_count, 0)::int            AS file_count,
       COALESCE(fs.failed_file_count, 0)::int     AS failed_file_count,
       COALESCE(fs.processing_file_count, 0)::int AS processing_file_count,
       COALESCE(ms.message_count, 0)::int         AS message_count,
       ms.last_message_at                         AS last_message_at`

const kbStatsJoins = `
       LEFT JOIN LATERAL (
           SELECT COUNT(*)                                              AS file_count,
                  COUNT(*) FILTER (WHERE status IN ('error','partial'))      AS failed_file_count,
                  COUNT(*) FILTER (WHERE status IN ('pending','processing')) AS processing_file_count
           FROM files f WHERE f.kb_id = kb.id
       ) fs ON true
       LEFT JOIN LATERAL (
           SELECT COUNT(m.id) AS message_count, MAX(m.created_at) AS last_message_at
           FROM messages m JOIN chats c ON c.id = m.chat_id WHERE c.kb_id = kb.id
       ) ms ON true`

const kbSelectColsNoAlias = `id, name, user_id, description, is_global, is_published,
       language, ai_config_id, chat_model, embedding_model, rerank_model, tts_model, stt_model,
       system_prompt, header_text, example_prompts, studio_config, chunk_size, chunk_overlap, created_at,
       NULL::text AS owner_first_name, NULL::text AS owner_last_name, NULL::text AS owner_username`

// kbMembershipCols surfaces the caller's own role (my_role) and the KB's
// total member count (member_count) for list endpoints, so the frontend can
// decide delete-vs-leave and render a "shared with N" badge without an extra
// per-KB round trip. Kept separate from kbSelectCols/kbSelectColsNoAlias
// (rather than baked in, the way those constants are) because
// Create/UpdateKnowledgeBase RETURNING clauses have no "the caller" bind
// parameter and no `kb`-aliased row to correlate against; only the two list
// queries below use this. userIDParam is the caller-supplied $N placeholder,
// which differs between ListKnowledgeBases and ListGlobalKnowledgeBases.
// Correlates against `kb.id`, so the caller's FROM clause must alias
// knowledge_bases as `kb` (both list queries already do, for kbStatsJoins).
func kbMembershipCols(userIDParam string) string {
	return `,
       (SELECT role FROM kb_members WHERE kb_id = kb.id AND user_id = ` + userIDParam + `) AS my_role,
       (SELECT COUNT(*)::int FROM kb_members WHERE kb_id = kb.id)         AS member_count`
}

func toKBRow(r kbFullRow) KBRow {
	return KBRow{
		ID:             r.ID,
		Name:           r.Name,
		UserID:         r.UserID,
		Description:    r.Description,
		IsGlobal:       r.IsGlobal,
		IsPublished:    r.IsPublished,
		Language:       r.Language,
		AIConfigID:     r.AIConfigID,
		ChatModel:      r.ChatModel,
		EmbeddingModel: r.EmbeddingModel,
		RerankModel:    r.RerankModel,
		TTSModel:       r.TTSModel,
		SttModel:       r.SttModel,
		SystemPrompt:   r.SystemPrompt,
		HeaderText:     r.HeaderText,
		ExamplePrompts: r.ExamplePrompts,
		StudioConfig:   r.StudioConfig,
		ChunkSize:      r.ChunkSize,
		ChunkOverlap:   r.ChunkOverlap,
		CreatedAt:      r.CreatedAt,
		OwnerFirstName: r.OwnerFirstName,
		OwnerLastName:  r.OwnerLastName,
		OwnerUsername:  r.OwnerUsername,
	}
}

// kbListRow scans a knowledge_bases row plus the card-metadata aggregates from
// kbStatsCols. Embedding kbFullRow keeps the base column mapping in one place;
// pgx.RowToStructByName flattens the embedded fields.
type kbListRow struct {
	kbFullRow
	FileCount           int        `db:"file_count"`
	FailedFileCount     int        `db:"failed_file_count"`
	ProcessingFileCount int        `db:"processing_file_count"`
	MessageCount        int        `db:"message_count"`
	LastMessageAt       *time.Time `db:"last_message_at"`
	MyRole              *string    `db:"my_role"`
	MemberCount         int        `db:"member_count"`
}

func toKBRowWithStats(r kbListRow) KBRow {
	row := toKBRow(r.kbFullRow)
	row.FileCount = r.FileCount
	row.FailedFileCount = r.FailedFileCount
	row.ProcessingFileCount = r.ProcessingFileCount
	row.MessageCount = r.MessageCount
	row.LastMessageAt = r.LastMessageAt
	row.MyRole = r.MyRole
	row.MemberCount = r.MemberCount
	return row
}

// ---------------------------------------------------------------------------
// kb.Store implementation
// ---------------------------------------------------------------------------

// listKnowledgeBasesMaxLimit is a defense-in-depth cap on the per-page row
// count surfaced to callers of ListKnowledgeBases. All current HTTP callers
// already enforce their own bounds (kb/http.go:100, publicapi:100,
// openaicompat:1000 hardcoded, KB router:100); this guards against a future
// caller passing an unbounded user-supplied limit.
const listKnowledgeBasesMaxLimit = 1000

// ListKnowledgeBases returns the user's personal KBs: those they own plus those
// explicitly shared with them. Global published KBs are intentionally excluded
// — the frontend and API clients fetch those separately via
// ListGlobalKnowledgeBases, and including them here caused them to appear
// twice in the UI.
// Results are ordered by created_at DESC with limit/offset pagination.
// limit is clamped to listKnowledgeBasesMaxLimit and silently coerced to 1
// when ≤ 0; offset < 0 is coerced to 0.
func (s *PGStore) ListKnowledgeBases(ctx context.Context, userID string, limit, offset int) ([]KBRow, error) {
	if limit <= 0 {
		limit = 1
	}
	if limit > listKnowledgeBasesMaxLimit {
		limit = listKnowledgeBasesMaxLimit
	}
	if offset < 0 {
		offset = 0
	}
	// Membership is expressed as an EXISTS subquery against kb_members rather
	// than a LEFT JOIN + DISTINCT. The join form could emit one row per
	// membership for a KB with multiple rows, which forced a DISTINCT
	// sort/dedup of the full result set before LIMIT/OFFSET — defeating
	// index-driven pagination. EXISTS yields at most one row per KB by
	// construction, letting the planner use the btree index on
	// kb_members.kb_id. The owner always has a kb_members row since
	// migration 0064, so a separate kb.user_id = $1 clause is unnecessary.
	// sql concatenates kbMembershipCols's function-call result, so it cannot
	// be a const (unlike the analogous ListGlobalKnowledgeBases query below,
	// this one was previously a const; keep it a plain string now).
	sql := `
		SELECT ` + kbSelectCols + `,
		       u.first_name AS owner_first_name,
		       u.last_name  AS owner_last_name,
		       u.username   AS owner_username` + kbStatsCols + kbMembershipCols("$1") + `
		FROM knowledge_bases kb
		LEFT JOIN users u ON kb.user_id = u.id` + kbStatsJoins + `
		WHERE EXISTS (
		    SELECT 1 FROM kb_members
		    WHERE kb_id = kb.id AND user_id = $1
		)
		ORDER BY kb.created_at DESC
		LIMIT $2 OFFSET $3`

	rows, err := pgxutil.QueryRows[kbListRow](ctx, s.pool, sql, userID, limit, offset)
	if err != nil {
		return []KBRow{}, err
	}

	result := make([]KBRow, len(rows))
	for i, r := range rows {
		result[i] = toKBRowWithStats(r)
	}
	return result, nil
}

// ListGlobalKnowledgeBases returns global KBs. Admins see all; non-admins see only published.
//
// Results are capped at listKnowledgeBasesMaxLimit rows as a defense-in-depth
// guard: on a deployment with many global KBs an uncapped query would stream
// every row into the process on every call. The admin variant skips the
// is_published filter and is especially exposed.
// userID now also seeds kbMembershipCols's correlated my_role subquery, so a
// global KB the caller has an explicit kb_members row on (e.g. an admin- or
// self-added membership) reports that role instead of the implicit-viewer
// null.
func (s *PGStore) ListGlobalKnowledgeBases(ctx context.Context, userID string, isAdmin bool) ([]KBRow, error) {
	var sql string
	if isAdmin {
		sql = `
			SELECT ` + kbSelectColsNoAlias + kbStatsCols + kbMembershipCols("$1") + `
			FROM knowledge_bases kb` + kbStatsJoins + `
			WHERE is_global = true
			ORDER BY created_at DESC
			LIMIT $2`
	} else {
		sql = `
			SELECT ` + kbSelectColsNoAlias + kbStatsCols + kbMembershipCols("$1") + `
			FROM knowledge_bases kb` + kbStatsJoins + `
			WHERE is_global = true AND is_published = true
			ORDER BY created_at DESC
			LIMIT $2`
	}

	rows, err := pgxutil.QueryRows[kbListRow](ctx, s.pool, sql, userID, listKnowledgeBasesMaxLimit)
	if err != nil {
		return []KBRow{}, err
	}

	result := make([]KBRow, len(rows))
	for i, r := range rows {
		result[i] = toKBRowWithStats(r)
	}
	return result, nil
}

// CreateKnowledgeBase inserts a new knowledge base owned by userID and returns
// the created row. Writes both the knowledge_bases row and the owner's
// kb_members row (role='owner') in one transaction — kb_members is the
// authority kbaccess.EffectiveRole reads (Task 1/4); without this row the
// creator would have no role on their own KB. The direct user_id write below
// and the kb_members insert use the same userID value, so they cannot
// diverge; migration 0064's kb_members_sync_owner_trg trigger re-applies the
// same value to knowledge_bases.user_id as a (harmless, idempotent) side
// effect of the kb_members insert.
func (s *PGStore) CreateKnowledgeBase(ctx context.Context, name string, description *string, userID string, systemPrompt *string) (*KBRow, error) {
	var result *KBRow
	err := pgxutil.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		const sql = `
			INSERT INTO knowledge_bases (name, description, user_id, system_prompt)
			VALUES ($1, $2, $3, $4)
			RETURNING ` + kbSelectColsNoAlias

		row, err := pgxutil.QueryOne[kbFullRow](ctx, tx, sql, name, description, userID, systemPrompt)
		if err != nil {
			return err
		}
		if row == nil {
			return fmt.Errorf("CreateKnowledgeBase: no row returned")
		}
		r := toKBRow(*row)

		if _, err := tx.Exec(ctx,
			`INSERT INTO kb_members (kb_id, user_id, role) VALUES ($1, $2, $3)`,
			r.ID, userID, kbaccess.RoleOwner,
		); err != nil {
			return fmt.Errorf("CreateKnowledgeBase: insert owner kb_members row: %w", err)
		}

		result = &r
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// kb.UpdateStore implementation
// ---------------------------------------------------------------------------

// addOrNull combines the two patterns used across the nullable-string columns:
// set to val when non-nil, or set to NULL when jsonKey is in data.NullFields.
// The shared pgxutil.ClauseBuilder keeps each `$N` in lock-step with its bind
// arg; the builder is created with base=1 so the first SET arg binds $2 (id is
// $1 in the WHERE clause).
func addOrNull(b *pgxutil.ClauseBuilder, column string, val *string, nullFields map[string]bool, jsonKey string) {
	if val != nil {
		b.Add(column+" = $%d", *val)
	} else if nullFields[jsonKey] {
		b.AddRaw(column + " = NULL")
	}
}

// UpdateKnowledgeBase applies the non-nil fields in data to the knowledge base
// identified by id. Returns (nil, store.ErrNotFound) when no matching row
// exists — callers match with errors.Is to distinguish "missing" from other
// failure modes.
func (s *PGStore) UpdateKnowledgeBase(ctx context.Context, id string, data KBUpdate) (*KBRow, error) {
	b := pgxutil.NewClauseBuilder(1) // id=$1 in WHERE; first SET arg binds $2

	if data.Name != nil {
		b.Add("name = $%d", *data.Name)
	}
	addOrNull(b, "description", data.Description, data.NullFields, "description")
	if data.Language != nil {
		b.Add("language = $%d", *data.Language)
	}
	addOrNull(b, "system_prompt", data.SystemPrompt, data.NullFields, "systemPrompt")
	addOrNull(b, "header_text", data.HeaderText, data.NullFields, "headerText")
	addOrNull(b, "example_prompts", data.ExamplePrompts, data.NullFields, "examplePrompts")
	addOrNull(b, "tts_model", data.TTSModel, data.NullFields, "ttsModel")
	addOrNull(b, "stt_model", data.SttModel, data.NullFields, "sttModel")
	addOrNull(b, "ai_config_id", data.AIConfigID, data.NullFields, "aiConfigId")
	addOrNull(b, "chat_model", data.ChatModel, data.NullFields, "chatModel")
	addOrNull(b, "embedding_model", data.EmbeddingModel, data.NullFields, "embeddingModel")
	addOrNull(b, "rerank_model", data.RerankModel, data.NullFields, "rerankModel")
	if data.ChunkSize != nil {
		b.Add("chunk_size = $%d", *data.ChunkSize)
	}
	if data.ChunkOverlap != nil {
		b.Add("chunk_overlap = $%d", *data.ChunkOverlap)
	}
	if data.IsPublished != nil {
		b.Add("is_published = $%d", *data.IsPublished)
	}
	if data.StudioConfig != nil {
		jsonBytes, err := json.Marshal(data.StudioConfig)
		if err != nil {
			return nil, fmt.Errorf("UpdateKnowledgeBase: marshal studio_config: %w", err)
		}
		b.Add("studio_config = $%d", jsonBytes)
	}

	if b.Len() == 0 {
		// Nothing to update — return the current row unchanged.
		row, err := pgxutil.QueryOne[kbFullRow](ctx, s.pool,
			`SELECT `+kbSelectColsNoAlias+` FROM knowledge_bases WHERE id = $1`, id)
		if err != nil {
			return nil, err
		}
		if row == nil {
			return nil, store.ErrNotFound
		}
		r := toKBRow(*row)
		return &r, nil
	}

	updateSQL := fmt.Sprintf(
		`UPDATE knowledge_bases SET %s WHERE id = $1 RETURNING `+kbSelectColsNoAlias,
		strings.Join(b.Clauses(), ", "),
	)
	allArgs := append([]any{id}, b.Args()...)

	row, err := pgxutil.QueryOne[kbFullRow](ctx, s.pool, updateSQL, allArgs...)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, store.ErrNotFound
	}
	r := toKBRow(*row)
	return &r, nil
}

// fileDBRow is an internal struct with db tags for scanning files rows.
// TotalCount is populated only by ListFiles via a COUNT(*) OVER () window
// so the total and the page derive from a single MVCC snapshot.
type fileDBRow struct {
	ID                 string    `db:"id"`
	Name               string    `db:"name"`
	Type               string    `db:"type"`
	Size               *int      `db:"size"`
	Status             string    `db:"status"`
	Progress           int       `db:"progress"`
	Origin             string    `db:"origin"`
	ErrorStage         *string   `db:"error_stage"`
	ErrorMessage       *string   `db:"error_message"`
	CurrentStage       *string   `db:"current_stage"`
	StageIndex         *int      `db:"stage_index"`
	StageTotal         *int      `db:"stage_total"`
	RSSFeedID          *string   `db:"rss_feed_id"`
	ConfluenceSourceID *string   `db:"confluence_source_id"`
	CreatedAt          time.Time `db:"created_at"`
	TotalCount         int       `db:"total_count"`
}

// ListFiles returns a paginated slice of files for kbID, ordered by created_at DESC,
// together with the total count of files in that KB. The count is computed
// via COUNT(*) OVER () in the same statement as the page so both reflect the
// same MVCC snapshot — no race window where the count and the rows can
// disagree under concurrent inserts/deletes.
func (s *PGStore) ListFiles(ctx context.Context, kbID string, limit, offset int) ([]FileRow, int, error) {
	const listSQL = `
		SELECT id, name, type, size, status, progress, origin,
		       error_stage, error_message, current_stage, stage_index, stage_total,
		       rss_feed_id, confluence_source_id, created_at,
		       COUNT(*) OVER ()::int AS total_count
		FROM files
		WHERE kb_id = $1
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3`

	rows, err := pgxutil.QueryRows[fileDBRow](ctx, s.pool, listSQL, kbID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("ListFiles query: %w", err)
	}

	if len(rows) == 0 {
		// Empty page — issue a separate count so an offset past the end
		// (or an empty KB) still surfaces the correct total. Same MVCC
		// concern applies, but the page is empty so no row/count drift
		// can be observed.
		var total int
		if err := s.pool.QueryRow(ctx,
			`SELECT COUNT(*)::int FROM files WHERE kb_id = $1`, kbID,
		).Scan(&total); err != nil {
			return nil, 0, fmt.Errorf("ListFiles count: %w", err)
		}
		return []FileRow{}, total, nil
	}

	result := make([]FileRow, len(rows))
	for i, r := range rows {
		result[i] = FileRow{
			ID:                 r.ID,
			Name:               r.Name,
			Type:               r.Type,
			Size:               r.Size,
			Status:             r.Status,
			Progress:           r.Progress,
			Origin:             r.Origin,
			ErrorStage:         r.ErrorStage,
			ErrorMessage:       r.ErrorMessage,
			CurrentStage:       r.CurrentStage,
			StageIndex:         r.StageIndex,
			StageTotal:         r.StageTotal,
			RSSFeedID:          r.RSSFeedID,
			ConfluenceSourceID: r.ConfluenceSourceID,
			CreatedAt:          r.CreatedAt,
		}
	}
	return result, rows[0].TotalCount, nil
}

// ---------------------------------------------------------------------------
// kb.ShareStore implementation
// ---------------------------------------------------------------------------

// kbShareListRow is an internal struct for scanning knowledge_base_shares joined with users.
type kbShareListRow struct {
	ID         string    `db:"id"`
	UserID     string    `db:"user_id"`
	Username   string    `db:"username"`
	Permission string    `db:"permission"`
	CreatedAt  time.Time `db:"created_at"`
}

// ListKBShares returns all share entries for the given KB, joined with users for username.
func (s *PGStore) ListKBShares(ctx context.Context, kbID string) ([]ShareRow, error) {
	const sql = `
		SELECT kbs.id, kbs.user_id, u.username, kbs.permission, kbs.created_at
		FROM knowledge_base_shares kbs
		INNER JOIN users u ON kbs.user_id = u.id
		WHERE kbs.kb_id = $1
		ORDER BY kbs.created_at DESC`

	rows, err := pgxutil.QueryRows[kbShareListRow](ctx, s.pool, sql, kbID)
	if err != nil {
		return []ShareRow{}, err
	}

	result := make([]ShareRow, len(rows))
	for i, r := range rows {
		result[i] = ShareRow(r)
	}
	return result, nil
}

// addShareRow is an internal struct for scanning the RETURNING clause of AddKBShare.
type addShareRow struct {
	ID         string    `db:"id"`
	UserID     string    `db:"user_id"`
	Permission string    `db:"permission"`
	CreatedAt  time.Time `db:"created_at"`
}

// AddKBShare upserts a share entry for kbID+userID. If the pair already exists,
// the permission is updated. Returns the stored row (username is not populated).
func (s *PGStore) AddKBShare(ctx context.Context, kbID, userID, permission string) (*ShareRow, error) {
	const sql = `
		INSERT INTO knowledge_base_shares (kb_id, user_id, permission)
		VALUES ($1, $2, $3)
		ON CONFLICT (kb_id, user_id) DO UPDATE SET permission = EXCLUDED.permission
		RETURNING id, user_id, permission, created_at`

	row, err := pgxutil.QueryOne[addShareRow](ctx, s.pool, sql, kbID, userID, permission)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, fmt.Errorf("AddKBShare: no row returned")
	}
	return &ShareRow{
		ID:         row.ID,
		UserID:     row.UserID,
		Permission: row.Permission,
		CreatedAt:  row.CreatedAt,
	}, nil
}

// RemoveKBShare deletes the share entry for kbID+userID.
// Wraps store.ErrNotFound with the (kbID, userID) pair if no row was deleted.
func (s *PGStore) RemoveKBShare(ctx context.Context, kbID, userID string) error {
	ct, err := s.pool.Exec(ctx,
		`DELETE FROM knowledge_base_shares WHERE kb_id = $1 AND user_id = $2`, kbID, userID)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("kb_share kb=%s user=%s: %w", kbID, userID, store.ErrNotFound)
	}
	return nil
}

// GetUserIDByUsername resolves a username to a user id, case-insensitively.
// found is false (with nil error) when no such user exists yet.
func (s *PGStore) GetUserIDByUsername(ctx context.Context, username string) (string, bool, error) {
	var id string
	err := s.pool.QueryRow(ctx,
		`SELECT id FROM users WHERE LOWER(username) = LOWER($1) LIMIT 1`, username).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, err
	}
	return id, true, nil
}

// ShareExists reports whether a share row already exists for kbID+userID.
func (s *PGStore) ShareExists(ctx context.Context, kbID, userID string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM knowledge_base_shares WHERE kb_id = $1 AND user_id = $2)`,
		kbID, userID).Scan(&exists)
	return exists, err
}

// UpsertPendingInvite stores (or updates the role of) a pending invite for
// a username that does not yet exist as a user. invitedBy may be "" (stored NULL).
// The column is named pending_kb_invites.role as of migration 0064's Task 7
// addendum ("permission" renamed so "admin" can be invited too); the
// permission parameter name is kept here for API/JSON-tag stability on the
// (deprecated) callers above, it is only the DB column that changed.
func (s *PGStore) UpsertPendingInvite(ctx context.Context, kbID, username, permission, invitedBy string) error {
	var by *string
	if invitedBy != "" {
		by = &invitedBy
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO pending_kb_invites (kb_id, username, role, invited_by)
		VALUES ($1, LOWER($2), $3, $4)
		ON CONFLICT (kb_id, LOWER(username)) DO UPDATE SET role = EXCLUDED.role`,
		kbID, username, permission, by)
	return err
}

// pendingInviteDBRow scans a pending_kb_invites row. The Permission field's db
// tag maps it to the "role" column (migration 0064's Task 7 rename) while
// keeping the Go/JSON name unchanged for the deprecated ShareStore surface.
type pendingInviteDBRow struct {
	Username   string    `db:"username"`
	Permission string    `db:"role"`
	CreatedAt  time.Time `db:"created_at"`
}

// ListPendingInvites returns all unapplied invites for a KB, newest first.
func (s *PGStore) ListPendingInvites(ctx context.Context, kbID string) ([]PendingInviteRow, error) {
	const sql = `
		SELECT username, role, created_at
		FROM pending_kb_invites
		WHERE kb_id = $1
		ORDER BY created_at DESC`
	rows, err := pgxutil.QueryRows[pendingInviteDBRow](ctx, s.pool, sql, kbID)
	if err != nil {
		return []PendingInviteRow{}, err
	}
	result := make([]PendingInviteRow, len(rows))
	for i, r := range rows {
		result[i] = PendingInviteRow(r)
	}
	return result, nil
}

// RemovePendingInvite deletes a pending invite by KB + username (case-insensitive).
// Wraps store.ErrNotFound when no row was deleted.
func (s *PGStore) RemovePendingInvite(ctx context.Context, kbID, username string) error {
	ct, err := s.pool.Exec(ctx,
		`DELETE FROM pending_kb_invites WHERE kb_id = $1 AND LOWER(username) = LOWER($2)`,
		kbID, username)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("pending_invite kb=%s user=%s: %w", kbID, username, store.ErrNotFound)
	}
	return nil
}

// ---------------------------------------------------------------------------
// GetKBChunkConfig — used by worker.KBChunkConfigStore
// ---------------------------------------------------------------------------

// GetKBChunkConfig returns the chunk_size and chunk_overlap for a KB. Returns
// 0, 0, nil when the KB row is absent or the columns are NULL — callers
// fall back to the global default in either case, so a missing KB is not
// distinguished from "no override set" here.
func (s *PGStore) GetKBChunkConfig(ctx context.Context, kbID string) (chunkSize, chunkOverlap int, err error) {
	var cs, co *int
	err = s.pool.QueryRow(ctx, `SELECT chunk_size, chunk_overlap FROM knowledge_bases WHERE id = $1`, kbID).Scan(&cs, &co)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, 0, nil
		}
		return 0, 0, err
	}
	if cs != nil {
		chunkSize = *cs
	}
	if co != nil {
		chunkOverlap = *co
	}
	return chunkSize, chunkOverlap, nil
}

// ---------------------------------------------------------------------------
// GetKBSystemPrompt — used by openaicompat.Store and publicapi.Store
// ---------------------------------------------------------------------------

// GetKBSystemPrompt returns the system_prompt for a KB, or nil if not set.
// A missing KB row maps to (nil, nil) — same as "column is NULL" — because
// callers treat both as "no per-KB override".
func (s *PGStore) GetKBSystemPrompt(ctx context.Context, kbID string) (*string, error) {
	var sp *string
	err := s.pool.QueryRow(ctx, `SELECT system_prompt FROM knowledge_bases WHERE id = $1`, kbID).Scan(&sp)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return sp, nil
}

// ---------------------------------------------------------------------------
// ListAllKBIDs / ListReembedableFilesByKBID — used by reembed-all handler
// ---------------------------------------------------------------------------

// kbIDRow holds a single knowledge base ID.
type kbIDRow struct {
	ID string `db:"id"`
}

// ListAllKBIDs returns the IDs of every knowledge base in the system.
func (s *PGStore) ListAllKBIDs(ctx context.Context) ([]string, error) {
	rows, err := pgxutil.QueryRows[kbIDRow](ctx, s.pool, `SELECT id FROM knowledge_bases ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("ListAllKBIDs: %w", err)
	}
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	return ids, nil
}

// FileReembedRow holds the minimal fields needed to enqueue a re-embed job.
type FileReembedRow struct {
	ID          string  `db:"id"`
	KbID        string  `db:"kb_id"`
	StoragePath *string `db:"storage_path"`
	Name        string  `db:"name"`
	Type        string  `db:"type"`
}

// ListReembedableFilesByKBID returns file records eligible for re-embedding:
// 'completed', 'partial', or 'error'. Including 'error' is the key recovery
// path — re-embed deletes old chunks before re-processing, so when a file
// transiently fails (e.g. embedder rate-limit) it ends up with no chunks AND
// status='error'. Without 'error' in this list, re-embed-all skips them on
// every subsequent run and the recall hole becomes permanent.
func (s *PGStore) ListReembedableFilesByKBID(ctx context.Context, kbID string) ([]FileReembedRow, error) {
	const sql = `
		SELECT id, kb_id, storage_path, name, type
		FROM files
		WHERE kb_id = $1
		  AND status IN ('completed', 'partial', 'error')
		ORDER BY created_at`
	return pgxutil.QueryRows[FileReembedRow](ctx, s.pool, sql, kbID)
}

// GetKBInfo returns the name of the knowledge base with the given UUID and
// whether it exists. A non-nil error signals a DB failure distinct from
// "not found" (which returns ("", false, nil)).
func (s *PGStore) GetKBInfo(ctx context.Context, id uuid.UUID) (string, bool, error) {
	var name string
	err := s.pool.QueryRow(ctx, `SELECT name FROM knowledge_bases WHERE id = $1`, id).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return name, true, nil
}
