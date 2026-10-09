package chat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/singleflight"

	"github.com/justrag/go-backend/internal/pgxutil"
	"github.com/justrag/go-backend/internal/store"
)

// siteConfigStoreCacheTTL bounds how long an admin edit waits to propagate
// to the chat hot path. Matches the search package's
// loadSiteConfigCached convention so operators see consistent staleness
// behaviour across subsystems that read site_configs.
const siteConfigStoreCacheTTL = 10 * time.Second

// siteConfigStoreCacheEntry is the immutable whole-table snapshot stored in
// the atomic pointer. site_configs is bounded (a few dozen admin-managed
// keys) so a full-table fetch is cheaper than per-key queries with a per-key
// cache; readers do a map lookup on the snapshot.
type siteConfigStoreCacheEntry struct {
	values    map[string]*string
	expiresAt time.Time
}

// PGStore is a PostgreSQL-backed implementation of the chat Store interface.
// It also satisfies research.ResearchStore via the additional methods
// CreateResearchSession, SaveResearchResults, and GetSiteConfigValue.
type PGStore struct {
	pool *pgxpool.Pool

	// siteConfigCache memoises a whole-table snapshot of site_configs for
	// siteConfigStoreCacheTTL. The chat dispatch path reads ~10+ feature-flag
	// keys per request through GetSiteConfigValue; without this cache every
	// flag is a separate DB round-trip before retrieval starts.
	//
	// atomic.Pointer + singleflight follows the pattern in
	// internal/vector/search.go: hot reads are a wait-free pointer load + a
	// time comparison; refreshes coalesce into one DB round-trip per TTL
	// window. Admin writes go through siteconfig.PGStore (uncached) and
	// become visible to chat within one TTL.
	siteConfigCache atomic.Pointer[siteConfigStoreCacheEntry]
	siteConfigSF    singleflight.Group
}

// NewStore creates a new PGStore backed by pool.
func NewStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

// Compile-time interface assertions catch drift between the chat.Store
// contract and its production implementation. Mirrors the convention used in
// adminconfigs, apikeys, siteconfig, etc.
var _ Store = (*PGStore)(nil)

// chatDBRow is an internal struct with db tags for scanning the chats table.
type chatDBRow struct {
	ID        string    `db:"id"`
	KbID      string    `db:"kb_id"`
	UserID    string    `db:"user_id"`
	Title     string    `db:"title"`
	Type      string    `db:"type"`
	TeamID    *string   `db:"team_id"`
	AgentID   *string   `db:"agent_id"`
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}

// messageDBRow is an internal struct with db tags for scanning the messages
// table. Sources and Verification are scanned as raw JSONB bytes
// (json.RawMessage) and decoded into the typed MessageRow fields by
// toMessageRow — that keeps the SQL-scanning layer independent of the
// concrete shapes and avoids paying for a decode on rows whose JSONB column
// is empty/null.
type messageDBRow struct {
	ID                string          `db:"id"`
	ChatID            string          `db:"chat_id"`
	ParentMessageID   *string         `db:"parent_message_id"`
	Role              string          `db:"role"`
	Content           string          `db:"content"`
	Sources           json.RawMessage `db:"sources"`
	IsEnhanced        bool            `db:"is_enhanced"`
	EnhancedQuery     *string         `db:"enhanced_query"`
	Reasoning         *string         `db:"reasoning"`
	Feedback          *string         `db:"feedback"`
	FeedbackComment   *string         `db:"feedback_comment"`
	FeedbackUpdatedAt *time.Time      `db:"feedback_updated_at"`
	Verification      json.RawMessage `db:"verification"`
	TraceID           *string         `db:"trace_id"`
	StructuredTable   json.RawMessage `db:"structured_table"`
	Conflicts         json.RawMessage `db:"conflicts"`
	TeamID            *string         `db:"team_id"`
	AgentID           *string         `db:"agent_id"`
	CreatedAt         time.Time       `db:"created_at"`
}

func toChatRow(r chatDBRow) ChatRow {
	return ChatRow(r)
}

func toMessageRow(r messageDBRow) (MessageRow, error) {
	out := MessageRow{
		ID:                r.ID,
		ChatID:            r.ChatID,
		ParentMessageID:   r.ParentMessageID,
		Role:              r.Role,
		Content:           r.Content,
		IsEnhanced:        r.IsEnhanced,
		EnhancedQuery:     r.EnhancedQuery,
		Reasoning:         r.Reasoning,
		Feedback:          r.Feedback,
		FeedbackComment:   r.FeedbackComment,
		FeedbackUpdatedAt: r.FeedbackUpdatedAt,
		TraceID:           r.TraceID,
		TeamID:            r.TeamID,
		AgentID:           r.AgentID,
		CreatedAt:         r.CreatedAt,
	}
	if len(r.Sources) > 0 && !isJSONNull(r.Sources) {
		// Decode purely to validate: a malformed or type-drifted column must
		// still surface as an error rather than reaching the client. The
		// decoded value is deliberately discarded — see MessageRow.Sources.
		var sources []ChatSource
		if err := json.Unmarshal(r.Sources, &sources); err != nil {
			return MessageRow{}, fmt.Errorf("decode sources for message %s: %w", r.ID, err)
		}
		out.Sources = r.Sources
	}
	if len(r.Verification) > 0 && !isJSONNull(r.Verification) {
		var v MessageVerification
		if err := json.Unmarshal(r.Verification, &v); err != nil {
			return MessageRow{}, fmt.Errorf("decode verification for message %s: %w", r.ID, err)
		}
		out.Verification = &v
	}
	if len(r.StructuredTable) > 0 && !isJSONNull(r.StructuredTable) {
		var t StructuredTable
		if err := json.Unmarshal(r.StructuredTable, &t); err != nil {
			return MessageRow{}, fmt.Errorf("decode structured_table for message %s: %w", r.ID, err)
		}
		out.StructuredTable = &t
	}
	if len(r.Conflicts) > 0 && !isJSONNull(r.Conflicts) {
		// The column holds the bare array — the same shape the SSE frame and
		// the response body carry (W5 "one wire shape" ruling).
		var c []MessageConflict
		if err := json.Unmarshal(r.Conflicts, &c); err != nil {
			return MessageRow{}, fmt.Errorf("decode conflicts for message %s: %w", r.ID, err)
		}
		out.Conflicts = c
	}
	return out, nil
}

// isJSONNull returns true when raw is the JSON literal `null`. pgx scans a
// SQL-NULL jsonb column as a nil RawMessage and a JSON-`null` value as the
// literal bytes; both must be treated as "no value" by toMessageRow.
func isJSONNull(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 4 && string(trimmed) == "null"
}

// GetChats returns all chats for the given kbID and userID, ordered by updated_at DESC.
func (s *PGStore) GetChats(ctx context.Context, kbID, userID string) ([]ChatRow, error) {
	const sql = `
		SELECT id, COALESCE(kb_id::text, '') AS kb_id, user_id, title, type, team_id, agent_id, created_at, updated_at
		FROM chats
		WHERE kb_id = $1 AND user_id = $2
		ORDER BY updated_at DESC`

	rows, err := pgxutil.QueryRows[chatDBRow](ctx, s.pool, sql, kbID, userID)
	if err != nil {
		return nil, fmt.Errorf("GetChats: %w", err)
	}
	result := make([]ChatRow, len(rows))
	for i, r := range rows {
		result[i] = toChatRow(r)
	}
	return result, nil
}

// GetChatByID returns the chat with the given ID, or nil if not found.
func (s *PGStore) GetChatByID(ctx context.Context, chatID string) (*ChatRow, error) {
	const sql = `
		SELECT id, COALESCE(kb_id::text, '') AS kb_id, user_id, title, type, team_id, agent_id, created_at, updated_at
		FROM chats WHERE id = $1`

	row, err := pgxutil.QueryOne[chatDBRow](ctx, s.pool, sql, chatID)
	if err != nil {
		return nil, fmt.Errorf("GetChatByID: %w", err)
	}
	if row == nil {
		return nil, nil
	}
	r := toChatRow(*row)
	return &r, nil
}

// CreateChat inserts a new chat and returns the created row.
func (s *PGStore) CreateChat(ctx context.Context, kbID, userID, title string) (*ChatRow, error) {
	const sql = `
		INSERT INTO chats (kb_id, user_id, title)
		VALUES ($1, $2, $3)
		RETURNING id, COALESCE(kb_id::text, '') AS kb_id, user_id, title, type, team_id, agent_id, created_at, updated_at`

	row, err := pgxutil.QueryOne[chatDBRow](ctx, s.pool, sql, kbID, userID, title)
	if err != nil {
		return nil, fmt.Errorf("CreateChat: %w", err)
	}
	if row == nil {
		return nil, fmt.Errorf("CreateChat: no row returned")
	}
	r := toChatRow(*row)
	return &r, nil
}

// CreateChatWithID inserts a chat with a caller-chosen id (the agent chat's
// thread id, handed to the client before the chat exists).
func (s *PGStore) CreateChatWithID(ctx context.Context, id, kbID, userID, title string) (*ChatRow, error) {
	const sql = `
		INSERT INTO chats (id, kb_id, user_id, title)
		VALUES ($1, $2, $3, $4)
		RETURNING id, kb_id, user_id, title, type, team_id, agent_id, created_at, updated_at`

	row, err := pgxutil.QueryOne[chatDBRow](ctx, s.pool, sql, id, kbID, userID, title)
	if err != nil {
		return nil, fmt.Errorf("CreateChatWithID: %w", err)
	}
	if row == nil {
		return nil, fmt.Errorf("CreateChatWithID: no row returned")
	}
	r := toChatRow(*row)
	return &r, nil
}

// CreateLibraryChat inserts a KB-less library chat (kb_id NULL, type
// 'library') and returns the created row (KbID "").
func (s *PGStore) CreateLibraryChat(ctx context.Context, userID, title string) (*ChatRow, error) {
	const sql = `
		INSERT INTO chats (kb_id, user_id, title, type)
		VALUES (NULL, $1, $2, 'library')
		RETURNING id, COALESCE(kb_id::text, '') AS kb_id, user_id, title, type, team_id, agent_id, created_at, updated_at`

	row, err := pgxutil.QueryOne[chatDBRow](ctx, s.pool, sql, userID, title)
	if err != nil {
		return nil, fmt.Errorf("CreateLibraryChat: %w", err)
	}
	if row == nil {
		return nil, fmt.Errorf("CreateLibraryChat: no row returned")
	}
	r := toChatRow(*row)
	return &r, nil
}

// LastMessageID returns the id of the chat's newest message, nil when the
// chat has none.
func (s *PGStore) LastMessageID(ctx context.Context, chatID string) (*string, error) {
	var id string
	err := s.pool.QueryRow(ctx,
		`SELECT id::text FROM messages WHERE chat_id = $1 ORDER BY created_at DESC, id DESC LIMIT 1`, chatID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("LastMessageID: %w", err)
	}
	return &id, nil
}

// GetLibraryChats returns the user's library chats, newest activity first.
func (s *PGStore) GetLibraryChats(ctx context.Context, userID string) ([]ChatRow, error) {
	const sql = `
		SELECT id, COALESCE(kb_id::text, '') AS kb_id, user_id, title, type, team_id, agent_id, created_at, updated_at
		FROM chats
		WHERE type = 'library' AND kb_id IS NULL AND user_id = $1
		ORDER BY updated_at DESC`

	rows, err := pgxutil.QueryRows[chatDBRow](ctx, s.pool, sql, userID)
	if err != nil {
		return nil, fmt.Errorf("GetLibraryChats: %w", err)
	}
	result := make([]ChatRow, len(rows))
	for i, r := range rows {
		result[i] = toChatRow(r)
	}
	return result, nil
}

// GetChatFileRefs returns the library file ids selected for a chat, in the
// order they were added.
func (s *PGStore) GetChatFileRefs(ctx context.Context, chatID string) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT user_file_id::text FROM chat_file_refs WHERE chat_id = $1::uuid ORDER BY added_at, user_file_id`, chatID)
	if err != nil {
		return nil, fmt.Errorf("GetChatFileRefs: %w", err)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("GetChatFileRefs scan: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("GetChatFileRefs: %w", err)
	}
	return ids, nil
}

// GetChatsFileRefs returns the selected library file ids of several chats in
// one query, keyed by chat id, each list in the order GetChatFileRefs uses.
// A chat without refs is absent from the map.
func (s *PGStore) GetChatsFileRefs(ctx context.Context, chatIDs []string) (map[string][]string, error) {
	out := make(map[string][]string, len(chatIDs))
	if len(chatIDs) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT chat_id::text, user_file_id::text FROM chat_file_refs
		 WHERE chat_id = ANY($1::uuid[]) ORDER BY chat_id, added_at, user_file_id`, chatIDs)
	if err != nil {
		return nil, fmt.Errorf("GetChatsFileRefs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var chatID, id string
		if err := rows.Scan(&chatID, &id); err != nil {
			return nil, fmt.Errorf("GetChatsFileRefs scan: %w", err)
		}
		out[chatID] = append(out[chatID], id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("GetChatsFileRefs: %w", err)
	}
	return out, nil
}

// ErrChatFileRefGone means a library file (or the chat) was deleted between
// validation and ReplaceChatFileRefs, so a reference row could not be written.
var ErrChatFileRefGone = errors.New("chat file reference target no longer exists")

// ReplaceChatFileRefs atomically replaces a chat's selected library files.
// The given order is preserved: rows get strictly increasing added_at
// within the transaction. The chat row is locked first, so two concurrent
// replacements serialize (the later one wins whole) instead of interleaving
// their DELETE/INSERT into the union of both selections. A chat deleted
// meanwhile locks nothing and the INSERT's FK failure reports it.
func (s *PGStore) ReplaceChatFileRefs(ctx context.Context, chatID string, userFileIDs []string) error {
	return pgxutil.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT 1 FROM chats WHERE id = $1::uuid FOR UPDATE`, chatID); err != nil {
			return fmt.Errorf("ReplaceChatFileRefs lock: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM chat_file_refs WHERE chat_id = $1::uuid`, chatID); err != nil {
			return fmt.Errorf("ReplaceChatFileRefs delete: %w", err)
		}
		if len(userFileIDs) == 0 {
			return nil
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO chat_file_refs (chat_id, user_file_id, added_at)
			SELECT $1::uuid, f.id, now() + f.ord * interval '1 microsecond'
			FROM unnest($2::uuid[]) WITH ORDINALITY AS f(id, ord)
			ON CONFLICT DO NOTHING`, chatID, userFileIDs); err != nil {
			if pgxutil.IsForeignKeyViolation(err) {
				return fmt.Errorf("ReplaceChatFileRefs insert: %w", ErrChatFileRefGone)
			}
			return fmt.Errorf("ReplaceChatFileRefs insert: %w", err)
		}
		return nil
	})
}

// DeleteChat deletes the chat with the given ID (cascade deletes messages via
// FK) and, in the same transaction, the agent chat's ADK session (events
// cascade) and runs of that thread: they are keyed by the chat id without an
// FK, so nothing else removes them.
func (s *PGStore) DeleteChat(ctx context.Context, chatID string) error {
	err := pgxutil.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`DELETE FROM adk_sessions WHERE app_name = $1 AND id = $2`, agentChatApp, chatID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM agent_runs WHERE app_name = $1 AND thread_id = $2`, agentChatApp, chatID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM chats WHERE id = $1`, chatID)
		return err
	})
	if err != nil {
		return fmt.Errorf("DeleteChat: %w", err)
	}
	return nil
}

// UpdateChatTitle renames a chat for its owner. One statement checks
// ownership and writes, so there is no read-then-write window. updated_at is
// deliberately left alone: the history list orders by it, and a cosmetic
// rename must not move a chat to the top.
func (s *PGStore) UpdateChatTitle(ctx context.Context, chatID, userID, title string) error {
	ct, err := s.pool.Exec(ctx,
		`UPDATE chats SET title = $3 WHERE id = $1 AND user_id = $2`,
		chatID, userID, title)
	if err != nil {
		return fmt.Errorf("UpdateChatTitle: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("chat %s: %w", chatID, store.ErrNotFound)
	}
	return nil
}

// UpdateChatAgentSelection persists the sticky per-chat team/agent pick.
// NULLs clear it (picker back to Standard).
func (s *PGStore) UpdateChatAgentSelection(ctx context.Context, chatID string, teamID, agentID *string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE chats SET team_id = $2, agent_id = $3, updated_at = now() WHERE id = $1`,
		chatID, teamID, agentID)
	if err != nil {
		return fmt.Errorf("UpdateChatAgentSelection: %w", err)
	}
	return nil
}

// GetChatMessages returns all messages for the given chatID, ordered by created_at ASC.
func (s *PGStore) GetChatMessages(ctx context.Context, chatID string) ([]MessageRow, error) {
	const sql = `
		SELECT id, chat_id, parent_message_id, role, content, sources,
		       is_enhanced, enhanced_query, reasoning, feedback, feedback_comment, feedback_updated_at,
		       verification, trace_id, structured_table, conflicts, team_id, agent_id, created_at
		FROM messages
		WHERE chat_id = $1
		ORDER BY created_at ASC`

	rows, err := pgxutil.QueryRows[messageDBRow](ctx, s.pool, sql, chatID)
	if err != nil {
		return nil, fmt.Errorf("GetChatMessages: %w", err)
	}
	result := make([]MessageRow, len(rows))
	for i, r := range rows {
		mr, err := toMessageRow(r)
		if err != nil {
			return nil, fmt.Errorf("GetChatMessages: %w", err)
		}
		result[i] = mr
	}
	return result, nil
}

// GetMessageAncestors returns the message with the given ID and all its ancestors
// via a recursive CTE, ordered by created_at ASC.
func (s *PGStore) GetMessageAncestors(ctx context.Context, messageID, chatID string) ([]MessageRow, error) {
	const sql = `
		WITH RECURSIVE message_tree AS (
			SELECT id, chat_id, parent_message_id, role, content, sources,
			       is_enhanced, enhanced_query, reasoning, feedback, feedback_comment, feedback_updated_at,
			       verification, trace_id, structured_table, conflicts, team_id, agent_id, created_at
			FROM messages
			WHERE id = $1 AND chat_id = $2
			UNION ALL
			SELECT p.id, p.chat_id, p.parent_message_id, p.role, p.content, p.sources,
			       p.is_enhanced, p.enhanced_query, p.reasoning, p.feedback, p.feedback_comment, p.feedback_updated_at,
			       p.verification, p.trace_id, p.structured_table, p.conflicts, p.team_id, p.agent_id, p.created_at
			FROM messages p
			INNER JOIN message_tree mt ON mt.parent_message_id = p.id
			-- The walk must never leave the chat: a parent_message_id that
			-- points into another chat would otherwise pull a foreign
			-- conversation into the answer history.
			WHERE p.chat_id = $2
		)
		SELECT * FROM message_tree ORDER BY created_at ASC`

	rows, err := pgxutil.QueryRows[messageDBRow](ctx, s.pool, sql, messageID, chatID)
	if err != nil {
		return nil, fmt.Errorf("GetMessageAncestors: %w", err)
	}
	result := make([]MessageRow, len(rows))
	for i, r := range rows {
		mr, err := toMessageRow(r)
		if err != nil {
			return nil, fmt.Errorf("GetMessageAncestors: %w", err)
		}
		result[i] = mr
	}
	return result, nil
}

// MessageInChat reports whether messageID is a message of chatID. A malformed
// id is simply not in the chat.
func (s *PGStore) MessageInChat(ctx context.Context, messageID, chatID string) (bool, error) {
	if _, err := uuid.Parse(messageID); err != nil {
		return false, nil
	}
	var ok bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM messages WHERE id = $1::uuid AND chat_id = $2::uuid)`,
		messageID, chatID).Scan(&ok); err != nil {
		return false, fmt.Errorf("MessageInChat: %w", err)
	}
	return ok, nil
}

// AddMessage inserts a new message and updates the parent chat's updated_at within
// a single transaction. Returns the created message row.
func (s *PGStore) AddMessage(ctx context.Context, p AddMessageParams) (*MessageRow, error) {
	var sourcesJSON []byte
	if p.Sources != nil {
		var err error
		sourcesJSON, err = json.Marshal(p.Sources)
		if err != nil {
			return nil, fmt.Errorf("AddMessage marshal sources: %w", err)
		}
	}
	var structuredTableJSON []byte
	if p.StructuredTable != nil {
		var err error
		structuredTableJSON, err = json.Marshal(p.StructuredTable)
		if err != nil {
			return nil, fmt.Errorf("AddMessage marshal structured_table: %w", err)
		}
	}

	var conflictsJSON []byte
	if len(p.Conflicts) > 0 {
		var err error
		conflictsJSON, err = json.Marshal(p.Conflicts)
		if err != nil {
			return nil, fmt.Errorf("AddMessage marshal conflicts: %w", err)
		}
	}

	const insertSQL = `
		INSERT INTO messages (chat_id, role, content, sources, is_enhanced, enhanced_query, reasoning, parent_message_id, structured_table, conflicts, team_id, agent_id)
		VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7,
		        -- A parent outside this chat (or a missing one) is stored as
		        -- NULL: a foreign parent would let a later ancestor walk start
		        -- in another user's conversation.
		        (SELECT pm.id FROM messages pm WHERE pm.id = $8::uuid AND pm.chat_id = $1::uuid),
		        $9::jsonb, $10::jsonb, $11, $12)
		RETURNING id, chat_id, parent_message_id, role, content, sources,
		          is_enhanced, enhanced_query, reasoning, feedback, feedback_comment, feedback_updated_at,
		          verification, trace_id, structured_table, conflicts, team_id, agent_id, created_at`
	const updateChatSQL = `UPDATE chats SET updated_at = NOW() WHERE id = $1`

	var row *messageDBRow
	err := pgxutil.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		row, err = pgxutil.QueryOne[messageDBRow](ctx, tx, insertSQL,
			p.ChatID, p.Role, p.Content, sourcesJSON, p.IsEnhanced, p.EnhancedQuery, p.Reasoning, p.ParentMessageID, structuredTableJSON, conflictsJSON, p.TeamID, p.AgentID)
		if err != nil {
			return fmt.Errorf("AddMessage insert: %w", err)
		}
		if row == nil {
			return fmt.Errorf("AddMessage: no row returned")
		}
		if _, err := tx.Exec(ctx, updateChatSQL, p.ChatID); err != nil {
			return fmt.Errorf("AddMessage update chat: %w", err)
		}
		// Persist the chunk -> message links used by the online feedback loop.
		// kb_id is resolved from the chat in the same tx. ChunkID is in-memory
		// only (json:"-"), so this is the sole place the link can be recorded.
		ids, positions := chunkRefsFromSources(p.Sources)
		if len(ids) > 0 {
			const insertLinksSQL = `
				INSERT INTO message_chunks (message_id, chunk_id, kb_id, position)
				SELECT $1::uuid, c.chunk_id::uuid,
				       (SELECT kb_id FROM chats WHERE id = $2::uuid), c.ord
				FROM unnest($3::uuid[], $4::int[]) AS c(chunk_id, ord)
				WHERE (SELECT kb_id FROM chats WHERE id = $2::uuid) IS NOT NULL
				ON CONFLICT (message_id, chunk_id) DO NOTHING`
			if _, err := tx.Exec(ctx, insertLinksSQL, row.ID, p.ChatID, ids, positions); err != nil {
				return fmt.Errorf("AddMessage insert chunk links: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	r, err := toMessageRow(*row)
	if err != nil {
		return nil, fmt.Errorf("AddMessage: %w", err)
	}
	return &r, nil
}

// UpdateMessageFeedback writes a feedback event to message_feedback_events
// AND updates the denormalized columns on messages, in one transaction.
//
// feedback semantics:
//   - non-nil "positive" / "negative": writes a rating event, optionally with comment.
//   - nil ("cleared"): writes a "cleared" event with NULL comment, sets
//     messages.feedback to NULL, clears feedback_comment and updates
//     feedback_updated_at.
//
// userID may be empty; in that case the event row stores user_id = NULL.
func (s *PGStore) UpdateMessageFeedback(ctx context.Context, messageID, chatID, userID string, feedback *string, comment *string) error {
	return pgxutil.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Denormalized columns on messages.
		var feedbackArg, commentArg any = nil, nil
		if feedback != nil {
			feedbackArg = *feedback
			if comment != nil && *comment != "" {
				commentArg = *comment
			}
		}

		// Single UPDATE doubles as the auth check: WHERE id=$3 AND chat_id=$4
		// enforces "message belongs to chat" via RowsAffected, avoiding a
		// separate EXISTS round-trip and the TOCTOU window between them. The
		// FK on message_feedback_events.message_id (cascade) makes the
		// downstream INSERT redundant as an existence check; this path is the
		// sole source of truth for the (message, chat) invariant.
		ct, err := tx.Exec(ctx,
			`UPDATE messages
			 SET feedback = $1, feedback_comment = $2, feedback_updated_at = now()
			 WHERE id = $3 AND chat_id = $4`,
			feedbackArg, commentArg, messageID, chatID,
		)
		if err != nil {
			return fmt.Errorf("UpdateMessageFeedback update: %w", err)
		}
		if ct.RowsAffected() == 0 {
			return fmt.Errorf("message %s in chat %s: %w", messageID, chatID, store.ErrNotFound)
		}

		// Determine event rating string. NULL feedback = "cleared".
		var eventRating string
		var eventComment any = nil
		if feedback == nil {
			eventRating = "cleared"
		} else {
			eventRating = *feedback
			if comment != nil && *comment != "" {
				eventComment = *comment
			}
		}

		var userIDArg any = nil
		if userID != "" {
			userIDArg = userID
		}

		if _, err := tx.Exec(ctx,
			`INSERT INTO message_feedback_events (message_id, user_id, rating, comment)
			 VALUES ($1::uuid, $2, $3, $4)`,
			messageID, userIDArg, eventRating, eventComment,
		); err != nil {
			return fmt.Errorf("UpdateMessageFeedback insert event: %w", err)
		}

		return nil
	})
}

// UpdateMessageVerification sets the verification JSON on the message identified by messageID.
// If verification is nil, it returns immediately without touching the database.
func (s *PGStore) UpdateMessageVerification(ctx context.Context, messageID string, verification *MessageVerification) error {
	if verification == nil {
		return nil
	}
	payload, err := json.Marshal(verification)
	if err != nil {
		return fmt.Errorf("UpdateMessageVerification marshal: %w", err)
	}
	ct, err := s.pool.Exec(ctx, `UPDATE messages SET verification = $1::jsonb WHERE id = $2`, payload, messageID)
	if err != nil {
		return fmt.Errorf("UpdateMessageVerification exec: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("message %s: %w", messageID, store.ErrNotFound)
	}
	return nil
}

// UpdateMessageContent overwrites the message body. Used by the AP-A1
// factuality refine gate: when refine produces a corrected answer, the
// original streamed text in the messages row is replaced. Caller is
// responsible for rejecting empty content before calling — refine
// fail-open should keep the original by skipping this call entirely.
func (s *PGStore) UpdateMessageContent(ctx context.Context, messageID string, content string) error {
	ct, err := s.pool.Exec(ctx, `UPDATE messages SET content = $1 WHERE id = $2`, content, messageID)
	if err != nil {
		return fmt.Errorf("UpdateMessageContent exec: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("message %s: %w", messageID, store.ErrNotFound)
	}
	return nil
}

// UpdateMessageTraceID stores the OTel trace id on a saved message. Empty
// traceID is a no-op (matches the case where tracing is disabled).
func (s *PGStore) UpdateMessageTraceID(ctx context.Context, messageID string, traceID string) error {
	if traceID == "" {
		return nil
	}
	ct, err := s.pool.Exec(ctx, `UPDATE messages SET trace_id = $1 WHERE id = $2`, traceID, messageID)
	if err != nil {
		return fmt.Errorf("UpdateMessageTraceID exec: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("UpdateMessageTraceID message=%s: %w", messageID, store.ErrNotFound)
	}
	return nil
}

// GetKBSystemPrompt returns the system_prompt for a KB, or nil if not set.
// A missing KB row maps to (nil, nil) — same as "column is NULL" — because
// callers treat both as "no per-KB override". Mirrors kb.PGStore.GetKBSystemPrompt.
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
// research.ResearchStore helpers — included here because they operate on
// the same chats/messages tables and share the internal scan structs.
// ---------------------------------------------------------------------------

// CreateResearchSession inserts a new chat row with the given type (e.g.
// "research") and returns the created row.
func (s *PGStore) CreateResearchSession(ctx context.Context, kbID, userID, goal, sessionType string) (*ChatRow, error) {
	const sql = `
		INSERT INTO chats (kb_id, user_id, title, type)
		VALUES ($1, $2, $3, $4)
		RETURNING id, COALESCE(kb_id::text, '') AS kb_id, user_id, title, type, team_id, agent_id, created_at, updated_at`

	row, err := pgxutil.QueryOne[chatDBRow](ctx, s.pool, sql, kbID, userID, goal, sessionType)
	if err != nil {
		return nil, fmt.Errorf("CreateResearchSession: %w", err)
	}
	if row == nil {
		return nil, fmt.Errorf("CreateResearchSession: no row returned")
	}
	r := toChatRow(*row)
	return &r, nil
}

// SaveResearchResults saves the final research report and findings to the DB
// in a single transaction:
//   - inserts a "user" message containing the goal
//   - inserts an "assistant" message containing the report (with findings as sources)
//   - bumps the chat's updated_at timestamp
func (s *PGStore) SaveResearchResults(ctx context.Context, chatID, goal, report string, findings any) error {
	var sourcesJSON []byte
	if findings != nil {
		var err error
		sourcesJSON, err = json.Marshal(findings)
		if err != nil {
			return fmt.Errorf("SaveResearchResults marshal findings: %w", err)
		}
	}

	return pgxutil.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		// User message (the research goal).
		if _, err := tx.Exec(ctx,
			`INSERT INTO messages (chat_id, role, content) VALUES ($1, 'user', $2)`,
			chatID, goal,
		); err != nil {
			return fmt.Errorf("SaveResearchResults insert user msg: %w", err)
		}

		// AI message (the report). Role must be 'ai' — not 'assistant' — to
		// match the convention used by the chat handler (http_send.go) and
		// expected by the frontend when loading saved research sessions.
		if _, err := tx.Exec(ctx,
			`INSERT INTO messages (chat_id, role, content, sources) VALUES ($1, 'ai', $2, $3::jsonb)`,
			chatID, report, sourcesJSON,
		); err != nil {
			return fmt.Errorf("SaveResearchResults insert assistant msg: %w", err)
		}

		// Bump the chat's updated_at.
		if _, err := tx.Exec(ctx,
			`UPDATE chats SET updated_at = NOW() WHERE id = $1`,
			chatID,
		); err != nil {
			return fmt.Errorf("SaveResearchResults update chat: %w", err)
		}

		return nil
	})
}

// GetSiteConfigValue returns the value for a single site_configs key, or nil
// if the key does not exist. Included here to satisfy research.ResearchStore.
// Served from the in-memory whole-table snapshot — see loadSiteConfigSnapshot.
func (s *PGStore) GetSiteConfigValue(ctx context.Context, key string) (*string, error) {
	snap, err := s.loadSiteConfigSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	v, ok := snap[key]
	if !ok {
		return nil, nil
	}
	return v, nil
}

// GetSiteConfigValues returns the values for the requested site_configs keys.
// Keys not present in the table are absent from the returned map (rather than
// mapped to nil) so callers can distinguish "missing" from "explicitly NULL".
// When keys is empty, returns an empty map and nil error.
// Served from the in-memory whole-table snapshot.
func (s *PGStore) GetSiteConfigValues(ctx context.Context, keys []string) (map[string]*string, error) {
	out := make(map[string]*string, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	snap, err := s.loadSiteConfigSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	for _, k := range keys {
		if v, ok := snap[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}

// loadSiteConfigSnapshot returns the cached whole-table snapshot when fresh,
// otherwise re-fetches every row in one round-trip and seeds the cache.
// Concurrent refreshers coalesce via singleflight so a TTL expiry under load
// triggers exactly one DB query per process.
func (s *PGStore) loadSiteConfigSnapshot(ctx context.Context) (map[string]*string, error) {
	if e := s.siteConfigCache.Load(); e != nil && time.Now().Before(e.expiresAt) {
		return e.values, nil
	}
	v, err, _ := s.siteConfigSF.Do("sitecfg", func() (any, error) {
		if e := s.siteConfigCache.Load(); e != nil && time.Now().Before(e.expiresAt) {
			return e.values, nil
		}
		m, err := s.fetchAllSiteConfigs(ctx)
		if err != nil {
			return nil, err
		}
		s.siteConfigCache.Store(&siteConfigStoreCacheEntry{
			values:    m,
			expiresAt: time.Now().Add(siteConfigStoreCacheTTL),
		})
		return m, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(map[string]*string), nil
}

// fetchAllSiteConfigs reads every row of site_configs in a single round-trip.
// site_configs has bounded cardinality (admin-configured feature flags), so
// fetching the whole table is cheaper than caching per-key entries with the
// associated bookkeeping.
func (s *PGStore) fetchAllSiteConfigs(ctx context.Context) (map[string]*string, error) {
	const sql = `SELECT key, value FROM site_configs`
	rows, err := s.pool.Query(ctx, sql)
	if err != nil {
		return nil, fmt.Errorf("fetchAllSiteConfigs: %w", err)
	}
	defer rows.Close()

	out := make(map[string]*string, 64)
	for rows.Next() {
		var k string
		var v *string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("fetchAllSiteConfigs scan: %w", err)
		}
		out[k] = v
	}
	return out, rows.Err()
}

// StarterContext returns the KB's name and its newest ingested documents
// (newest first, at most limit): status completed or partial, and never a
// file the ingest-time prompt-injection screen flagged (injection_flag) —
// their text would otherwise be fed to the starter-question model.
func (s *PGStore) StarterContext(ctx context.Context, kbID string, limit int) (StarterSource, error) {
	var src StarterSource
	if err := s.pool.QueryRow(ctx, `SELECT name FROM knowledge_bases WHERE id = $1`, kbID).Scan(&src.KBName); err != nil {
		return StarterSource{}, fmt.Errorf("StarterContext: kb name: %w", err)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, name FROM files
		 WHERE kb_id = $1
		   AND status IN ('completed', 'partial')
		   AND NOT injection_flag
		 ORDER BY created_at DESC, id
		 LIMIT $2`, kbID, limit)
	if err != nil {
		return StarterSource{}, fmt.Errorf("StarterContext: files: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var f StarterFile
		if err := rows.Scan(&f.ID, &f.Name); err != nil {
			return StarterSource{}, fmt.Errorf("StarterContext: scan file: %w", err)
		}
		src.Files = append(src.Files, f)
	}
	if err := rows.Err(); err != nil {
		return StarterSource{}, fmt.Errorf("StarterContext: iterate files: %w", err)
	}
	return src, nil
}
