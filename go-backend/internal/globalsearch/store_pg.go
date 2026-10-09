package globalsearch

import (
	"context"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/kbaccess"
	"github.com/justrag/go-backend/internal/pgxutil"
)

// descriptionSnippetLen is the length, in characters, of TopicHit.Description.
const descriptionSnippetLen = 160

// visibleKBsCTE is kbaccess.VisibleKBsCTE: the SQL mirror of
// kbaccess.EffectiveRole, which every query below uses as its visibility
// filter. It lives in internal/kbaccess, next to the ladder it mirrors, so
// there is one place to keep in sync; see its doc comment for the rules, the
// parameter layout ($1..$3) and the parity test.
const visibleKBsCTE = kbaccess.VisibleKBsCTE

// scopeSQL resolves the caller's role on the scoping KB. No row: the KB does
// not exist. A row with a NULL role: it exists but is invisible.
const scopeSQL = `WITH ` + visibleKBsCTE + `
	SELECT v.role FROM visible_kbs v`

// Parameters shared by topicsSQL and sourcesSQL: $1..$3 visibleKBsCTE's,
// $4 / $5 the LIKE-escaped substring / prefix patterns, $6 the group limit,
// $7 the raw query text for the trigram operators (searchMatch). Both run in
// a transaction that has called setFuzzyThreshold first.
//
// Visibility is untouched by fuzzy matching: every row still comes from
// visible_kbs filtered on `v.role IS NOT NULL`, and the match predicates only
// narrow that set further.

// topicsSQL matches name, description and header_text — the same three
// columns GET /api/kb/catalog searches, for the same reason: description has
// no editor in the UI, so a search that skipped header_text would look
// broken. Only the name is matched fuzzily: description and header_text are
// long free text, where trigram similarity to a short query is noise, so they
// match by literal substring only.
//
// Rank (r.rank), and hence order:
//
//	0 prefix     name starts with q
//	1 substring  name contains q
//	2 substring  description or header_text contains q
//	3 fuzzy      only the trigram arm matched; ordered by similarity desc
//
// ties by case-folded name, then id, so the order is stable.
var topicsSQL = `WITH ` + visibleKBsCTE + `
	SELECT v.id::text AS id, v.name, v.visibility, v.role,
	       CASE r.rank WHEN 0 THEN '` + MatchPrefix + `' WHEN 3 THEN '` + MatchFuzzy + `'
	                   ELSE '` + MatchSubstring + `' END AS match,
	       CASE
	         WHEN d.snip IS NULL THEN NULL
	         WHEN char_length(d.snip) > ` + strconv.Itoa(descriptionSnippetLen) + `
	           THEN left(d.snip, ` + strconv.Itoa(descriptionSnippetLen-1) + `) || '…'
	         ELSE d.snip
	       END AS description
	FROM visible_kbs v
	CROSS JOIN LATERAL (
		SELECT NULLIF(btrim(regexp_replace(
		         COALESCE(NULLIF(btrim(v.description), ''), NULLIF(btrim(v.header_text), ''), ''),
		         '\s+', ' ', 'g')), '') AS snip
	) d
	CROSS JOIN LATERAL (
		SELECT CASE
		         WHEN ` + searchMatch.hasPrefix("v.name") + ` THEN 0
		         WHEN ` + searchMatch.contains("v.name") + ` THEN 1
		         WHEN ` + searchMatch.contains("v.description") + `
		           OR ` + searchMatch.contains("v.header_text") + ` THEN 2
		         ELSE 3
		       END AS rank
	) r
	WHERE v.role IS NOT NULL
	  AND (` + searchMatch.matches("v.name") + `
	       OR ` + searchMatch.contains("v.description") + `
	       OR ` + searchMatch.contains("v.header_text") + `)
	ORDER BY r.rank,
	         CASE WHEN r.rank = 3 THEN ` + searchMatch.similarity("v.name") + ` END DESC NULLS LAST,
	         lower(v.name), v.id
	LIMIT $6`

// sourcesSQL matches files by name in every visible KB, both ways.
//
// Rank: 0 prefix, 1 substring, 2 fuzzy (by similarity desc); ties by
// case-folded name, then id. Both arms of the WHERE are served by
// files_name_trgm_idx (migration 0084) — see TestSearchQueriesUseTrigramIndexes.
var sourcesSQL = `WITH ` + visibleKBsCTE + `
	SELECT f.id::text AS id, f.name, f.type, v.id::text AS kb_id, v.name AS kb_name,
	       CASE r.rank WHEN 0 THEN '` + MatchPrefix + `' WHEN 1 THEN '` + MatchSubstring + `'
	                   ELSE '` + MatchFuzzy + `' END AS match
	FROM files f
	JOIN visible_kbs v ON v.id = f.kb_id
	CROSS JOIN LATERAL (
		SELECT CASE
		         WHEN ` + searchMatch.hasPrefix("f.name") + ` THEN 0
		         WHEN ` + searchMatch.contains("f.name") + ` THEN 1
		         ELSE 2
		       END AS rank
	) r
	WHERE v.role IS NOT NULL
	  AND ` + searchMatch.matches("f.name") + `
	ORDER BY r.rank,
	         CASE WHEN r.rank = 2 THEN ` + searchMatch.similarity("f.name") + ` END DESC NULLS LAST,
	         lower(f.name), f.id
	LIMIT $6`

// chatsSQL matches the CALLER'S OWN chats by title, with the same matching
// and ranking as file names (match.go): 0 prefix, 1 substring, 2 fuzzy by
// similarity desc. Ties go to the most recently updated chat — many chats
// share a title like "New Chat", and the newest is the likeliest target.
//
// Privacy: `c.user_id = $1` is unconditional. It is ANDed with the
// visibility filter, not ORed with any role, so no system role (superadmin
// included) sees another user's chat, and the caller's own chat disappears
// once its topic is no longer visible to them (e.g. membership removed).
// Same parameter layout as sourcesSQL (searchMatch).
var chatsSQL = `WITH ` + visibleKBsCTE + `
	SELECT c.id::text AS id, c.title, c.type, v.id::text AS kb_id, v.name AS kb_name, c.updated_at,
	       CASE r.rank WHEN 0 THEN '` + MatchPrefix + `' WHEN 1 THEN '` + MatchSubstring + `'
	                   ELSE '` + MatchFuzzy + `' END AS match
	FROM chats c
	JOIN visible_kbs v ON v.id = c.kb_id
	CROSS JOIN LATERAL (
		SELECT CASE
		         WHEN ` + searchMatch.hasPrefix("c.title") + ` THEN 0
		         WHEN ` + searchMatch.contains("c.title") + ` THEN 1
		         ELSE 2
		       END AS rank
	) r
	WHERE v.role IS NOT NULL
	  AND c.user_id = $1::uuid
	  AND ` + searchMatch.matches("c.title") + `
	ORDER BY r.rank,
	         CASE WHEN r.rank = 2 THEN ` + searchMatch.similarity("c.title") + ` END DESC NULLS LAST,
	         c.updated_at DESC, c.id
	LIMIT $6`

// messagesSQL searches the content of the CALLER'S OWN messages with Postgres
// full text (fulltext.go) and returns at most one hit per chat: the
// best-ranked message by ts_rank, ties to the newest. Hits are then ordered
// by that rank, newest first on ties. Same privacy rule as chatsSQL.
//
// Parameters (its own layout — full text needs neither LIKE pattern):
// $1..$3 visibleKBsCTE's, $4 the raw query text, $5 the limit, $6 the
// ts_headline options (snippetOptions).
//
// query_ts is MATERIALIZED so the tsquery is built once; referenced through
// scalar subqueries it becomes an InitPlan parameter, which the planner can
// use as the Index Cond of messages_content_fts_idx (migration 0085).
// ts_headline — the expensive part — runs only on the final, limited rows,
// and structurally so: ORDER BY + LIMIT sit in a subquery over best (a
// subquery with LIMIT is never flattened into its parent), and ts_headline is
// computed in the SELECT list above it. The outer ORDER BY repeats the order,
// because a subquery's order is not guaranteed to survive; it sorts at most
// $5 rows. TestSnippetIsComputedAboveTheLimit pins this with EXPLAIN VERBOSE.
// The snippet is cut from the content with both highlight delimiters removed
// first, so every delimiter in a snippet is one ts_headline put there.
var messagesSQL = `WITH ` + visibleKBsCTE + `,
	query_ts AS MATERIALIZED (SELECT ` + prefixTSQuery("$4") + ` AS q),
	best AS (
		SELECT DISTINCT ON (m.chat_id)
		       m.id, m.chat_id, c.title AS chat_title, c.type AS chat_type,
		       v.id AS kb_id, v.name AS kb_name, m.role, m.content, m.created_at,
		       ts_rank(` + messageTSVector("m.content") + `, (SELECT q FROM query_ts)) AS rank
		FROM messages m
		JOIN chats c ON c.id = m.chat_id
		JOIN visible_kbs v ON v.id = c.kb_id
		WHERE v.role IS NOT NULL
		  AND c.user_id = $1::uuid
		  AND ` + messageTSVector("m.content") + ` @@ (SELECT q FROM query_ts)
		ORDER BY m.chat_id, rank DESC, m.created_at DESC, m.id
	)
	SELECT t.id::text AS id, t.chat_id::text AS chat_id, t.chat_title, t.chat_type,
	       t.kb_id::text AS kb_id, t.kb_name, t.role, t.created_at,
	       btrim(regexp_replace(
	         ts_headline('` + ftsConfig + `',
	                     left(translate(t.content, '` + SnippetStart + SnippetEnd + `', ''), ` +
	strconv.Itoa(maxIndexedChars) + `),
	                     (SELECT q FROM query_ts), $6),
	         '\s+', ' ', 'g')) AS snippet
	FROM (
		SELECT b.* FROM best b
		ORDER BY b.rank DESC, b.created_at DESC, b.id
		LIMIT $5
	) t
	ORDER BY t.rank DESC, t.created_at DESC, t.id`

// PGStore is the Postgres-backed Store.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewStore creates a PGStore over the main database pool.
func NewStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

// Compile-time interface assertion.
var _ Store = (*PGStore)(nil)

type scopeRow struct {
	Role *string `db:"role"`
}

// Search implements Store: the scope check (when q.KBID is set), then all
// four groups — topics, sources, chats, messages. The store trusts q to be
// validated (see Query); it only guards Limit against a non-positive value,
// because LIMIT 0 would silently return nothing.
//
// All queries run in one read-only REPEATABLE READ transaction. Two reasons:
// setFuzzyThreshold is transaction-local, so it must share the transaction
// with the queries it tunes; and under REPEATABLE READ every statement reads
// the transaction's one snapshot, so the scope check and the four groups see
// the same state of memberships, topics and chats (under the default READ
// COMMITTED each statement would take its own snapshot). Read-only, it can
// neither block writers nor fail with a serialization error.
func (s *PGStore) Search(ctx context.Context, caller Caller, q Query) (*Response, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}

	// nil, not "", so $3::uuid IS NULL selects every KB.
	var kbID any
	if q.KBID != "" {
		kbID = q.KBID
	}
	contains, prefix, raw := matchArgs(q.Text)

	var topics []TopicHit
	var sources []SourceHit
	var chats []ChatHit
	var messages []MessageHit
	err := pgxutil.WithTxOptions(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if kbID != nil {
			scope, err := pgxutil.QueryOne[scopeRow](ctx, tx, scopeSQL, caller.UserID, caller.SysRole, kbID)
			if err != nil {
				return fmt.Errorf("search scope: %w", err)
			}
			if scope == nil || scope.Role == nil {
				return ErrNotFound
			}
		}
		if err := setFuzzyThreshold(ctx, tx); err != nil {
			return err
		}
		var err error
		topics, err = pgxutil.QueryRows[TopicHit](ctx, tx, topicsSQL,
			caller.UserID, caller.SysRole, kbID, contains, prefix, limit, raw)
		if err != nil {
			return fmt.Errorf("search topics: %w", err)
		}
		sources, err = pgxutil.QueryRows[SourceHit](ctx, tx, sourcesSQL,
			caller.UserID, caller.SysRole, kbID, contains, prefix, limit, raw)
		if err != nil {
			return fmt.Errorf("search sources: %w", err)
		}
		chats, err = pgxutil.QueryRows[ChatHit](ctx, tx, chatsSQL,
			caller.UserID, caller.SysRole, kbID, contains, prefix, limit, raw)
		if err != nil {
			return fmt.Errorf("search chats: %w", err)
		}
		messages, err = pgxutil.QueryRows[MessageHit](ctx, tx, messagesSQL,
			caller.UserID, caller.SysRole, kbID, raw, limit, snippetOptions)
		if err != nil {
			return fmt.Errorf("search messages: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	resp := &Response{Query: q.Text, Topics: topics, Sources: sources, Chats: chats, Messages: messages}
	if q.KBID != "" {
		id := q.KBID
		resp.KBID = &id
	}
	return resp, nil
}
