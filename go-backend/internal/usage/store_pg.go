package usage

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/safego"
)

// PGRecorder writes usage events to the main Postgres pool.
type PGRecorder struct {
	pool *pgxpool.Pool
}

// NewRecorder creates a PGRecorder over the main pool.
func NewRecorder(pool *pgxpool.Pool) *PGRecorder {
	return &PGRecorder{pool: pool}
}

// Compile-time interface assertion.
var _ Recorder = (*PGRecorder)(nil)

// Record inserts one usage_events row, fire-and-forget.
//
// The insert runs on a detached context (context.WithoutCancel + a timeout)
// because the caller's request context is canceled the moment the SSE stream
// finishes, which would otherwise abort the write on exactly the turns we most
// want counted. WithoutCancel keeps the logctx values (request_id, user_id,
// kb_id) and the trace span, so a failed write is still traceable — the same
// pattern apikeyauth.maybeUpdateLastUsed uses for its last_used_at update.
//
// Failures are logged at WARN and swallowed: a missing usage row is a reporting
// gap, not a reason to fail a user's chat turn.
func (r *PGRecorder) Record(ctx context.Context, e Event) {
	if r == nil || r.pool == nil {
		return
	}
	safego.GoCtx(ctx, func() {
		bgCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()

		const sql = `
			INSERT INTO usage_events (kb_id, user_id, api_key_id, surface)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4)`
		if _, err := r.pool.Exec(bgCtx, sql, e.KbID, e.UserID, e.APIKeyID, string(e.Surface)); err != nil {
			logctx.From(bgCtx).Warn("usage: record failed",
				"kb_id", e.KbID, "surface", string(e.Surface), "error", err)
		}
	})
}
