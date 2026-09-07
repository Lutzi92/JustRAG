package chat

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/ai"
)

// ---------------------------------------------------------------------------
// RunTracker
// ---------------------------------------------------------------------------

// The Wave-4 G01 evidence: a flat answer emitted ~15 400 unbroken `_`
// (17 337 runes) before recovering. The guard must catch that at 400 — and
// it must catch it even though the provider hands the run over in many small
// chunks, i.e. the run state has to survive chunk boundaries.
func TestRunTracker_SingleRuneRunAcrossChunks(t *testing.T) {
	tr := NewRunTracker(400)
	fed := 0
	tripped := false
	for i := 0; i < 10; i++ {
		fed += 100
		if tr.Feed(strings.Repeat("_", 100)) {
			tripped = true
			break
		}
	}
	if !tripped {
		t.Fatalf("a 1000-rune `_` run fed in 100-rune chunks did not trip at limit 400")
	}
	// 400 is allowed, 401 is not: the trip must land in the chunk that
	// crosses the limit (the 5th, at 500 runes fed), never later.
	if fed != 500 {
		t.Errorf("tripped after %d runes fed; want the 5th chunk (500) — the run counter is not carried across chunk boundaries", fed)
	}
	if got := tr.RunLength(); got < 401 {
		t.Errorf("RunLength() = %d, want >= 401 (the run length at the trip)", got)
	}
	if !tr.Tripped() {
		t.Error("Tripped() = false after a trip")
	}
}

// A single Feed carrying the whole run must behave identically.
func TestRunTracker_SingleChunkRun(t *testing.T) {
	tr := NewRunTracker(400)
	if !tr.Feed(strings.Repeat("_", 1000)) {
		t.Fatal("a 1000-rune run in one chunk did not trip at limit 400")
	}
}

// Markdown table rules and separator lines are single-rune runs. The default
// 400 sits above any realistic rule width — the guard must not fire on them.
func TestRunTracker_TableRuleDoesNotTrip(t *testing.T) {
	cases := []struct {
		name  string
		limit int
		text  string
	}{
		{"300 dashes at limit 400", 400, "| a | b |\n|" + strings.Repeat("-", 300) + "|\n"},
		{"400 dashes at limit 401", 401, strings.Repeat("-", 400)},
		{"400 dashes at limit 400", 400, strings.Repeat("-", 400)},
		{"equals rule", 400, strings.Repeat("=", 120)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := NewRunTracker(tc.limit)
			if tr.Feed(tc.text) {
				t.Errorf("tripped on a %d-rune rule at limit %d (run length %d)", len([]rune(tc.text)), tc.limit, tr.RunLength())
			}
		})
	}
}

// 401 runes of one character DO trip at 400 — the boundary case the rule
// tests above sit just under.
func TestRunTracker_401Trips(t *testing.T) {
	tr := NewRunTracker(400)
	if !tr.Feed(strings.Repeat("_", 401)) {
		t.Fatal("401 runes of `_` did not trip at limit 400")
	}
}

// Degeneration is not always a single character: `ababab…` is the other
// shape the guard has to see.
func TestRunTracker_RepeatedPatternTrips(t *testing.T) {
	tr := NewRunTracker(400)
	if !tr.Feed(strings.Repeat("ab", 300)) {
		t.Fatal("`ab` x 300 (600 runes) did not trip at limit 400")
	}
	if got := tr.RunLength(); got < 401 {
		t.Errorf("RunLength() = %d, want >= 401", got)
	}
}

func TestRunTracker_PatternAcrossChunks(t *testing.T) {
	tr := NewRunTracker(400)
	// One continuous `abab…` run sliced into 7-rune chunks: the pattern
	// period never aligns with a chunk boundary, so only a carried tail can
	// keep the count going.
	full := strings.Repeat("ab", 400)
	tripped := false
	for i := 0; i < len(full); i += 7 {
		end := min(i+7, len(full))
		if tr.Feed(full[i:end]) {
			tripped = true
			break
		}
	}
	if !tripped {
		t.Fatal("a repeated `ab` pattern split across 7-rune chunks did not trip at limit 400")
	}
}

// Prose must never trip, however long.
func TestRunTracker_ProseDoesNotTrip(t *testing.T) {
	prose := strings.Repeat("Die Beitragshöhe richtet sich nach dem Einkommen. ", 200)
	tr := NewRunTracker(400)
	if tr.Feed(prose) {
		t.Fatalf("tripped on %d runes of prose (run length %d)", len([]rune(prose)), tr.RunLength())
	}
}

// The tracker sees every rune of every streamed answer, so its boundary
// carry must not allocate per rune. The append-then-reslice form it started
// with reallocated the backing array about once per four runes — thousands
// of allocations for a window of four. A fixed ring makes Feed allocation-
// free, which is what this pins: the only allocation left in the measured
// body is the tracker itself.
func TestRunTracker_FeedDoesNotAllocatePerRune(t *testing.T) {
	// Prose, so the tracker never trips and actually walks every rune.
	text := strings.Repeat("Die Beitragshöhe richtet sich nach dem Einkommen. ", 2000) // ~100k runes
	got := testing.AllocsPerRun(3, func() {
		tr := NewRunTracker(400)
		if tr.Feed(text) {
			t.Fatal("prose tripped the guard")
		}
	})
	if got > 2 {
		t.Errorf("Feed over ~100k runes allocated %.0f times, want <= 2 (the tracker itself)", got)
	}
}

func TestRunTracker_LimitZeroDisables(t *testing.T) {
	tr := NewRunTracker(0)
	if tr.Feed(strings.Repeat("_", 100000)) {
		t.Fatal("limit 0 must disable the guard entirely")
	}
	if tr.Tripped() {
		t.Error("Tripped() = true with limit 0")
	}
}

// ---------------------------------------------------------------------------
// StripDegenerateRun
// ---------------------------------------------------------------------------

func TestStripDegenerateRun_KeepsSurroundingText(t *testing.T) {
	before := "Die Antwort lautet 42."
	after := "Quelle: [1]"
	in := before + strings.Repeat("_", 1000) + after

	out, stripped := StripDegenerateRun(in, 400)
	if !stripped {
		t.Fatal("stripped = false on a 1000-rune run at limit 400")
	}
	if strings.Contains(out, strings.Repeat("_", 401)) {
		t.Error("the degenerate run is still present after stripping")
	}
	if !strings.Contains(out, before) {
		t.Errorf("text before the run was lost: %q", out)
	}
	if !strings.Contains(out, after) {
		t.Errorf("text after the run was lost: %q", out)
	}
}

func TestStripDegenerateRun_PatternRun(t *testing.T) {
	in := "vor" + strings.Repeat("ab", 300) + "nach"
	out, stripped := StripDegenerateRun(in, 400)
	if !stripped {
		t.Fatal("stripped = false on an `ab` x 300 run at limit 400")
	}
	if !strings.HasPrefix(out, "vor") || !strings.HasSuffix(out, "nach") {
		t.Errorf("surrounding text lost: %q", out)
	}
	if len([]rune(out)) > 400 {
		t.Errorf("the pattern run survived stripping: %d runes left", len([]rune(out)))
	}
}

func TestStripDegenerateRun_CleanTextUntouched(t *testing.T) {
	in := "| a | b |\n|" + strings.Repeat("-", 300) + "|\nnormaler Text"
	out, stripped := StripDegenerateRun(in, 400)
	if stripped {
		t.Error("stripped = true on a 300-rune table rule at limit 400")
	}
	if out != in {
		t.Errorf("clean text was modified:\ngot  %q\nwant %q", out, in)
	}
}

func TestStripDegenerateRun_LimitZero(t *testing.T) {
	in := strings.Repeat("_", 5000)
	out, stripped := StripDegenerateRun(in, 0)
	if stripped || out != in {
		t.Error("limit 0 must leave the text untouched")
	}
}

// The whole answer being one run is the degenerate extreme: the result is
// empty text, not a panic.
func TestStripDegenerateRun_EntireTextIsTheRun(t *testing.T) {
	out, stripped := StripDegenerateRun(strings.Repeat("_", 1000), 400)
	if !stripped {
		t.Fatal("stripped = false")
	}
	if out != "" {
		t.Errorf("out = %q, want empty", out)
	}
}

// ---------------------------------------------------------------------------
// Notice
// ---------------------------------------------------------------------------

func TestDegenerateNotice(t *testing.T) {
	if got := DegenerateNotice("de"); got != "[Antwort wegen einer degenerierten Zeichenfolge gekürzt.]" {
		t.Errorf("de notice = %q", got)
	}
	if got := DegenerateNotice("en"); got != "[Answer truncated after a degenerate character run.]" {
		t.Errorf("en notice = %q", got)
	}
	if got := DegenerateNotice("fr"); got != DegenerateNotice("en") {
		t.Errorf("an unknown language must fall back to the English notice, got %q", got)
	}
}

// ---------------------------------------------------------------------------
// Streaming wiring
// ---------------------------------------------------------------------------

// The production streaming sink: a fake stream emits a 1000-`_` run, the SSE
// output must stop at the trip, the persisted answer must be the stripped
// text plus the notice, and the trajectory event must be present.
func TestGuardedEmit_StopsStreamStripsAndNotifies(t *testing.T) {
	var sent []string
	var traj []map[string]any
	cancelled := 0

	guard := newAnswerGuard(400, "de", "web", func() { cancelled++ })
	var responseBuf, reasoningBuf strings.Builder
	emit := newGuardedEmit(guard, &responseBuf, &reasoningBuf,
		func(s string) { sent = append(sent, s) },
		func(string) {},
	)

	// Fake stream: a sentence, then 1000 `_` in 50-rune chunks, then more
	// text the cancelled completion would never really have produced.
	emit(ai.StreamEvent{Content: "Die Antwort lautet 42."})
	for i := 0; i < 20; i++ {
		emit(ai.StreamEvent{Content: strings.Repeat("_", 50)})
	}
	emit(ai.StreamEvent{Content: "tail that must never be forwarded"})

	if !guard.tripped() {
		t.Fatal("the guard did not trip on a 1000-rune run")
	}
	if cancelled != 1 {
		t.Fatalf("cancel called %d times, want exactly 1", cancelled)
	}

	streamed := strings.Join(sent, "")
	if strings.Contains(streamed, "tail that must never be forwarded") {
		t.Error("chunks after the trip were still forwarded to the client")
	}
	if n := strings.Count(streamed, "_"); n > 450 {
		t.Errorf("the client saw %d run runes; the stream must stop at the trip (<= limit + one chunk)", n)
	}

	final, appended := guard.finish(responseBuf.String())
	if strings.Contains(final, strings.Repeat("_", 401)) {
		t.Error("the persisted answer still carries the degenerate run")
	}
	if !strings.Contains(final, "Die Antwort lautet 42.") {
		t.Errorf("the answer text before the run was lost: %q", final)
	}
	if !strings.HasSuffix(final, DegenerateNotice("de")) {
		t.Errorf("the notice is not appended to the persisted answer: %q", final)
	}
	if !strings.Contains(appended, DegenerateNotice("de")) {
		t.Errorf("appended = %q, want it to carry the notice for the final content frame", appended)
	}

	guard.recordTrajectory(func(p map[string]any) { traj = append(traj, p) })
	if len(traj) == 0 {
		t.Fatal("no trajectory event emitted")
	}
	evt, ok := traj[0]["agentTrajectory"].(TrajectoryEvent)
	if !ok {
		t.Fatalf("payload is not a trajectory envelope: %v", traj[0])
	}
	if evt.Stage != "answer_degenerate_guard" {
		t.Errorf("stage = %q, want answer_degenerate_guard", evt.Stage)
	}
	if evt.Limit != 400 {
		t.Errorf("limit = %d, want 400", evt.Limit)
	}
	if evt.RunLength < 401 {
		t.Errorf("run_length = %d, want >= 401", evt.RunLength)
	}
}

// With the guard disabled every rune reaches the client and the answer is
// persisted verbatim — the kill switch has to be total.
func TestGuardedEmit_LimitZeroForwardsEverything(t *testing.T) {
	var sent []string
	cancelled := 0
	guard := newAnswerGuard(0, "de", "web", func() { cancelled++ })
	var responseBuf, reasoningBuf strings.Builder
	emit := newGuardedEmit(guard, &responseBuf, &reasoningBuf,
		func(s string) { sent = append(sent, s) }, func(string) {})

	for i := 0; i < 20; i++ {
		emit(ai.StreamEvent{Content: strings.Repeat("_", 50)})
	}
	if guard.tripped() || cancelled != 0 {
		t.Fatalf("guard fired with limit 0 (tripped=%v cancels=%d)", guard.tripped(), cancelled)
	}
	if n := strings.Count(strings.Join(sent, ""), "_"); n != 1000 {
		t.Errorf("client saw %d runes, want all 1000", n)
	}
	if responseBuf.Len() != 1000 {
		t.Errorf("buffered %d bytes, want 1000", responseBuf.Len())
	}
}

// Reasoning is deliberately NOT guarded (it is not the answer, and it is not
// persisted as one) — a long run there must neither trip nor be dropped.
func TestGuardedEmit_ReasoningIsNotGuarded(t *testing.T) {
	guard := newAnswerGuard(400, "de", "web", func() {})
	var responseBuf, reasoningBuf strings.Builder
	emit := newGuardedEmit(guard, &responseBuf, &reasoningBuf, func(string) {}, func(string) {})
	emit(ai.StreamEvent{Reasoning: strings.Repeat("_", 5000)})
	if guard.tripped() {
		t.Error("a run in the reasoning stream tripped the answer guard")
	}
	if reasoningBuf.Len() != 5000 {
		t.Errorf("reasoning buffered %d bytes, want 5000", reasoningBuf.Len())
	}
}

// ---------------------------------------------------------------------------
// GuardStreamedAnswer (the tripped-stream form used by every streaming
// surface)
// ---------------------------------------------------------------------------

// The tracker tripped, so the answer IS truncated — the notice and the
// metric must not depend on a second, independent detection agreeing. This
// is the case the API streams got wrong by calling the post-hoc
// GuardAnswerText: a buffer whose degenerate region does not survive into
// the text passed to the strip (here: nothing periodic at all) came out
// unannotated, and the reader saw an answer that simply stopped.
func TestGuardStreamedAnswer_ForcesTheNoticeOnATrippedStream(t *testing.T) {
	const buffered = "Die Antwort bricht hier ab."
	guarded, appended := GuardStreamedAnswer(buffered, buffered, 400, "de", "api_v1")
	if appended == "" {
		t.Fatal("appended = \"\" — a tripped stream must always get the notice")
	}
	if !strings.HasSuffix(guarded, DegenerateNotice("de")) {
		t.Errorf("guarded = %q, want the notice appended", guarded)
	}
	if !strings.HasPrefix(guarded, buffered) {
		t.Errorf("guarded = %q, want the buffered text kept", guarded)
	}
}

// The chunk that trips the guard is buffered but not forwarded, so any
// legitimate text ahead of the run inside it is in the persisted answer and
// not in the client's. It is streamed BEFORE the notice, which is what keeps
// the client's assembled text identical to the guarded answer — and every
// citation offset computed over the latter valid in the former.
func TestGuardStreamedAnswer_ForwardsTheUnsentPreRunPrefix(t *testing.T) {
	sent := "Erster Teil [1]. "
	tripChunk := "Zweiter Teil [2]: " + strings.Repeat("_", 500)
	guarded, appended := GuardStreamedAnswer(sent+tripChunk, sent, 400, "de", "api_v1")

	if !strings.HasPrefix(appended, "Zweiter Teil [2]:") {
		t.Errorf("appended = %q, want it to lead with the un-forwarded pre-run prefix", appended)
	}
	if client := sent + appended; client != guarded {
		t.Errorf("client text != guarded answer:\n client = %q\nguarded = %q", client, guarded)
	}
}

// The usual case: the run started in an earlier chunk, so the client already
// holds MORE text than the guarded answer and there is nothing to forward
// but the notice. Nothing may be spliced back in.
func TestGuardStreamedAnswer_RunStartedEarlierForwardsOnlyTheNotice(t *testing.T) {
	sent := "Antwort. " + strings.Repeat("_", 400)
	guarded, appended := GuardStreamedAnswer(sent+strings.Repeat("_", 50), sent, 400, "de", "web")
	if appended != "\n\n"+DegenerateNotice("de") {
		t.Errorf("appended = %q, want only the notice", appended)
	}
	if strings.Contains(guarded, "____") {
		t.Errorf("guarded = %q, want the run stripped", guarded)
	}
}

// GuardStreamedAnswer must use the limit it is GIVEN (the tracker's), never
// a fresh site_config read: with an operator changing the key mid-stream the
// two can differ, and the value that fired is the only one whose strip
// matches what the client was cut off at.
func TestGuardStreamedAnswer_UsesTheSuppliedLimit(t *testing.T) {
	buffered := "Antwort. " + strings.Repeat("-", 120)
	guarded, _ := GuardStreamedAnswer(buffered, buffered, 100, "de", "web")
	if strings.Contains(guarded, "----") {
		t.Errorf("guarded = %q, want the 120-rune run stripped at limit 100", guarded)
	}
	guarded2, _ := GuardStreamedAnswer(buffered, buffered, 400, "de", "web")
	if !strings.Contains(guarded2, strings.Repeat("-", 120)) {
		t.Errorf("guarded = %q, want the run KEPT at limit 400", guarded2)
	}
}

// ---------------------------------------------------------------------------
// GuardAnswerText (the post-hoc, non-streaming surfaces)
// ---------------------------------------------------------------------------

func TestGuardAnswerText_PostHoc(t *testing.T) {
	in := "Antwort." + strings.Repeat("_", 1000)
	out, appended := GuardAnswerText(context.Background(), nil, in, "en", "api_v1")
	if appended == "" {
		t.Fatal("appended = \"\" — the guard did not fire on a 1000-rune run")
	}
	if !strings.HasPrefix(out, "Antwort.") {
		t.Errorf("leading text lost: %q", out)
	}
	if !strings.HasSuffix(out, DegenerateNotice("en")) {
		t.Errorf("notice missing: %q", out)
	}
	if !strings.HasSuffix(out, appended) {
		t.Errorf("appended (%q) is not the suffix of the guarded text (%q)", appended, out)
	}
}

func TestGuardAnswerText_CleanAnswerUntouched(t *testing.T) {
	in := "Eine ganz normale Antwort mit einer Tabelle:\n|" + strings.Repeat("-", 200) + "|"
	out, appended := GuardAnswerText(context.Background(), nil, in, "de", "web")
	if appended != "" {
		t.Errorf("appended = %q on a clean answer", appended)
	}
	if out != in {
		t.Error("a clean answer must be returned byte-identically")
	}
}

// ---------------------------------------------------------------------------
// site_config reader
// ---------------------------------------------------------------------------

func TestChatAnswerDegenerateRunLimit(t *testing.T) {
	cases := map[string]struct {
		raw  *string
		want int
	}{
		"unset":         {nil, 400},
		"empty":         {strPtr(""), 400},
		"explicit":      {strPtr("800"), 800},
		"zero disables": {strPtr("0"), 0},
		"below range":   {strPtr("49"), 400},
		"at low bound":  {strPtr("50"), 50},
		"above range":   {strPtr("100001"), 400},
		"at high bound": {strPtr("100000"), 100000},
		"garbage":       {strPtr("viele"), 400},
		"negative":      {strPtr("-5"), 400},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := &stubSiteConfigReader{vals: map[string]*string{"chat_answer_degenerate_run_limit": tc.raw}}
			if got := ChatAnswerDegenerateRunLimit(context.Background(), r); got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
	if got := ChatAnswerDegenerateRunLimit(context.Background(), nil); got != 400 {
		t.Errorf("nil reader: got %d, want 400", got)
	}
}

type stubSiteConfigReader struct{ vals map[string]*string }

func (s *stubSiteConfigReader) GetSiteConfigValue(_ context.Context, key string) (*string, error) {
	return s.vals[key], nil
}

// ---------------------------------------------------------------------------
// The abort itself, end to end
// ---------------------------------------------------------------------------

// The point of the guard is not that the client stops rendering — it is that
// the PROVIDER stops generating (and billing). This drives the real
// ai.StreamCompletionWithHistory against a server that streams `_` forever,
// and asserts the server was cut off BY THE CANCELLATION.
//
// The assertion is deliberately not "the server wrote fewer than N frames":
// that was load-flaky (it failed 5/5 under -cpu=1), because a handler that
// gets the CPU first can push a bounded body into the socket buffers before
// the reader ever runs, and then the counter proves nothing. So the body is
// unbounded — the handler cannot reach an end on its own — and it records
// WHY it stopped. Only "the request context was cancelled" passes; a write
// error with a live context (a deadline, a broken pipe for another reason)
// and the iteration cap are separate, named failures. Remove the cancelGen()
// in answerGuard.feed and the handler runs until the cap, which fails.
func TestDegenerateGuard_CancelStopsTheProviderStream(t *testing.T) {
	// A hard bound so a regression fails instead of hanging the suite. It is
	// far beyond any socket buffer (~18 MB of frames), so it is never reached
	// while the guard works.
	const maxFrames = 200000
	var written atomic.Int64
	var exit atomic.Value // "ctx" | "write_error" | "frame_cap"
	handlerDone := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerDone)
		// Bound a write that blocks on a socket nobody drains any more, so a
		// regression fails the test instead of hanging the suite. Generous:
		// the cancel is what should end this, not the deadline.
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(10 * time.Second))
		w.Header().Set("Content-Type", "text/event-stream")
		frame := `data: {"choices":[{"delta":{"content":"` + strings.Repeat("_", 50) + `"}}]}` + "\n\n"
		// cancelled reports whether the request context is done, waiting a
		// moment first: a blocked write fails the instant the client drops
		// the connection, which can be marginally before the server's own
		// read loop cancels the request context.
		cancelled := func() bool {
			select {
			case <-r.Context().Done():
				return true
			case <-time.After(2 * time.Second):
				return false
			}
		}
		for i := 0; i < maxFrames; i++ {
			if r.Context().Err() != nil {
				exit.Store("ctx")
				return
			}
			if _, err := io.WriteString(w, frame); err != nil {
				if cancelled() {
					exit.Store("ctx")
				} else {
					exit.Store("write_error")
				}
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			written.Add(1)
		}
		exit.Store("frame_cap")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	resolver := ai.NewConfigResolver(&chatTestConfigStore{baseURL: srv.URL + "/v1/", model: "fake-model"})

	genCtx, cancelGen := context.WithCancel(context.Background())
	defer cancelGen()
	guard := newAnswerGuard(400, "de", "web", cancelGen)
	var responseBuf, reasoningBuf strings.Builder
	var sent []string
	emit := newGuardedEmit(guard, &responseBuf, &reasoningBuf,
		func(s string) { sent = append(sent, s) }, func(string) {})

	events, err := ai.StreamCompletionWithHistory(genCtx, resolver, nil, "hi", "sys", "kb-1", "", 0.3)
	if err != nil {
		t.Fatalf("start stream: %v", err)
	}
	// Keep draining the channel after the trip instead of abandoning it. An
	// abandoned stream stalls the socket, the handler's write eventually
	// fails, and the connection teardown cancels the request context — which
	// looks exactly like the guard's own cancel and makes the assertion below
	// pass with the cancel removed. Draining leaves cancellation as the only
	// thing that can stop the provider.
	for e := range events {
		if e.Done {
			break
		}
		emit(ai.StreamEvent{Content: e.Content, Reasoning: e.Reasoning})
	}

	if !guard.tripped() {
		t.Fatal("the guard never tripped on an endless `_` stream")
	}
	select {
	case <-handlerDone:
	case <-time.After(30 * time.Second):
		t.Fatal("the provider handler never returned — the guard did not cancel the completion")
	}
	if got, _ := exit.Load().(string); got != "ctx" {
		t.Errorf("the provider handler stopped because %q after %d frames; want %q — only a cancelled request context proves the guard stopped generation",
			got, written.Load(), "ctx")
	}
	if n := strings.Count(strings.Join(sent, ""), "_"); n > 450 {
		t.Errorf("the client was sent %d run runes, want the stream to stop at the trip", n)
	}
	guarded, _ := guard.finish(responseBuf.String())
	if strings.Contains(guarded, strings.Repeat("_", 401)) {
		t.Error("the persisted answer still carries the degenerate run")
	}
}
