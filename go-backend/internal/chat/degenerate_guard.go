package chat

import (
	"context"
	"strings"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/observability"
)

// ---------------------------------------------------------------------------
// Degenerate-answer guard (Wave-5 Task 7, ruling W5-R4)
// ---------------------------------------------------------------------------
//
// A generation-layer guard, independent of orchestrator: some answers
// collapse into a runaway repetition and never recover on their own. The
// Wave-4 evidence is question G01, whose flat long-context answer emitted
// ~15 400 unbroken `_` inside a 17 337-rune response before returning to
// prose — minutes of streamed garbage, a useless persisted answer, and a
// bill for every token of it.
//
// The guard watches the assembled answer while it streams. Two shapes are
// degenerate:
//
//   - a run of ONE repeated rune (`____…`, `----…`), and
//   - a repeated 2–4-rune pattern (`ababab…`, `. . . `).
//
// Both are measured as the length of the maximal region that is periodic
// with period p ∈ [1, 4]; the guard fires when that length EXCEEDS the
// limit. Markdown table rules and separator lines are period-1 regions, so
// the limit has to sit above any realistic rule width — the default 400
// does (a 300-`-` rule is common, 400 is not).
//
// The tracker state is carried across chunk boundaries: a provider hands a
// long run over in many small chunks, and a per-chunk counter would never
// see it.
const (
	// maxPatternRunes is the longest repeated pattern the guard recognises.
	// Beyond 4 the detector starts matching legitimate prose rhythm.
	maxPatternRunes = 4

	// degenerateRunLimitDefault is the default value of
	// chat_answer_degenerate_run_limit, in runes.
	degenerateRunLimitDefault = 400

	// degenerateRunLimitMin / Max bound an operator-supplied limit. 0 is
	// accepted outside this range as the explicit "disabled" value.
	degenerateRunLimitMin = 50
	degenerateRunLimitMax = 100000
)

// DegenerateNotice is the one-line notice appended to an answer the guard
// truncated, in the answer language. Anything that is not German gets the
// English line — the chat surfaces only ever pass "de"/"en", but the public
// API and the MCP server accept an arbitrary language string.
func DegenerateNotice(lang string) string {
	if strings.EqualFold(strings.TrimSpace(lang), "de") {
		return "[Antwort wegen einer degenerierten Zeichenfolge gekürzt.]"
	}
	return "[Answer truncated after a degenerate character run.]"
}

// ---------------------------------------------------------------------------
// RunTracker
// ---------------------------------------------------------------------------

// RunTracker watches a streaming answer for a degenerate run. Feed it every
// content chunk in order; it reports the first chunk that pushes a periodic
// region past the limit. Not safe for concurrent use — one tracker belongs
// to one answer stream, which is single-consumer by construction.
type RunTracker struct {
	limit int

	// carry is the boundary carry: the last maxPatternRunes runes seen,
	// kept SO THAT the periodicity comparison r[j] == r[j-p] can span a
	// chunk boundary. Without it every chunk would restart the count and a
	// run split across chunks — which is how a provider always delivers
	// one — would never trip.
	//
	// A fixed RING, not a slice: this is the hot loop of the guard (it sees
	// every rune of every streamed answer), and the append-then-reslice form
	// it replaces reallocated the backing array roughly once per four runes,
	// i.e. thousands of allocations per answer for a window that never grows.
	// carry[i%maxPatternRunes] holds the i-th rune fed; `seen` is the total
	// count, so the rune p positions back is carry[(seen-p)%maxPatternRunes].
	carry [maxPatternRunes]rune
	seen  int

	// cur[p] is the length of the maximal region ending at the last rune
	// seen that is periodic with period p. cur[0] is unused.
	cur [maxPatternRunes + 1]int

	tripped   bool
	runLength int
}

// NewRunTracker returns a tracker for the given limit. A limit <= 0
// disables it: Feed always reports false and never allocates.
func NewRunTracker(limit int) *RunTracker {
	return &RunTracker{limit: limit}
}

// Limit is the configured limit in runes (0 = disabled).
func (t *RunTracker) Limit() int { return t.limit }

// Tripped reports whether a degenerate run has been seen.
func (t *RunTracker) Tripped() bool { return t.tripped }

// RunLength is the length in runes of the periodic region at the moment of
// the trip, 0 when the tracker never tripped.
func (t *RunTracker) RunLength() int { return t.runLength }

// Feed consumes one content chunk and reports whether the tracker is
// tripped. Once tripped it stays tripped and stops scanning.
func (t *RunTracker) Feed(chunk string) bool {
	if t.limit <= 0 || t.tripped {
		return t.tripped
	}
	for _, r := range chunk {
		// have is how much history the ring actually holds — below
		// maxPatternRunes only at the very start of a stream.
		have := min(t.seen, maxPatternRunes)
		for p := 1; p <= maxPatternRunes; p++ {
			if have >= p && t.carry[(t.seen-p)%maxPatternRunes] == r {
				t.cur[p]++
			} else {
				// The region restarts. Its first p runes are the pattern
				// itself, so a fresh region is p runes long (or fewer while
				// the stream is still shorter than p).
				t.cur[p] = min(have+1, p)
			}
			if t.cur[p] > t.limit {
				t.tripped = true
				t.runLength = t.cur[p]
				return true
			}
		}
		t.carry[t.seen%maxPatternRunes] = r
		t.seen++
	}
	return false
}

// ---------------------------------------------------------------------------
// StripDegenerateRun
// ---------------------------------------------------------------------------

// StripDegenerateRun removes every degenerate run from text, keeping the
// text before and after each one, and reports whether anything was removed.
// limit <= 0 returns the text untouched. The detection is exactly the one
// RunTracker applies while streaming, so a stream that tripped always has
// something to strip.
func StripDegenerateRun(text string, limit int) (string, bool) {
	if limit <= 0 || text == "" {
		return text, false
	}
	rs := []rune(text)
	spans := degenerateSpans(rs, limit)
	if len(spans) == 0 {
		return text, false
	}
	var b strings.Builder
	prev := 0
	for _, s := range spans {
		if s[0] > prev {
			b.WriteString(string(rs[prev:s[0]]))
		}
		if s[1] > prev {
			prev = s[1]
		}
	}
	if prev < len(rs) {
		b.WriteString(string(rs[prev:]))
	}
	return b.String(), true
}

// degenerateSpans returns the [start, end) rune ranges of every maximal
// region that is periodic with a period of 1..maxPatternRunes and longer
// than limit. Spans from different periods overlap (a run of one rune is
// periodic with every period), so the result is merged before use.
func degenerateSpans(rs []rune, limit int) [][2]int {
	var spans [][2]int
	for p := 1; p <= maxPatternRunes; p++ {
		cur := 0
		for j := 0; j < len(rs); j++ {
			if j >= p && rs[j] == rs[j-p] {
				cur++
				continue
			}
			if cur > limit {
				spans = append(spans, [2]int{j - cur, j})
			}
			cur = min(j+1, p)
		}
		if cur > limit {
			spans = append(spans, [2]int{len(rs) - cur, len(rs)})
		}
	}
	return mergeSpans(spans)
}

// mergeSpans sorts and coalesces overlapping/adjacent spans.
func mergeSpans(in [][2]int) [][2]int {
	if len(in) < 2 {
		return in
	}
	// Insertion sort by start: the slice holds at most a handful of spans.
	for i := 1; i < len(in); i++ {
		for j := i; j > 0 && in[j][0] < in[j-1][0]; j-- {
			in[j], in[j-1] = in[j-1], in[j]
		}
	}
	out := in[:1]
	for _, s := range in[1:] {
		last := &out[len(out)-1]
		if s[0] <= last[1] {
			if s[1] > last[1] {
				last[1] = s[1]
			}
			continue
		}
		out = append(out, s)
	}
	return out
}

// ---------------------------------------------------------------------------
// Applying the guard to a finished answer
// ---------------------------------------------------------------------------

// applyDegenerateGuard strips the degenerate runs from text and appends the
// notice. Returns the guarded text and the suffix that was appended, so a
// streaming caller can emit exactly that as its final content frame. force
// makes it append the notice even when nothing was stripped — the streaming
// guard cancelled the completion, so the answer is truncated whether or not
// the buffered tail still holds the whole run.
func applyDegenerateGuard(text string, limit int, lang string, force bool) (guarded, appended string) {
	if limit <= 0 {
		return text, ""
	}
	clean, stripped := StripDegenerateRun(text, limit)
	if !stripped && !force {
		return text, ""
	}
	clean = strings.TrimRight(clean, " \t\r\n")
	notice := DegenerateNotice(lang)
	if clean == "" {
		return notice, notice
	}
	appended = "\n\n" + notice
	return clean + appended, appended
}

// GuardStreamedAnswer applies the guard to a streaming answer whose
// RunTracker TRIPPED. It is the counterpart of GuardAnswerText for the
// surfaces that stream (public API, OpenAI-compat, and the two web paths via
// answerGuard.finish), and it differs from it in three ways that all follow
// from the trip having already happened:
//
//   - the strip is FORCED. The completion was cancelled, so the answer is
//     truncated whether or not the buffered tail still holds a region
//     StripDegenerateRun re-detects; the notice and the metric are what tell
//     the reader (and the operator) that it was cut, and they must not
//     depend on a second, independent detection agreeing with the tracker.
//   - the limit is the TRACKER's limit, passed in. Re-reading
//     chat_answer_degenerate_run_limit here would be a second site_config
//     read per tripped turn, and — if an operator changed the key mid-stream
//     — a different limit than the one that actually fired.
//   - it returns the content the client still has to be SENT, not just the
//     notice. The chunk that trips the guard is buffered but not forwarded,
//     so any legitimate text preceding the run INSIDE that chunk is part of
//     the persisted (and, on OpenAI-compat, annotated) answer while the
//     client never saw it — every citation marker in that prefix would then
//     sit at a different offset in the client's assembled text than in the
//     annotations computed over the guarded answer. Forwarding the missing
//     prefix ahead of the notice closes that gap.
//
// sent is the text already forwarded to the client, which is always a prefix
// of buffered.
func GuardStreamedAnswer(buffered, sent string, limit int, lang, surface string) (guarded, appended string) {
	guarded, appended = applyDegenerateGuard(buffered, limit, lang, true)
	observability.RecordAnswerDegenerate(surface)
	if pending := pendingClientText(guarded, appended, sent); pending != "" {
		appended = pending + appended
	}
	return guarded, appended
}

// pendingClientText returns the part of the guarded answer's BODY (the
// guarded text minus the appended notice) that the client has not been sent.
//
// It is empty in the common case, where the run began in an earlier chunk
// and the body therefore ends before everything the client already holds.
// The HasPrefix check is what keeps this honest: the client's text can only
// be extended, never rewritten, so a body that does not start with what was
// already sent yields nothing rather than a bogus splice.
func pendingClientText(guarded, appended, sent string) string {
	body := strings.TrimSuffix(guarded, appended)
	if len(body) <= len(sent) || !strings.HasPrefix(body, sent) {
		return ""
	}
	return body[len(sent):]
}

// GuardAnswerText applies the degenerate-run guard to a FINISHED answer —
// the post-hoc form used by the non-streaming surfaces (public API,
// OpenAI-compat, KB-as-MCP-server, the web JSON branch), which have no
// stream to abort. Returns the guarded answer and the suffix that was
// appended (empty when the answer was clean). Records
// rag_answer_degenerate_total{surface} when it fires.
func GuardAnswerText(ctx context.Context, reader SiteConfigReader, text, lang, surface string) (guarded, appended string) {
	limit := ChatAnswerDegenerateRunLimit(ctx, reader)
	guarded, appended = applyDegenerateGuard(text, limit, lang, false)
	if appended != "" {
		observability.RecordAnswerDegenerate(surface)
	}
	return guarded, appended
}

// ---------------------------------------------------------------------------
// Streaming wiring
// ---------------------------------------------------------------------------

// answerGuard couples a RunTracker to the cancel func of the child context
// the answer completion runs under. Both streaming answer paths in this
// package build one; the trip cancels the completion, which is what stops
// the provider from generating (and billing) the rest of the run.
type answerGuard struct {
	tracker *RunTracker
	cancel  context.CancelFunc
	lang    string
	surface string
	// sentBytes is how much of the buffered answer has been forwarded to
	// the client. Everything forwarded is a prefix of the buffer, so this
	// one counter is enough for finish to work out what the client is still
	// missing (see pendingClientText).
	sentBytes int
}

func newAnswerGuard(limit int, lang, surface string, cancel context.CancelFunc) *answerGuard {
	return &answerGuard{tracker: NewRunTracker(limit), cancel: cancel, lang: lang, surface: surface}
}

func (g *answerGuard) tripped() bool { return g.tracker.Tripped() }

// feed reports whether this chunk tripped the guard, cancelling the
// completion's context when it does.
func (g *answerGuard) feed(content string) bool {
	if g.tracker.Tripped() {
		return true
	}
	if !g.tracker.Feed(content) {
		return false
	}
	if g.cancel != nil {
		g.cancel()
	}
	return true
}

// finish produces the answer to persist: the buffered text with the
// degenerate run removed and the notice appended, plus the content to stream
// as the final frame — the trip chunk's un-forwarded pre-run prefix (usually
// empty) followed by the notice. Records the metric.
func (g *answerGuard) finish(text string) (guarded, appended string) {
	sent := text
	if g.sentBytes < len(sent) {
		sent = sent[:g.sentBytes]
	}
	return GuardStreamedAnswer(text, sent, g.tracker.Limit(), g.lang, g.surface)
}

// recordTrajectory emits the answer_degenerate_guard trajectory event so the
// reasoning panel (and the trajectory eval) can see why the answer stopped.
func (g *answerGuard) recordTrajectory(emit func(map[string]any)) {
	emitTrajectory(emit, TrajectoryEvent{
		Stage:     "answer_degenerate_guard",
		Decision:  "truncated",
		Limit:     g.tracker.Limit(),
		RunLength: g.tracker.RunLength(),
	}, nil)
}

// newGuardedEmit builds the ai.StreamEvent sink both streaming answer paths
// use: it buffers the answer and the reasoning, forwards each frame to the
// client, and feeds every content chunk to the guard.
//
// The chunk that trips the guard is still BUFFERED (the strip needs the run
// and whatever legitimate text preceded it inside the same chunk) but not
// forwarded — that is where the visible stream stops. Everything after it is
// dropped entirely: the completion is already cancelled and only the notice
// still goes out. Reasoning is deliberately not guarded (it is not the
// answer and is not what a client renders as one).
func newGuardedEmit(
	g *answerGuard,
	responseBuf, reasoningBuf *strings.Builder,
	sendContent, sendReasoning func(string),
) func(ai.StreamEvent) {
	return func(e ai.StreamEvent) {
		if e.Content != "" && !g.tripped() {
			responseBuf.WriteString(e.Content)
			if !g.feed(e.Content) {
				sendContent(e.Content)
				g.sentBytes += len(e.Content)
			}
		}
		if e.Reasoning != "" {
			reasoningBuf.WriteString(e.Reasoning)
			sendReasoning(e.Reasoning)
		}
	}
}
