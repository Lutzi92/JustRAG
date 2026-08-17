// Package usage records one row per accepted chat turn, on every surface that
// can answer from a KB: the web chat, the public API v1, the OpenAI-compatible
// endpoint, and the KB-as-MCP endpoint.
//
// Why a dedicated ledger instead of counting `messages`: internal/openaicompat
// and internal/mcpserver persist nothing at all (only internal/chat/store_pg.go
// writes chats/messages), so message-based counters and every "last activity"
// timestamp were blind to API traffic. `messages` stays chat history; this table
// is the usage truth.
//
// A row is written when a turn is ACCEPTED — after the KB and user are resolved
// and the request body validated, before the answer is produced. A turn that
// then fails still counted against the model backend, which is what makes these
// numbers comparable with the LLM gateway's own usage view.
package usage

import "context"

// Surface identifies the entry point a turn arrived through. The values are
// mirrored by the usage_events_surface_check CHECK constraint; adding one
// requires a migration.
type Surface string

const (
	SurfaceWeb          Surface = "web"
	SurfaceAPIv1        Surface = "api_v1"
	SurfaceOpenAICompat Surface = "openai_compat"
	SurfaceMCP          Surface = "mcp"
)

// Event is one accepted turn. APIKeyID is nil for web turns (JWT auth).
type Event struct {
	KbID     string
	UserID   string
	APIKeyID *string
	Surface  Surface
}

// Recorder persists usage events. Record never returns an error and never
// blocks: telemetry must not be able to fail a chat turn or slow one down.
type Recorder interface {
	Record(ctx context.Context, e Event)
}

// NopRecorder satisfies Recorder and does nothing. Used by tests and as the
// zero value for handlers wired without a recorder.
type NopRecorder struct{}

// Record does nothing.
func (NopRecorder) Record(context.Context, Event) {}
