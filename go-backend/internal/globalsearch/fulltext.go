package globalsearch

import "strconv"

// Full-text matching for message content. Chat titles
// are matched like topic and file names (match.go); message bodies are long
// free text, where trigram similarity is noise, so they use Postgres full
// text search instead.

// ftsConfig is the text search configuration for message content: 'simple',
// which only lowercases. Decided over 'german' because the corpus is mixed
// German and English, and chat content is full of code, identifiers and
// product names: German stemming would serve the German words but mangle the
// English words and the code terms, which then no longer match what the user
// types. Prefix matching (prefixTSQuery) recovers most of what stemming would
// add for German inflections ("Prüfung" finds "Prüfungen"). Switching later
// needs a new index (migration) and a change here, since the query must
// repeat the index expression exactly.
const ftsConfig = "simple"

// maxIndexedChars bounds the text that is turned into a tsvector. A tsvector
// is limited to 1 MB, and to_tsvector ERRORS beyond that ("string is too long
// for tsvector") — inside an expression index that error would abort the
// INSERT of the message, i.e. break the chat write path, and fail the
// concurrent index build on any existing oversize row. Measured on Postgres
// 18.6: 30 000 distinct 32-character tokens (~1 MB of text) already exceed
// it (TestOversizeMessageStillInserts checks the premise). The worst case is about 2.7 tsvector bytes per input
// character (two-letter distinct tokens), so 100 000 characters stay far
// below the limit. User messages are capped at 32 000 bytes
// (chat.MaxMessageLength); only text past the first 100 000 characters of an
// unusually long AI message is not searchable.
const maxIndexedChars = 100000

// messageTSVector renders the tsvector expression for a message content
// column. It MUST stay textually identical to the index expression of
// migration 0085 (messages_content_fts_idx), or the planner cannot use the
// index — TestMessageSearchUsesFullTextIndex pins that.
func messageTSVector(col string) string {
	return `to_tsvector('` + ftsConfig + `', left(` + col + `, ` + strconv.Itoa(maxIndexedChars) + `))`
}

// prefixTSQuery renders a tsquery built from the raw query text in param,
// safely and entirely in SQL:
//
//  1. to_tsvector(ftsConfig, param) tokenises the input with the same parser
//     that built the index, so terms split exactly as in the content;
//  2. each resulting lexeme is quoted as a tsquery literal (single quotes
//     are doubled, backslashes escaped) and given the prefix marker :* for
//     as-you-type matching;
//  3. the terms are ANDed.
//
// The user's text is only ever a bind parameter and a to_tsvector input. It
// never reaches to_tsquery's operator syntax, so `a & !b | (c)` searches for
// a, b and c rather than being evaluated.
//
// Lexemes shorter than minLexemeLen are dropped before the query is built:
// a one- or two-character prefix term ('a':*, 'ab':*) expands to a huge
// share of the GIN index's entries, for a term that narrows next to nothing.
// "abc de" therefore searches only abc:*. Input with no lexeme left (e.g.
// "!!" or "ab cd") yields NULL, which matches nothing and raises no error.
func prefixTSQuery(param string) string {
	return `(SELECT to_tsquery('` + ftsConfig + `', string_agg(` +
		`'''' || replace(replace(l.lexeme, '\', '\\'), '''', '''''') || ''':*', ' & '))` +
		` FROM unnest(tsvector_to_array(to_tsvector('` + ftsConfig + `', ` + param + `))) AS l(lexeme)` +
		` WHERE char_length(l.lexeme) >= ` + strconv.Itoa(minLexemeLen) + `)`
}

// minLexemeLen is the shortest query term that becomes a prefix term. Equal
// to MinQueryLen, so a single-word query that passes the handler's length
// check is never dropped here.
const minLexemeLen = 3

// Snippet highlight delimiters. MessageHit.Snippet is plain text: the
// matched words are wrapped in these two Unicode private-use characters,
// which carry no meaning in HTML, Markdown or any other markup and are
// stripped from the content before the snippet is cut, so every occurrence
// in a snippet is one of ours. A client splits on them and renders each part
// as a text node — never as HTML.
const (
	SnippetStart = ""
	SnippetEnd   = ""
)

// snippetOptions are the ts_headline options: one fragment of 8-20 words
// around the best match.
const snippetOptions = `StartSel=` + SnippetStart + `, StopSel=` + SnippetEnd +
	`, MaxWords=20, MinWords=8, ShortWord=2, MaxFragments=1`
