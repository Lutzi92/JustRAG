package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/parser"
	"github.com/justrag/go-backend/internal/prompts"
	"github.com/justrag/go-backend/internal/splitter"
	"github.com/justrag/go-backend/internal/vector"
)

// libraryChunkTokens is the per-chunk budget library pages are pre-split to,
// so a single huge page cannot dominate a map group and TruncateChunksToFit
// never has a whole page to drop (P3-R3).
const libraryChunkTokens = 1500

// LibraryFile is one selected library file with its parsed text.
type LibraryFile struct {
	UserFileID string
	Name       string
	Parsed     *parser.ParseResult
}

// LibraryContextParams is the input of BuildLibraryContext.
type LibraryContextParams struct {
	Files           []LibraryFile
	Query           string
	Language        string
	CurrentDateLine string
	Emit            func(map[string]any) // trajectory events, may be nil
}

// ErrLibraryNoText means none of the selected files yielded any text.
var ErrLibraryNoText = errors.New("library: the selected files contain no text")

// ErrLibraryTooLarge reports that the selected files exceed even the
// long-context budget. Nothing is silently truncated. AtLeast marks a
// rejection by the cheap pre-check, whose Tokens is a lower bound rather than
// the exact count.
type ErrLibraryTooLarge struct {
	Tokens, Max int
	AtLeast     bool
}

func (e *ErrLibraryTooLarge) Error() string {
	if e.AtLeast {
		return fmt.Sprintf("selected files are too large for one chat turn (at least %d tokens, maximum %d)", e.Tokens, e.Max)
	}
	return fmt.Sprintf("selected files are too large for one chat turn (%d tokens, maximum %d)", e.Tokens, e.Max)
}

// Seams over the splitter so a test can prove the over-budget pre-check
// rejects without tokenizing or splitting anything.
var (
	libraryCountTokens = splitter.CountTokens
	librarySplit       = splitter.Split
	// libraryBoundSound reports whether the cl100k tokenizer is loaded: the
	// lower bound below only holds against real BPE counts, not against
	// CountTokens' runes/4 fallback, so the pre-check is skipped without it.
	libraryBoundSound = sync.OnceValue(func() bool { return splitter.EncodeBPE("a") != nil })
)

// BuildLibraryContext turns the selected files' parsed text into a
// *ChatContext for a KB-less chat turn: full text when it fits
// chat_library_fulltext_max_tokens, the map_reduce long-context consumer when
// it fits chat_longcontext_max_tokens, else *ErrLibraryTooLarge.
func BuildLibraryContext(ctx context.Context, resolver *ai.ConfigResolver, cfg SiteConfigReader, p LibraryContextParams) (*ChatContext, error) {
	return buildLibraryContextWith(ctx, resolver, ai.ExtractLongContextFindings, cfg, p)
}

// buildLibraryContextWith is the testable core; extract may be nil when the
// map_reduce path is not expected.
func buildLibraryContextWith(ctx context.Context, resolver *ai.ConfigResolver, extract extractFindingsFn, cfg SiteConfigReader, p LibraryContextParams) (*ChatContext, error) {
	fullMax, lcMax := ChatLibraryFulltextMaxTokens(ctx, cfg), ChatLongContextMaxTokens(ctx, cfg)
	// The largest selection any tier accepts: the full-text budget may be
	// configured above the long-context one (then there is no map_reduce tier).
	limit := max(fullMax, lcMax)
	// Cheap pre-check before any BPE tokenization or recursive split: up to
	// 20 files of several million characters each would otherwise be fully
	// tokenized on every turn just to reach the same 400.
	if libraryBoundSound() {
		if bound := libraryTokenLowerBound(p.Files); bound > limit {
			observability.RecordLibraryChatTurn("too_large")
			return nil, &ErrLibraryTooLarge{Tokens: bound, Max: limit, AtLeast: true}
		}
	}
	chunks, skipped, total := libraryChunks(p.Files)
	if len(chunks) == 0 {
		return nil, ErrLibraryNoText
	}
	if len(skipped) > 0 {
		logctx.From(ctx).Warn("library.files_without_text", "files", skipped)
		if p.Emit != nil {
			p.Emit(map[string]any{"stage": "library_files_skipped", "files": skipped})
		}
	}

	notice := prompts.LibraryNotice(p.Language)
	var cc *ChatContext
	var err error
	switch {
	case total <= fullMax:
		observability.RecordLibraryChatTurn("fulltext")
		sources, text := buildChatSourcesAndContext(chunks)
		cc = &ChatContext{
			// Notice first, like the map_reduce path (KbSystemPrompt leads there).
			SystemPrompt: notice + "\n\n" + prompts.ChatSystemPromptWithDate(p.Language, p.CurrentDateLine) +
				"\n\nCONTEXT:\n" + text,
			Sources:     sources,
			Context:     text,
			FinalChunks: chunks,
		}
	case total <= lcMax:
		lp := resolveLongContextParams(ctx, cfg, LongContextParams{
			Query:           p.Query,
			Language:        p.Language,
			KbSystemPrompt:  notice,
			CurrentDateLine: p.CurrentDateLine,
			Mode:            LongContextModeMapReduce,
			MaxTokens:       lcMax,
			Emit:            p.Emit,
		})
		cc, err = consumeLongContextWith(ctx, resolver, extract, lp, chunks)
		if err != nil {
			return nil, err
		}
		observability.RecordLibraryChatTurn("map_reduce")
	default:
		observability.RecordLibraryChatTurn("too_large")
		return nil, &ErrLibraryTooLarge{Tokens: total, Max: limit}
	}

	// The owner rides on the chunk (FileID) so it survives any reordering the
	// consumer applies (e.g. the map_empty sandwich degrade).
	for i := range cc.Sources {
		cc.Sources[i].UserFileID = cc.Sources[i].FileID
		cc.Sources[i].FileID = ""
	}
	for i := range cc.FinalChunks {
		cc.FinalChunks[i].FileID = ""
	}
	return cc, nil
}

// libraryChunks renders the files as synthetic page chunks in selection
// order (descending score keeps that order), splitting pages above
// libraryChunkTokens. Each chunk carries its UserFileID in FileID (the caller
// moves it to ChatSource.UserFileID); skipped names files that contributed no
// text; total is the summed token count of the chunk bodies.
func libraryChunks(files []LibraryFile) (chunks []vector.SearchChunk, skipped []string, total int) {
	cfg := splitter.DefaultConfig()
	cfg.ChunkSize = libraryChunkTokens
	cfg.ChunkOverlap = 0

	add := func(f LibraryFile, page int, text string) bool {
		text = strings.TrimSpace(text)
		if text == "" {
			return false
		}
		parts := []string{text}
		if libraryCountTokens(text) > libraryChunkTokens {
			parts = librarySplit(text, cfg)
		}
		for _, part := range parts {
			c := vector.SearchChunk{Content: part, FileName: f.Name, FileID: f.UserFileID}
			if page > 0 {
				c.Metadata = map[string]any{"pages": []int{page}}
			}
			chunks = append(chunks, c)
			total += libraryCountTokens(part)
		}
		return true
	}
	for _, f := range files {
		got := false
		if f.Parsed != nil {
			if len(f.Parsed.Pages) > 0 {
				for _, pg := range f.Parsed.Pages {
					got = add(f, pg.PageNumber, pg.Text) || got
				}
			} else {
				got = add(f, 0, f.Parsed.Text)
			}
		}
		if !got {
			skipped = append(skipped, f.Name)
		}
	}
	n := float64(len(chunks))
	for i := range chunks {
		chunks[i].Score = 1 - float64(i)/(n+1)
	}
	return chunks, skipped, total
}

// libraryTokenLowerBound is a lower bound on the cl100k token count
// libraryChunks would report for files, computed in one rune scan with no
// tokenizer. It reads the same text libraryChunks reads (pages when present,
// else the whole text).
//
// Why it never over-estimates: cl100k's pre-tokenizer splits text into
// pieces before BPE, and BPE never merges across pieces. Every piece pattern
// holds at most one maximal run of letters (\p{L}+, optionally behind one
// non-letter, non-digit prefix rune, or a contraction like 's) or one run of
// at most three digits (\p{N}{1,3}), never a letter and a digit together —
// so each maximal letter run and each maximal digit run starts its own
// piece, and each piece is at least one token. Splitting a page only adds
// pieces. The bound is therefore safe for the reject decision: a selection
// above the accepting limit (the larger of chat_library_fulltext_max_tokens
// and chat_longcontext_max_tokens) by this count is above it by the exact
// count too, so nothing the exact path would accept is rejected. On German
// prose it lands at roughly 55-75% of the real count (one per word vs. the
// 1.3-1.8 tokens a German word costs), so it catches clearly oversized
// selections; borderline ones still fall through to the exact count.
func libraryTokenLowerBound(files []LibraryFile) int {
	n := 0
	for _, f := range files {
		if f.Parsed == nil {
			continue
		}
		if len(f.Parsed.Pages) > 0 {
			for _, pg := range f.Parsed.Pages {
				n += textTokenLowerBound(pg.Text)
			}
			continue
		}
		n += textTokenLowerBound(f.Parsed.Text)
	}
	return n
}

// textTokenLowerBound counts maximal letter runs plus maximal digit runs.
func textTokenLowerBound(s string) int {
	const none, letter, digit = 0, 1, 2
	n, prev := 0, none
	for _, r := range s {
		kind := none
		switch {
		case unicode.IsLetter(r):
			kind = letter
		case unicode.IsNumber(r):
			kind = digit
		}
		if kind != none && kind != prev {
			n++
		}
		prev = kind
	}
	return n
}
