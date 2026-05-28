// Package adminagentmetrics serves the Phase 1 §1.4 admin agent-decisions
// panel. The panel turns "is the agent doing the right thing" from a
// Prometheus query into a glance: per-(window, kb) outcome distributions
// for the agentic chat, plan-execute, and CRAG paths.
//
// Read path: GET /api/admin/agent-metrics?window=1h|24h|7d&kb_id=<id>
// Write path: chat handlers fire-and-forget Insert(...) at the end of each
// chat run; failures log and drop. Aggregation is deliberately a single
// SQL round-trip — at 10k chat messages per KB the table fits in a few
// hundred KB.
package adminagentmetrics

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/logctx"
)

// ---------------------------------------------------------------------------
// Public types
// ---------------------------------------------------------------------------

// DecisionDistribution is one row of the panel response: total decisions
// in the window, plus per-outcome counts. ByLabel keys are the closed-enum
// outcome labels emitted by the orchestrators (mirrors metrics.go).
type DecisionDistribution struct {
	Total   int            `json:"total"`
	ByLabel map[string]int `json:"by_label"`
}

// AgentMetricsResponse is the JSON the admin panel renders. Counts and
// percentiles are aggregated server-side so the frontend stays a thin
// presentation layer; the panel can issue this one request per (window,
// kb_id) pair without needing PromQL access.
type AgentMetricsResponse struct {
	WindowSeconds int                  `json:"window_seconds"`
	KbID          string               `json:"kb_id,omitempty"`
	AgenticChat   DecisionDistribution `json:"agentic_chat"`
	PlanExecute   DecisionDistribution `json:"plan_execute"`
	CRAG          DecisionDistribution `json:"crag"`
	MedianHops    float64              `json:"median_hops"`
	MedianRounds  float64              `json:"median_rounds"`
	P95LatencyMs  float64              `json:"p95_latency_ms"`
	// ToolMix is the AP-B4 per-tool aggregate: how often each tool
	// was dispatched in the window, plus median per-tool latency.
	// Empty when no tool dispatches happened (legacy non-tool-aware
	// deployments).
	ToolMix []ToolMixEntry `json:"tool_mix"`
}

// ToolMixEntry is one row of the AP-B4 tool-mix aggregate. ErrorRate
// is the fraction of calls whose status was anything other than "ok"
// — combines "error" and "timeout" because the admin panel cares
// about "is this tool reliable" not the timeout-vs-error distinction
// (which is operator-debug territory).
type ToolMixEntry struct {
	Tool             string  `json:"tool"`
	Calls            int     `json:"calls"`
	MedianDurationMs float64 `json:"median_duration_ms"`
	ErrorRate        float64 `json:"error_rate"`
}

// Mode labels — closed enum so a typo in a chat handler doesn't silently
// inflate a non-existent bucket.
const (
	ModeAgentic     = "agentic"
	ModePlanExecute = "plan_execute"
	ModeCRAG        = "crag"
	ModeStandard    = "standard"
)

// ---------------------------------------------------------------------------
// Store
// ---------------------------------------------------------------------------

// DecisionRow is one persisted chat-decision record.
//
// ToolCalls (AP-B4) is the per-turn tool-dispatch sequence — empty
// when no MCP tool was dispatched (legacy non-tool-aware paths or
// turns that didn't reach the dispatcher). Persisted as JSONB so
// the admin "Tool-Mix" card aggregates with `jsonb_array_elements`
// instead of joining a separate table.
type DecisionRow struct {
	KbID      uuid.UUID
	Mode      string
	Outcome   string
	Hops      int
	Rounds    int
	LatencyMs int
	ToolCalls []ToolCallEntry
}

// ToolCallEntry mirrors chat.ToolCallRecord on the persistence side.
// Defined here (not imported from chat) so the chat package keeps
// no dependency on adminagentmetrics — the adapter conversion lives
// at the recorder boundary in chat/http_send.go.
type ToolCallEntry struct {
	Tool       string `json:"tool"`
	DurationMs int    `json:"duration_ms"`
	Status     string `json:"status"`
}

// Store is the small persistence surface adminagentmetrics needs. The
// production implementation is *PgStore; tests inject fakes.
type Store interface {
	Insert(ctx context.Context, r DecisionRow) error
	Aggregate(ctx context.Context, kbID *uuid.UUID, since time.Time) (AgentMetricsResponse, error)
}

// PgStore is the pgx-backed Store.
type PgStore struct {
	pool *pgxpool.Pool
}

// NewPgStore returns a Postgres-backed Store. The DB connection pool is
// expected to point at the main DB (where migrations 0042 lives).
func NewPgStore(pool *pgxpool.Pool) *PgStore {
	return &PgStore{pool: pool}
}

// Record satisfies chat.DecisionRecorder. The kb_id arrives as a string
// from the chat handler so the chat package doesn't need a uuid dep; the
// store parses it here. Parse failures log via context-attached slog
// (no Prom counter — at this volume one bad row vs one good row is
// indistinguishable noise) and silently drop, since the chat path is
// fire-and-forget anyway.
//
// AP-B4: toolCalls is the per-turn dispatch sequence; nil/empty means
// the turn didn't go through the MCP dispatcher (legacy paths or
// non-tool-aware orchestrators). Persists as a JSONB array via Insert.
func (s *PgStore) Record(ctx context.Context, kbID, mode, outcome string, hops, rounds, latencyMs int, toolCalls []ToolCallEntry) {
	id, err := uuid.Parse(strings.TrimSpace(kbID))
	if err != nil {
		logctx.From(ctx).Warn("agent_decisions.record: bad kb_id", "kb_id", kbID, "error", err)
		return
	}
	if err := s.Insert(ctx, DecisionRow{
		KbID:      id,
		Mode:      mode,
		Outcome:   outcome,
		Hops:      hops,
		Rounds:    rounds,
		LatencyMs: latencyMs,
		ToolCalls: toolCalls,
	}); err != nil {
		logctx.From(ctx).Warn("agent_decisions.record: insert failed", "error", err)
	}
}

// Insert appends one decision row. Caller is expected to invoke this in
// a fire-and-forget goroutine — the analytics value is statistical, so
// dropping a row on a transient DB hiccup must never propagate to the
// user-visible chat response.
//
// tool_calls is JSONB; we marshal the slice (or empty array for nil)
// before INSERT so the column never carries SQL NULL — the migration's
// DEFAULT '[]' covers existing rows; new rows go through this path
// and write '[]' when no calls happened. The frontend can therefore
// always treat tool_calls as a (possibly empty) array.
func (s *PgStore) Insert(ctx context.Context, r DecisionRow) error {
	calls := r.ToolCalls
	if calls == nil {
		calls = []ToolCallEntry{}
	}
	payload, err := json.Marshal(calls)
	if err != nil {
		return fmt.Errorf("agent_decisions: marshal tool_calls: %w", err)
	}
	const q = `
        INSERT INTO agent_decisions (kb_id, mode, outcome, hops, rounds, latency_ms, tool_calls)
        VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb)
    `
	_, err = s.pool.Exec(ctx, q, r.KbID, r.Mode, r.Outcome, r.Hops, r.Rounds, r.LatencyMs, payload)
	return err
}

// Aggregate returns the panel response for the given (kb_id, since) pair.
// kb_id may be nil to aggregate across all KBs; since is the window's
// inclusive lower bound.
//
// The implementation runs three queries — one for the per-mode outcome
// histogram, one for the median hops/rounds, one for p95 latency — all
// keyed on the same (kb_id, created_at) index. At expected production
// volumes (~10k rows/KB) the aggregate response should comfortably fit
// the 500ms ship-gate budget.
func (s *PgStore) Aggregate(ctx context.Context, kbID *uuid.UUID, since time.Time) (AgentMetricsResponse, error) {
	resp := AgentMetricsResponse{
		WindowSeconds: int(time.Since(since).Seconds()),
		AgenticChat:   DecisionDistribution{ByLabel: map[string]int{}},
		PlanExecute:   DecisionDistribution{ByLabel: map[string]int{}},
		CRAG:          DecisionDistribution{ByLabel: map[string]int{}},
	}
	if kbID != nil {
		resp.KbID = kbID.String()
	}

	// 1. Outcome distribution per mode.
	const histQuery = `
        SELECT mode, outcome, COUNT(*)
          FROM agent_decisions
         WHERE created_at >= $1
           AND ($2::uuid IS NULL OR kb_id = $2)
         GROUP BY mode, outcome
    `
	rows, err := s.pool.Query(ctx, histQuery, since, kbID)
	if err != nil {
		return resp, fmt.Errorf("agent_decisions histogram: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var mode, outcome string
		var count int
		if err := rows.Scan(&mode, &outcome, &count); err != nil {
			return resp, fmt.Errorf("agent_decisions scan: %w", err)
		}
		dist := bucketFor(&resp, mode)
		if dist == nil {
			continue
		}
		dist.Total += count
		dist.ByLabel[outcome] = count
	}
	if err := rows.Err(); err != nil {
		return resp, fmt.Errorf("agent_decisions rows: %w", err)
	}

	// 2. Median hops / median rounds. percentile_cont returns NULL when the
	//    grouping is empty, which scans into a sql.NullFloat64-style nil; the
	//    pgx driver gives us a default float64=0 if we use Scan(&f), but to
	//    distinguish "no data" from "median is 0" we use COALESCE(0).
	const percQuery = `
        SELECT COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY hops),   0)::float8,
               COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY rounds), 0)::float8,
               COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms), 0)::float8
          FROM agent_decisions
         WHERE created_at >= $1
           AND ($2::uuid IS NULL OR kb_id = $2)
    `
	// An unfiltered aggregate always returns one row, so any error here is a
	// real DB error — no ErrNoRows special case needed.
	if err := s.pool.QueryRow(ctx, percQuery, since, kbID).Scan(&resp.MedianHops, &resp.MedianRounds, &resp.P95LatencyMs); err != nil {
		return resp, fmt.Errorf("agent_decisions percentiles: %w", err)
	}

	// 3. AP-B4 tool-mix aggregate. jsonb_array_elements unrolls the
	// per-row tool_calls array into one row per call; we then group
	// by tool name and compute count + median duration + error rate.
	// Rows with empty tool_calls don't contribute (jsonb_array_elements
	// on '[]' yields zero rows).
	const toolMixQuery = `
        WITH calls AS (
          SELECT (elem->>'tool')::text          AS tool,
                 (elem->>'duration_ms')::int    AS duration_ms,
                 (elem->>'status')::text        AS status
            FROM agent_decisions,
                 jsonb_array_elements(tool_calls) elem
           WHERE created_at >= $1
             AND ($2::uuid IS NULL OR kb_id = $2)
        )
        SELECT tool,
               COUNT(*)::int,
               COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY duration_ms), 0)::float8,
               (SUM(CASE WHEN status <> 'ok' THEN 1 ELSE 0 END)::float8 / COUNT(*)::float8) AS error_rate
          FROM calls
         GROUP BY tool
         ORDER BY COUNT(*) DESC
    `
	mixRows, mErr := s.pool.Query(ctx, toolMixQuery, since, kbID)
	if mErr != nil {
		return resp, fmt.Errorf("agent_decisions tool_mix: %w", mErr)
	}
	defer mixRows.Close()
	for mixRows.Next() {
		var entry ToolMixEntry
		if err := mixRows.Scan(&entry.Tool, &entry.Calls, &entry.MedianDurationMs, &entry.ErrorRate); err != nil {
			return resp, fmt.Errorf("agent_decisions tool_mix scan: %w", err)
		}
		resp.ToolMix = append(resp.ToolMix, entry)
	}
	if err := mixRows.Err(); err != nil {
		return resp, fmt.Errorf("agent_decisions tool_mix rows: %w", err)
	}

	return resp, nil
}

func bucketFor(resp *AgentMetricsResponse, mode string) *DecisionDistribution {
	switch mode {
	case ModeAgentic:
		return &resp.AgenticChat
	case ModePlanExecute:
		return &resp.PlanExecute
	case ModeCRAG:
		return &resp.CRAG
	default:
		// Standard / unknown modes are intentionally not surfaced in the
		// panel response — they don't represent agent decisions worth
		// inspecting per-outcome. Returning nil tells the caller to skip.
		return nil
	}
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

// Handler exposes the GET /api/admin/agent-metrics endpoint.
type Handler struct {
	store Store
}

// NewHandler builds the Handler with the given store.
func NewHandler(store Store) *Handler {
	return &Handler{store: store}
}

// GetMetrics serves GET /api/admin/agent-metrics?window=1h|24h|7d&kb_id=<id>.
func (h *Handler) GetMetrics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	windowStr := strings.TrimSpace(r.URL.Query().Get("window"))
	d, ok := parseWindow(windowStr)
	if !ok {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid window; supported: 1h, 24h, 7d")
		return
	}

	var kbID *uuid.UUID
	if kbStr := strings.TrimSpace(r.URL.Query().Get("kb_id")); kbStr != "" {
		parsed, err := uuid.Parse(kbStr)
		if err != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid kb_id; must be a UUID")
			return
		}
		kbID = &parsed
	}

	since := time.Now().Add(-d)
	resp, err := h.store.Aggregate(ctx, kbID, since)
	if err != nil {
		logctx.From(ctx).Error("admin.agent_metrics: aggregate", "error", err)
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "aggregate failed")
		return
	}
	resp.WindowSeconds = int(d.Seconds())
	writeJSON(w, http.StatusOK, resp)
}

// parseWindow maps the user-facing window strings (1h, 24h, 7d) onto a
// time.Duration. Anything else returns ok=false so the handler can reject
// with a clear 400.
func parseWindow(s string) (time.Duration, bool) {
	switch s {
	case "", "1h":
		return time.Hour, true
	case "24h", "1d":
		return 24 * time.Hour, true
	case "7d", "1w":
		return 7 * 24 * time.Hour, true
	default:
		return 0, false
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
