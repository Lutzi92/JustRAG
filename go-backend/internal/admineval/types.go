package admineval

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// CreateRunRequest is the JSON body for POST /api/admin/eval/runs.
// All fields are optional; missing required fields are resolved from site_configs.
type CreateRunRequest struct {
	KBID         *uuid.UUID `json:"kb_id,omitempty"`
	GoldenSetID  *uuid.UUID `json:"golden_set_id,omitempty"`
	JudgeEnabled *bool      `json:"judge_enabled,omitempty"`
	TopK         *int       `json:"top_k,omitempty"`
	Label        string     `json:"label,omitempty"`
	// TeamID dispatches the eval run through a user-created agent team
	// instead of the standard orchestrator-dispatch adapter. Teams are
	// KB-scoped, so TeamID requires an explicit KBID on the same request
	// (see CreateRun validation) — it cannot rely on eval_default_kb_id.
	TeamID *uuid.UUID `json:"team_id,omitempty"`
}

// CreateRunResponse is the 201 body returned by POST /api/admin/eval/runs.
type CreateRunResponse struct {
	ID        uuid.UUID `json:"id"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

// AggregateSummary is a compact view of aggregate metrics embedded in RunSummary.
type AggregateSummary struct {
	Count      int     `json:"count"`
	MeanRecall float64 `json:"mean_recall"`
	MRR        float64 `json:"mrr"`
}

// RunSummary is a single row in the ListRunsResponse.
type RunSummary struct {
	ID              uuid.UUID          `json:"id"`
	Label           string             `json:"label"`
	Status          string             `json:"status"`
	CreatedAt       time.Time          `json:"created_at"`
	StartedAt       *time.Time         `json:"started_at,omitempty"`
	FinishedAt      *time.Time         `json:"finished_at,omitempty"`
	KBID            uuid.UUID          `json:"kb_id"`
	KBName          string             `json:"kb_name,omitempty"`
	JudgeEnabled    bool               `json:"judge_enabled"`
	Aggregate       *AggregateSummary  `json:"aggregate,omitempty"`
	RouteMeanRecall map[string]float64 `json:"route_mean_recall,omitempty"`
	ErrorMessage    string             `json:"error_message,omitempty"`
}

// ListRunsResponse is the body returned by GET /api/admin/eval/runs.
type ListRunsResponse struct {
	Runs  []RunSummary `json:"runs"`
	Total int          `json:"total"`
}

// GoldenSetSummary is a single row in the ListGoldenSetsResponse (content omitted).
type GoldenSetSummary struct {
	ID            uuid.UUID `json:"id"`
	Name          string    `json:"name"`
	Description   string    `json:"description,omitempty"`
	ContentHash   string    `json:"content_hash"`
	QuestionCount int       `json:"question_count"`
	CreatedAt     time.Time `json:"created_at"`
}

// CreateGoldenSetResponse is the 201 body returned by POST /api/admin/eval/golden-sets.
type CreateGoldenSetResponse struct {
	ID            uuid.UUID `json:"id"`
	Name          string    `json:"name"`
	ContentHash   string    `json:"content_hash"`
	QuestionCount int       `json:"question_count"`
	CreatedAt     time.Time `json:"created_at"`
}

// ListGoldenSetsResponse is the body returned by GET /api/admin/eval/golden-sets.
type ListGoldenSetsResponse struct {
	GoldenSets []GoldenSetSummary `json:"golden_sets"`
}

// summarize parses the minimal aggregate fields out of stored eval.Report JSON.
// Returns (nil, nil) when the report is missing (run in-flight or failed).
func summarize(raw json.RawMessage) (*AggregateSummary, map[string]float64) {
	if len(raw) == 0 {
		return nil, nil
	}
	var rep struct {
		Aggregate struct {
			Count      int     `json:"count"`
			MeanRecall float64 `json:"mean_recall"`
			MRR        float64 `json:"mrr"`
		} `json:"aggregate"`
		RouteAggregates map[string]struct {
			MeanRecall float64 `json:"mean_recall"`
		} `json:"route_aggregates"`
	}
	if err := json.Unmarshal(raw, &rep); err != nil {
		return nil, nil
	}
	out := &AggregateSummary{
		Count:      rep.Aggregate.Count,
		MeanRecall: rep.Aggregate.MeanRecall,
		MRR:        rep.Aggregate.MRR,
	}
	routes := make(map[string]float64, len(rep.RouteAggregates))
	for k, v := range rep.RouteAggregates {
		routes[k] = v.MeanRecall
	}
	return out, routes
}
