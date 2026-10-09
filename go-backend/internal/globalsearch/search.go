// Package globalsearch serves GET /api/search, the shell header's global search:
// one request, one response with one array per result group (topics,
// sources, chats and messages), optionally scoped to a single topic.
//
// The load-bearing piece is not the matching but the visibility rule. A
// search result must never reveal a topic the caller could not open, so every
// group is filtered by kbaccess.VisibleKBsCTE, the SQL rendering of
// kbaccess.EffectiveRole's ladder, rule for rule, kept next to it in
// internal/kbaccess. The listing queries in
// internal/kb and internal/kbsubs each apply a *different subset* of that
// ladder (they answer "what is on my overview", not "what may I open"), so
// none of them could be reused here. The integration test in
// store_pg_integration_test.go runs both over the same fixture matrix and
// asserts they agree on every row.
//
// Chats and messages add a second, stricter rule on top: they are private to
// their owner everywhere else in the app (chat.GetChats filters user_id,
// GetMessages 404s for anyone else), so both groups return ONLY the caller's
// own chats — and only in topics VisibleKBsCTE still lets the caller open.
// No role widens this, superadmin included.
package globalsearch

import (
	"context"
	"errors"
	"time"
)

// Group limits. The limit is per group, not per response: a search for a
// common word should not let fifty file hits push every topic hit out.
const (
	DefaultLimit = 5
	MaxLimit     = 20
	// MinQueryLen is counted in runes after trimming, so "äöü" is a valid
	// three-character query even though it is six bytes. Three, not two: a
	// two-character ILIKE pattern contains no complete trigram, so the
	// pg_trgm indexes of migration 0084 cannot narrow it (measured on
	// Postgres 18.6, 50 000 rows: '%ab%' is planned as a sequential scan,
	// '%abc%' as a bitmap index scan), and the message full-text search
	// drops lexemes shorter than three characters anyway (minLexemeLen).
	MinQueryLen = 3
	// MaxQueryLen bounds the ILIKE pattern. A header search box never needs
	// more, and without a bound the pattern size is limited only by the
	// server's header size limit.
	MaxQueryLen = 200
)

// ErrNotFound means the scoping topic (kb_id) does not exist or is not
// visible to the caller. The two are deliberately indistinguishable: the
// handler answers 404 either way, so the route cannot be used to confirm that
// a hidden topic exists.
var ErrNotFound = errors.New("search: knowledge base not found")

// Caller is who is searching. SysRole is the system role from the caller's
// auth claims — the same value RequireKBRole hands to kbaccess.EffectiveRole —
// never a column read back from the users table.
type Caller struct {
	UserID  string
	SysRole string
}

// Query is one validated search request. Text is already trimmed and within
// [MinQueryLen, MaxQueryLen]; Limit within [1, MaxLimit]; KBID empty for a
// global search or a syntactically valid UUID.
type Query struct {
	Text  string
	KBID  string
	Limit int
}

// Response is the body of GET /api/search. One array per group, all four
// always present (the handler turns a nil group into [], never null).
type Response struct {
	Query    string       `json:"query"`
	KBID     *string      `json:"kbId"`
	Topics   []TopicHit   `json:"topics"`
	Sources  []SourceHit  `json:"sources"`
	Chats    []ChatHit    `json:"chats"`
	Messages []MessageHit `json:"messages"`
}

// TopicHit is one knowledge base the caller may open.
type TopicHit struct {
	ID   string `json:"id"   db:"id"`
	Name string `json:"name" db:"name"`
	// Description is the head of knowledge_bases.description, falling back to
	// header_text (the same fallback GET /api/kb/catalog uses: description
	// has no editor in the UI, header_text is what admins actually write),
	// whitespace-collapsed and cut at descriptionSnippetLen characters. null
	// when both are empty.
	Description *string `json:"description" db:"description"`
	Visibility  string  `json:"visibility"  db:"visibility"`
	// Role is the caller's effective KB role on this topic — exactly what
	// kbaccess.EffectiveRole resolves to: view, edit, admin or owner.
	Role string `json:"role" db:"role"`
	// Match is the best tier this hit reached: MatchPrefix (name starts with
	// q), MatchSubstring (name, description or header text contains q) or
	// MatchFuzzy (name is trigram-similar to q only). See match.go.
	Match string `json:"match" db:"match"`
}

// SourceHit is one file in a topic the caller may open.
type SourceHit struct {
	ID     string `json:"id"     db:"id"`
	Name   string `json:"name"   db:"name"`
	Type   string `json:"type"   db:"type"`
	KBID   string `json:"kbId"   db:"kb_id"`
	KBName string `json:"kbName" db:"kb_name"`
	// Match is the best tier this hit reached on the file name: MatchPrefix,
	// MatchSubstring or MatchFuzzy. See match.go.
	Match string `json:"match" db:"match"`
}

// ChatHit is one of the CALLER'S OWN chats whose title matches, in a topic
// the caller can still open.
type ChatHit struct {
	ID    string `json:"id"    db:"id"`
	Title string `json:"title" db:"title"`
	// Type is chats.type as stored: chat, research or academic-research
	// (hyphen; internal/academic writes it).
	Type      string    `json:"type"      db:"type"`
	KBID      string    `json:"kbId"      db:"kb_id"`
	KBName    string    `json:"kbName"    db:"kb_name"`
	UpdatedAt time.Time `json:"updatedAt" db:"updated_at"`
	// Match is the best tier the title reached: MatchPrefix, MatchSubstring
	// or MatchFuzzy — the same matching as topic and file names (match.go).
	Match string `json:"match" db:"match"`
}

// MessageHit is the best-ranked matching message of one of the CALLER'S OWN
// chats: at most one hit per chat, so one long conversation cannot fill the
// dropdown.
type MessageHit struct {
	ID        string `json:"id"        db:"id"`
	ChatID    string `json:"chatId"    db:"chat_id"`
	ChatTitle string `json:"chatTitle" db:"chat_title"`
	ChatType  string `json:"chatType"  db:"chat_type"`
	KBID      string `json:"kbId"      db:"kb_id"`
	KBName    string `json:"kbName"    db:"kb_name"`
	// Role is messages.role as stored: user or ai (internal/chat writes AI
	// turns as 'ai', not 'assistant').
	Role string `json:"role" db:"role"`
	// Snippet is PLAIN TEXT around the match, whitespace-collapsed, with each
	// matched word wrapped in SnippetStart ... SnippetEnd (U+E000 / U+E001,
	// see fulltext.go). Never render it as HTML.
	Snippet   string    `json:"snippet"   db:"snippet"`
	CreatedAt time.Time `json:"createdAt" db:"created_at"`
}

// Store runs the searches. PGStore is its only implementation.
type Store interface {
	// Search returns all four groups for q: topics, sources, chats and
	// messages. When q.KBID is set and the caller cannot see that topic, it
	// returns ErrNotFound.
	Search(ctx context.Context, caller Caller, q Query) (*Response, error)
}
