package agui

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"
	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/types"
	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/encoding/sse"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"

	"github.com/justrag/go-backend/internal/adkbridge"
)

// ScopeFunc refusals: ErrUnauthorized answers 401, ErrForbidden 403.
var (
	ErrUnauthorized = errors.New("agui: unauthenticated")
	ErrForbidden    = errors.New("agui: forbidden")
)

// ScopeFunc resolves the run scope from the authenticated request. It
// returns ErrUnauthorized (401) or ErrForbidden (403) to refuse the request.
type ScopeFunc func(r *http.Request) (adkbridge.Scope, error)

// Config wires one AG-UI endpoint to one ADK agent.
type Config struct {
	AppName  string
	Runner   *runner.Runner
	Sessions session.Service
	Runs     *adkbridge.RunStore
	Scope    ScopeFunc
	// Hooks, when set, owns thread identity and persistence: every threadId
	// is resolved through it (Scope.ChatID becomes the resolved id) and each
	// started run is reported to it. Nil keeps threads as bare session ids.
	Hooks TurnHooks
	// RunTimeout, when > 0, bounds a run's wall-clock time.
	RunTimeout time.Duration
}

type handler struct{ cfg Config }

// NewHandler returns the AG-UI endpoint (POST, RunAgentInput → SSE).
//
// Status codes before the stream starts: 400 bad body / no input / resume
// without thread / resume naming an interrupt that is not open while one is;
// 401/403 from ScopeFunc; 404 resume on a thread with no open interrupt for
// this user in this KB (a pause recorded in another KB stays claimable
// there); 409 interrupt_expired, interrupt_mismatch, thread_kb_mismatch (a
// new message on a thread whose first run was in another KB), or
// thread_has_open_interrupt (a new message on a thread with an open
// interrupt). With Hooks, a threadId the caller may not use is 404
// thread_not_found before anything is read or written, and a resume
// requires a threadId even before the hook runs. Otherwise 200 SSE. Error
// text from the
// model provider or the database never reaches the client.
func NewHandler(cfg Config) http.Handler { return &handler{cfg: cfg} }

func httpError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	sc, err := h.cfg.Scope(r)
	switch {
	case errors.Is(err, ErrUnauthorized):
		httpError(w, http.StatusUnauthorized, "unauthenticated")
		return
	case errors.Is(err, ErrForbidden):
		httpError(w, http.StatusForbidden, "forbidden")
		return
	case err != nil:
		slog.ErrorContext(r.Context(), "agui: resolve scope", "app", h.cfg.AppName, "error", err)
		httpError(w, http.StatusInternalServerError, "internal_error")
		return
	case sc.UserID == "":
		// Fail closed: every run, thread and claim is keyed by the user.
		httpError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	var in types.RunAgentInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		httpError(w, http.StatusBadRequest, "bad_request")
		return
	}
	ctx := adkbridge.WithScope(r.Context(), sc)

	resuming := len(in.Resume) > 0
	var userText string
	if h.cfg.Hooks != nil {
		// The thread is resolved before every thread-keyed step, and nothing
		// is created for a request that cannot run.
		if resuming {
			if in.ThreadID == "" {
				httpError(w, http.StatusBadRequest, "resume_requires_thread")
				return
			}
		} else {
			m, err := Input(ctx, &in, nil)
			if err != nil {
				httpError(w, http.StatusBadRequest, "no_input")
				return
			}
			userText = contentText(m)
		}
		tid, err := h.cfg.Hooks.ResolveThread(ctx, sc, in.ThreadID, userText)
		switch {
		case errors.Is(err, ErrThreadNotFound):
			httpError(w, http.StatusNotFound, "thread_not_found")
			return
		case err == nil && tid == "":
			err = errors.New("agui: hooks resolved an empty thread id")
			fallthrough
		case err != nil:
			slog.ErrorContext(ctx, "agui: resolve thread", "app", h.cfg.AppName, "error", err)
			httpError(w, http.StatusInternalServerError, "internal_error")
			return
		}
		in.ThreadID = tid
		sc.ChatID = tid
		ctx = adkbridge.WithScope(r.Context(), sc)
	}
	var msg *genai.Content
	if resuming {
		var ref *refusal
		if msg, ref = h.resumeInput(ctx, sc.UserID, &in); ref != nil {
			httpError(w, ref.code, ref.msg)
			return
		}
	} else {
		if in.ThreadID != "" {
			// A thread is bound to the KB of its first run: continuing it from
			// another KB would carry that KB's history (and pending approvals)
			// across the boundary.
			kb, found, err := h.cfg.Runs.ThreadKB(ctx, h.cfg.AppName, sc.UserID, in.ThreadID)
			if err != nil {
				slog.ErrorContext(ctx, "agui: thread kb", "app", h.cfg.AppName, "error", err)
				httpError(w, http.StatusInternalServerError, "internal_error")
				return
			}
			if found && kb != sc.KBID {
				httpError(w, http.StatusConflict, "thread_kb_mismatch")
				return
			}
			open, err := h.cfg.Runs.HasOpen(ctx, h.cfg.AppName, sc.UserID, in.ThreadID)
			if err != nil {
				slog.ErrorContext(ctx, "agui: check open interrupt", "app", h.cfg.AppName, "error", err)
				httpError(w, http.StatusInternalServerError, "internal_error")
				return
			}
			if open {
				httpError(w, http.StatusConflict, "thread_has_open_interrupt")
				return
			}
		}
		if msg, err = Input(ctx, &in, nil); err != nil {
			httpError(w, http.StatusBadRequest, "no_input")
			return
		}
	}
	if in.ThreadID == "" {
		in.ThreadID = uuid.NewString()
	}
	// Canonical form only: uuid.Parse accepts spellings ("urn:uuid:…",
	// braces) that Postgres rejects.
	if u, err := uuid.Parse(in.RunID); err != nil {
		in.RunID = uuid.NewString()
	} else {
		in.RunID = u.String()
	}
	// The new run row is written BEFORE a resume claims the paused run, so
	// no failure after the claim can lose the user's approval.
	if err := h.start(ctx, &in, sc); err != nil {
		slog.ErrorContext(ctx, "agui: start run", "app", h.cfg.AppName, "thread_id", in.ThreadID,
			"resume", resuming, "error", err)
		httpError(w, http.StatusInternalServerError, "internal_error")
		return
	}
	if h.cfg.Hooks != nil {
		if err := h.cfg.Hooks.TurnStarted(ctx, sc, in.ThreadID, in.RunID, userText); err != nil {
			slog.ErrorContext(ctx, "agui: turn started", "app", h.cfg.AppName, "run_id", in.RunID, "error", err)
			h.failBeforeStream(ctx, sc, &in, "turn started: "+err.Error())
			httpError(w, http.StatusInternalServerError, "internal_error")
			return
		}
	}
	if resuming {
		if ref := h.claim(ctx, sc, &in); ref != nil {
			h.failBeforeStream(ctx, sc, &in, "resume refused")
			httpError(w, ref.code, ref.msg)
			return
		}
	}
	h.stream(ctx, w, &in, sc, msg)
}

// failBeforeStream ends a started run that never reached the stream.
func (h *handler) failBeforeStream(ctx context.Context, sc adkbridge.Scope, in *types.RunAgentInput, detail string) {
	bg := context.WithoutCancel(ctx)
	if err := h.cfg.Runs.Finish(bg, in.RunID, adkbridge.RunFailed, detail); err != nil {
		slog.ErrorContext(ctx, "agui: finish run before stream", "app", h.cfg.AppName, "run_id", in.RunID, "error", err)
	}
	_, _ = h.turnFinished(bg, sc, in.ThreadID, TurnOutput{RunID: in.RunID, Status: adkbridge.RunFailed})
}

// turnFinished reports a run's end to the hooks and returns the events they
// want shown before RUN_FINISHED. A hook error is logged and returned with
// no events; what the client is told depends on the run's status (see
// stream). The run row has already reached its end state either way.
func (h *handler) turnFinished(ctx context.Context, sc adkbridge.Scope, threadID string, out TurnOutput) ([]events.Event, error) {
	if h.cfg.Hooks == nil {
		return nil, nil
	}
	evs, err := h.cfg.Hooks.TurnFinished(ctx, sc, threadID, out)
	if err != nil {
		slog.ErrorContext(ctx, "agui: turn finished", "app", h.cfg.AppName, "run_id", out.RunID,
			"status", out.Status, "error", err)
		return nil, err
	}
	return evs, nil
}

// contentText joins the text parts of a user message.
func contentText(c *genai.Content) string {
	var b strings.Builder
	for _, p := range c.Parts {
		if p != nil {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// refusal is an HTTP error answered before the stream starts.
type refusal struct {
	code int
	msg  string
}

var errInternal = &refusal{http.StatusInternalServerError, "internal_error"}

// resumeInput turns the resume into the ADK message from the user's own
// session. It runs before any row is written or claimed, so a malformed
// resume cannot consume a real interrupt.
func (h *handler) resumeInput(ctx context.Context, userID string, in *types.RunAgentInput) (*genai.Content, *refusal) {
	if in.ThreadID == "" {
		return nil, &refusal{http.StatusBadRequest, "resume_requires_thread"}
	}
	kind := InterruptKind(func(context.Context, string) (string, error) {
		return "", errors.New("agui: no open interrupts")
	})
	got, err := h.cfg.Sessions.Get(ctx, &session.GetRequest{AppName: h.cfg.AppName, UserID: userID, SessionID: in.ThreadID})
	switch {
	case err == nil:
		kind = OpenInterrupts(got.Session)
	case errors.Is(err, session.ErrNotFound):
		// Another user's thread is indistinguishable from a missing one.
	default:
		slog.ErrorContext(ctx, "agui: load session", "app", h.cfg.AppName, "error", err)
		return nil, errInternal
	}
	msg, err := Input(ctx, in, kind)
	if err != nil {
		// The resume does not match the session. Whether that is a 404 or a
		// 400 depends on whether this user has anything to resume at all.
		open, herr := h.cfg.Runs.HasOpen(ctx, h.cfg.AppName, userID, in.ThreadID)
		if herr != nil {
			slog.ErrorContext(ctx, "agui: check open interrupt", "app", h.cfg.AppName, "error", herr)
			return nil, errInternal
		}
		if open {
			return nil, &refusal{http.StatusBadRequest, "bad_resume"}
		}
		return nil, &refusal{http.StatusNotFound, "no_open_interrupt"}
	}
	return msg, nil
}

// claim takes the thread's paused run for the scope's user and KB, exactly once.
func (h *handler) claim(ctx context.Context, sc adkbridge.Scope, in *types.RunAgentInput) *refusal {
	ids := make([]string, 0, len(in.Resume))
	for _, e := range in.Resume {
		ids = append(ids, e.InterruptID)
	}
	// Scoped to the request's KB: a pause recorded in another KB is not
	// found (404) and stays claimable there.
	_, err := h.cfg.Runs.ClaimResume(ctx, h.cfg.AppName, sc.UserID, in.ThreadID, sc.KBID, ids)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, adkbridge.ErrNoOpenInterrupt):
		return &refusal{http.StatusNotFound, "no_open_interrupt"}
	case errors.Is(err, adkbridge.ErrInterruptExpired):
		return &refusal{http.StatusConflict, "interrupt_expired"}
	case errors.Is(err, adkbridge.ErrInterruptMismatch):
		return &refusal{http.StatusConflict, "interrupt_mismatch"}
	default:
		slog.ErrorContext(ctx, "agui: claim resume", "app", h.cfg.AppName, "error", err)
		return errInternal
	}
}

// start records the new run. A client-chosen run id that already exists
// (any user's) is replaced by a fresh one; RUN_STARTED reports the id used.
func (h *handler) start(ctx context.Context, in *types.RunAgentInput, sc adkbridge.Scope) error {
	run := adkbridge.Run{ID: in.RunID, ThreadID: in.ThreadID, AppName: h.cfg.AppName, UserID: sc.UserID, KBID: sc.KBID}
	err := h.cfg.Runs.Start(ctx, run)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "agent_runs_pkey" {
		run.ID = uuid.NewString()
		if err = h.cfg.Runs.Start(ctx, run); err == nil {
			in.RunID = run.ID
		}
	}
	return err
}

// stream runs the agent and writes AG-UI events. The run row reaches exactly
// one end state — completed, interrupted, failed or cancelled — on every
// path, through a context that survives a client disconnect.
func (h *handler) stream(ctx context.Context, w http.ResponseWriter, in *types.RunAgentInput, sc adkbridge.Scope, msg *genai.Content) {
	bg := context.WithoutCancel(ctx)
	log := slog.With("app", h.cfg.AppName, "run_id", in.RunID, "thread_id", in.ThreadID)
	var tr *Translator
	// ended reports the run's end state to the hooks exactly once; its
	// events are shown before RUN_FINISHED on the paths that write one.
	var (
		hookEvents []events.Event
		hookErr    error
		hooked     bool
	)
	ended := func(st adkbridge.RunStatus) {
		hooked = true // before the call: a panicking hook is not retried
		out := TurnOutput{RunID: in.RunID, Status: st}
		if tr != nil {
			out.Text, out.Reasoning, out.Custom = tr.Output()
		}
		hookEvents, hookErr = h.turnFinished(bg, sc, in.ThreadID, out)
	}
	// settled: the run row has its end state; settledAs is that state.
	settled := false
	var settledAs adkbridge.RunStatus
	finish := func(st adkbridge.RunStatus, detail string) {
		settled, settledAs = true, st
		if err := h.cfg.Runs.Finish(bg, in.RunID, st, detail); err != nil {
			log.ErrorContext(bg, "agui: finish run", "status", st, "error", err)
		}
		ended(st)
	}
	defer func() {
		// A panic (re-raised to net/http after this) must not leave the row
		// running, nor skip the hooks for a row that already ended.
		switch {
		case !settled:
			finish(adkbridge.RunFailed, "handler aborted")
		case !hooked:
			ended(settledAs)
		}
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	enc := sse.NewSSEWriter()
	emit := func(ev events.Event) error { return enc.WriteEvent(ctx, w, ev) }

	tr, err := NewTranslator(in.ThreadID, in.RunID, emit)
	if err != nil {
		finish(adkbridge.RunCancelled, "")
		return
	}
	// The run gets its own deadline (RunTimeout) under the request context,
	// so a run out of time is told apart from a client that left: the
	// client is still there and gets RUN_ERROR.
	runCtx := ctx
	if h.cfg.RunTimeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, h.cfg.RunTimeout)
		defer cancel()
	}
	timedOut := func() {
		log.WarnContext(bg, "agui: run exceeded its time budget", "timeout", h.cfg.RunTimeout)
		_ = tr.Fail(errors.New("run failed"))
		finish(adkbridge.RunFailed, "run time budget exceeded")
	}
	for ev, err := range h.cfg.Runner.Run(runCtx, sc.UserID, in.ThreadID, msg, agent.RunConfig{StreamingMode: agent.StreamingModeSSE}) {
		if ctx.Err() != nil {
			finish(adkbridge.RunCancelled, "")
			return
		}
		if runCtx.Err() != nil {
			timedOut()
			return
		}
		if err != nil {
			// Provider / storage detail stays in the log and the run row.
			log.ErrorContext(ctx, "agui: run failed", "error", err)
			_ = tr.Fail(errors.New("run failed"))
			finish(adkbridge.RunFailed, err.Error())
			return
		}
		if err := tr.Event(ev); err != nil {
			// Writing to the client failed: it is gone.
			finish(adkbridge.RunCancelled, "")
			return
		}
	}
	if ctx.Err() != nil {
		finish(adkbridge.RunCancelled, "")
		return
	}
	if runCtx.Err() != nil {
		timedOut()
		return
	}
	if pending := tr.Interrupts(); len(pending) > 0 {
		open := make([]adkbridge.OpenInterrupt, 0, len(pending))
		for _, i := range pending {
			open = append(open, adkbridge.OpenInterrupt{ID: i.ID, Reason: i.Reason, ToolCallID: i.ToolCallID})
		}
		// Taken before the row is written, so the advertised expiry is never
		// later than the stored one.
		expires := time.Now().Add(h.cfg.Runs.TTL())
		if err := h.cfg.Runs.Interrupt(bg, in.RunID, open); err != nil {
			// Typically ErrThreadHasOpenInterrupt (a concurrent run paused the
			// thread first). Announcing an interrupt nobody can resume would
			// strand the client, so the run fails instead.
			log.ErrorContext(bg, "agui: record interrupt", "error", err)
			_ = tr.Fail(errors.New("run failed"))
			finish(adkbridge.RunFailed, "record interrupt: "+err.Error())
			return
		}
		settled, settledAs = true, adkbridge.RunInterrupted
		tr.SetInterruptExpiry(expires)
		// Settled first: the hooks see the final text the client gets, and
		// their events land after the open step closes.
		_ = tr.settle()
		ended(adkbridge.RunInterrupted)
	} else {
		_ = tr.settle()
		finish(adkbridge.RunCompleted, "")
		if hookErr != nil {
			// The answer was not persisted and would vanish on reload: the
			// client is told the run failed. The interrupt path keeps its
			// RUN_FINISHED — that interrupt is real and open in the DB.
			_ = tr.Fail(errors.New("run failed"))
			return
		}
	}
	for _, ev := range hookEvents {
		_ = emit(ev)
	}
	_ = tr.Finish()
}
