package chat

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/prompts"
)

// SpanConfig configures ApplySpanVerification.
type SpanConfig struct {
	// Model is the fast-tier chat model for the quote-extraction call;
	// empty falls through GenerateCompletionStructured's own resolution
	// (the KB's chat model). Callers resolve this via
	// ChatCitationSpansModel before constructing SpanConfig.
	Model string
	// MaxSources caps how many distinct cited sources get one
	// quote-extraction request per answer (W3-R1). <= 0 means "no cap" —
	// callers should pass ChatCitationSpansMaxSources's clamped value.
	MaxSources int
	// Timeout bounds the whole extraction call. <= 0 means "no timeout
	// added" (the caller's ctx deadline, if any, still applies).
	Timeout time.Duration
	// Lang selects the DE/EN system prompt.
	Lang string
	// KbID scopes the completion call to the KB's resolved provider
	// config, same as every other chat-side LLM call.
	KbID string
}

// quoteRequest is one item sent to the span-extraction LLM call: N is the
// citation number, Window is the local sentence context around [N] in the
// answer (the model's CLAIM to ground), Content is the cited source's
// chunk body (capped to citationSpansMaxContentRunes, the SOURCE to search
// for a supporting quote in).
type quoteRequest struct {
	N       int
	Window  string
	Content string
}

// quoteResult is one item the span-extraction LLM call returns: N is the
// citation number, Quote is the verbatim excerpt the model claims supports
// it. An empty Quote (dropped before this type is populated — see
// ExtractCitationQuotes) means nothing supported the claim.
type quoteResult struct {
	N     int
	Quote string
}

// extractQuotesFn is a package-level seam so tests can inject a fake
// extractor without a real *ai.ConfigResolver / network call. Production
// code always runs ExtractCitationQuotes; tests swap this var (and MUST
// restore it, e.g. via t.Cleanup) to avoid leaking a stub into unrelated
// tests in the same package.
var extractQuotesFn = ExtractCitationQuotes

// citationSpansMaxContentRunes caps the per-source content shown to the
// span-extraction LLM. Chunks can run to several thousand characters; the
// supporting sentence is rarely near the very end, and capping keeps the
// prompt bounded across up to chat_citation_spans_max_sources sources in
// one call.
const citationSpansMaxContentRunes = 2000

// citationSpansSpec is the Structured-Outputs contract for the span
// extractor: a strict JSON object carrying one {n, quote} pair per
// requested item.
var citationSpansSpec = &ai.StructuredSpec{
	Name:   "citation_span_quotes",
	Schema: json.RawMessage(`{"type":"object","properties":{"quotes":{"type":"array","items":{"type":"object","properties":{"n":{"type":"integer"},"quote":{"type":"string"}},"required":["n","quote"],"additionalProperties":false}}},"required":["quotes"],"additionalProperties":false}`),
}

// ExtractCitationQuotes asks a fast-tier model to copy, verbatim, one short
// quote per requested item that supports the item's CLAIM from its SOURCE
// — one structured call covers every item (W3-R1), never one call per
// citation. Empty reqs is a no-op (no call, no error). Items the model
// could not ground are simply absent from the result (empty quotes are
// dropped), which ApplySpanVerification treats as "leave this status
// unchanged".
func ExtractCitationQuotes(ctx context.Context, resolver *ai.ConfigResolver, reqs []quoteRequest, kbID, lang, model string) ([]quoteResult, error) {
	if len(reqs) == 0 {
		return nil, nil
	}

	items := make([]prompts.CitationSpanItem, len(reqs))
	for i, r := range reqs {
		items[i] = prompts.CitationSpanItem{N: r.N, Claim: r.Window, Source: r.Content}
	}

	result, err := ai.GenerateCompletionStructured(ctx, resolver,
		prompts.CitationSpansUser(items), prompts.CitationSpansSystem(lang),
		kbID, model, citationSpansSpec)
	if err != nil {
		return nil, err
	}

	var parsed struct {
		Quotes []struct {
			N     int    `json:"n"`
			Quote string `json:"quote"`
		} `json:"quotes"`
	}
	if err := json.Unmarshal([]byte(result.Content), &parsed); err != nil {
		return nil, err
	}

	out := make([]quoteResult, 0, len(parsed.Quotes))
	for _, q := range parsed.Quotes {
		quote := strings.TrimSpace(q.Quote)
		if quote == "" {
			continue
		}
		out = append(out, quoteResult{N: q.N, Quote: quote})
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Matching: locate a verbatim (mod normalisation) quote inside content.
// ---------------------------------------------------------------------------

// citationSpanQuoteChars is the set of quote-mark runes W3-R3 strips before
// matching, so an LLM-added „…“ / "…" wrapper around an otherwise verbatim
// quote doesn't defeat the match.
var citationSpanQuoteChars = map[rune]bool{
	'„': true, '“': true, '”': true, '"': true,
	'\'': true, '‚': true, '‘': true, '’': true,
}

const (
	minQuoteRunes = 12
	maxQuoteRunes = 240
)

// normalizeForMatch NFC-normalises s, lower-cases it, collapses runs of
// whitespace to a single space, and drops quote-mark characters. It
// returns the resulting rune sequence plus, for each output rune, the RUNE
// offset in the ORIGINAL s it corresponds to (idx[i] is the source of
// out[i]) — MatchQuoteSpan uses idx to translate a match found in the
// normalised text back to offsets into the caller's original string.
//
// NFC is applied to the whole string first so a decomposed diacritic
// (rare — mostly OCR output) matches its precomposed form. When that
// changes the rune count (composition merged runes), the 1:1
// correspondence this function relies on no longer holds, so
// normalisation is skipped for that string and the original runes are
// used instead — fail-soft per W3-R3: a quote that only differs by
// normalization form in that rare case simply doesn't match, and keeps
// its n-gram/semantic verdict.
func normalizeForMatch(s string) (out []rune, idx []int) {
	base := s
	if nfc := norm.NFC.String(s); utf8.RuneCountInString(nfc) == utf8.RuneCountInString(s) {
		base = nfc
	}
	runes := []rune(base)
	out = make([]rune, 0, len(runes))
	idx = make([]int, 0, len(runes))
	lastWasSpace := false
	for i, r := range runes {
		if citationSpanQuoteChars[r] {
			continue
		}
		lr := unicode.ToLower(r)
		if unicode.IsSpace(lr) {
			if lastWasSpace {
				continue
			}
			lastWasSpace = true
			out = append(out, ' ')
			idx = append(idx, i)
			continue
		}
		lastWasSpace = false
		out = append(out, lr)
		idx = append(idx, i)
	}
	return out, idx
}

// trimTrailingQuotePunct trims a run of trailing sentence punctuation (and
// any whitespace baring it) from a normalised rune slice, so a quote
// copied with its closing period/comma still matches source text whose
// clause ends without one, and vice versa.
func trimTrailingQuotePunct(r []rune) []rune {
	end := len(r)
	for end > 0 {
		switch r[end-1] {
		case '.', ',', ';', ':', '!', '?', ' ':
			end--
		default:
			return r[:end]
		}
	}
	return r[:end]
}

// MatchQuoteSpan locates quote inside content and returns its RUNE offsets
// (domain: the caller's content string — for ApplySpanVerification that is
// ChatSource.Content — End exclusive) after W3-R3 normalisation: NFC,
// lower-case, whitespace-run collapse, quote-character strip, and a
// trailing-punctuation trim applied to the quote only. Returns ok=false
// when the normalised quote is shorter than 12 runes or longer than 240,
// or when no normalised occurrence exists in content.
func MatchQuoteSpan(content, quote string) (CitationSpanRef, bool) {
	normContent, idx := normalizeForMatch(content)
	normQuote, _ := normalizeForMatch(quote)
	normQuote = trimTrailingQuotePunct(normQuote)

	if len(normQuote) < minQuoteRunes || len(normQuote) > maxQuoteRunes {
		return CitationSpanRef{}, false
	}
	if len(normContent) == 0 {
		return CitationSpanRef{}, false
	}

	contentStr := string(normContent)
	quoteStr := string(normQuote)
	bytePos := strings.Index(contentStr, quoteStr)
	if bytePos < 0 {
		return CitationSpanRef{}, false
	}

	startRune := utf8.RuneCountInString(contentStr[:bytePos])
	endRune := startRune + len(normQuote)
	if endRune > len(idx) {
		return CitationSpanRef{}, false
	}

	return CitationSpanRef{Start: idx[startRune], End: idx[endRune-1] + 1}, true
}

// ---------------------------------------------------------------------------
// ApplySpanVerification: orchestrates extraction + matching for one answer.
// ---------------------------------------------------------------------------

// windowForN returns the local sentence window around citation number n in
// answer — the same context RunCitationValidation's semantic tier embeds
// — or "" if n never appears as a marker. Thin wrapper over windowsByN
// (citation_validator_semantic.go) so the span extractor's CLAIM text can
// never drift from what "the claim behind [N]" means elsewhere in this
// package.
func windowForN(answer string, n int) string {
	return windowsByN(answer)[n]
}

// collectQuoteRequests returns the extraction requests for answer: unique
// cited numbers (first-appearance order) whose current status is not
// out_of_range (no source exists to quote from) and whose source is not a
// RAPTOR/community summary (W3-R2 — those are paraphrased, so a verbatim
// span rarely exists), capped at maxSources (W3-R1, <= 0 = no cap), each
// with its Content truncated to citationSpansMaxContentRunes.
func collectQuoteRequests(answer string, sources []ChatSource, statuses []CitationStatus, maxSources int) []quoteRequest {
	spans := ExtractCitationSpans(answer)
	if len(spans) == 0 {
		return nil
	}

	statusByNumber := make(map[int]CitationStatus, len(statuses))
	for _, s := range statuses {
		statusByNumber[s.N] = s
	}

	seen := make(map[int]bool)
	var orderedNs []int
	for _, sp := range spans {
		for _, n := range sp.Numbers {
			if !seen[n] {
				seen[n] = true
				orderedNs = append(orderedNs, n)
			}
		}
	}

	var reqs []quoteRequest
	for _, n := range orderedNs {
		if maxSources > 0 && len(reqs) >= maxSources {
			break
		}
		st, ok := statusByNumber[n]
		if !ok || st.Reason == "out_of_range" {
			continue
		}
		idx := n - 1
		if idx < 0 || idx >= len(sources) {
			continue
		}
		src := sources[idx]
		if src.NodeKind == "summary" || src.NodeKind == "community_summary" {
			continue
		}
		content := src.Content
		if content == "" {
			continue
		}
		if r := []rune(content); len(r) > citationSpansMaxContentRunes {
			content = string(r[:citationSpansMaxContentRunes])
		}
		reqs = append(reqs, quoteRequest{N: n, Window: windowForN(answer, n), Content: content})
	}
	return reqs
}

// ApplySpanVerification fills CitationSpanRef + Method="span" for every
// citation ExtractCitationQuotes could ground with a matched verbatim
// quote. Entries it cannot improve (extractor found nothing for that N,
// matching failed, or the citation wasn't eligible — out_of_range or a
// summary source) are returned unchanged, so callers always get back at
// least the n-gram/semantic verdict. Fail-soft: any extractor error, or a
// timed-out ctx, leaves statuses byte-identical — no partial mutation.
func ApplySpanVerification(
	ctx context.Context,
	resolver *ai.ConfigResolver,
	cfg SpanConfig,
	answer string,
	sources []ChatSource,
	statuses []CitationStatus,
) []CitationStatus {
	if len(statuses) == 0 {
		return statuses
	}

	reqs := collectQuoteRequests(answer, sources, statuses, cfg.MaxSources)
	if len(reqs) == 0 {
		return statuses
	}

	callCtx := ctx
	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}

	results, err := extractQuotesFn(callCtx, resolver, reqs, cfg.KbID, cfg.Lang, cfg.Model)
	if err != nil || len(results) == 0 {
		return statuses
	}

	statusIdx := make(map[int]int, len(statuses))
	for i, s := range statuses {
		statusIdx[s.N] = i
	}

	for _, r := range results {
		si, ok := statusIdx[r.N]
		if !ok {
			continue
		}
		srcIdx := r.N - 1
		if srcIdx < 0 || srcIdx >= len(sources) {
			continue
		}
		span, ok := MatchQuoteSpan(sources[srcIdx].Content, r.Quote)
		if !ok {
			continue
		}
		statuses[si].Span = &span
		statuses[si].Method = "span"
		statuses[si].Verified = true
		statuses[si].Reason = ""
	}
	return statuses
}
