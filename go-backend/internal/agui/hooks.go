package agui

import (
	"context"
	"errors"

	"github.com/ag-ui-protocol/ag-ui/sdks/community/go/pkg/core/events"

	"github.com/justrag/go-backend/internal/adkbridge"
)

// ErrThreadNotFound: the threadId is not a thread the caller may use (→ 404 thread_not_found).
var ErrThreadNotFound = errors.New("agui: thread not found")

// TurnOutput is what a finished (or paused/failed) run produced.
type TurnOutput struct {
	RunID     string
	Text      string // assistant text the client received
	Reasoning string
	Status    adkbridge.RunStatus // completed | interrupted | failed | cancelled
	Custom    map[string]any      // last value per custom event name emitted this run
}

// TurnHooks lets the embedding application own thread identity and persistence.
type TurnHooks interface {
	// ResolveThread maps the client threadId ("" = new thread) to the
	// canonical thread id, creating it if needed. It must enforce ownership
	// and return ErrThreadNotFound otherwise. firstMessage is the new user
	// text ("" on a resume).
	ResolveThread(ctx context.Context, sc adkbridge.Scope, threadID, firstMessage string) (string, error)
	// TurnStarted runs after the run row exists, before the agent runs.
	// userText is "" on a resume.
	TurnStarted(ctx context.Context, sc adkbridge.Scope, threadID, runID, userText string) error
	// TurnFinished runs once per run, on every terminal path, with a context
	// that survives client disconnect. Events it returns are emitted before
	// RUN_FINISHED when the stream is still open.
	TurnFinished(ctx context.Context, sc adkbridge.Scope, threadID string, out TurnOutput) ([]events.Event, error)
}
