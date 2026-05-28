package eval

import (
	"encoding/json"

	"github.com/justrag/go-backend/internal/chat"
)

// TrajectoryRecord is one row of a trajectory eval JSONL output. One record
// per (question, orchestrator) tuple. The `--trajectory` mode of cmd/eval
// runs each question through one or more orchestrators (off / agentic /
// plan_execute) and persists the full event sequence so offline analysis
// can correlate decision-level mistakes with retrieval outcomes.
type TrajectoryRecord struct {
	QuestionID    string                     `json:"question_id"`
	Mode          string                     `json:"mode"` // "off" | "agentic" | "plan_execute"
	Events        []chat.TrajectoryEvent     `json:"events"`
	FinalAnswer   string                     `json:"final_answer,omitempty"`
	GoldAnswer    string                     `json:"gold_answer,omitempty"`
	RetrievalHits map[string]bool            `json:"retrieval_hits,omitempty"`
	Score         *TrajectoryScore           `json:"score,omitempty"`
	JudgeRaw      map[string]json.RawMessage `json:"judge_raw,omitempty"`
}

// TrajectoryScore is the LLM-as-judge verdict on the agent's decisions for
// one question. Each metric is in [0, 1]; nil means "judge skipped /
// errored on that metric". Tool-call correctness is reserved for Phase 2 §
// 2.2 — leave it nil until the MCP layer ships.
type TrajectoryScore struct {
	DecompositionCoverage *float64 `json:"decomposition_coverage,omitempty"`
	DecisionCorrectness   *float64 `json:"decision_correctness,omitempty"`
	RewriteUtility        *float64 `json:"rewrite_utility,omitempty"`
	ToolCallCorrectness   *float64 `json:"tool_call_correctness,omitempty"`
	Verdict               string   `json:"verdict,omitempty"`
	JudgeErrors           []string `json:"judge_errors,omitempty"`
}

// TrajectoryAggregate summarizes TrajectoryScores across questions for one
// orchestrator mode. Median + per-route would be the natural extension but
// for the minimum-viable output we ship simple means + a count of skipped
// metrics so the sample size is visible.
type TrajectoryAggregate struct {
	Mode                      string  `json:"mode"`
	Count                     int     `json:"count"`
	MeanDecompositionCoverage float64 `json:"mean_decomposition_coverage"`
	MeanDecisionCorrectness   float64 `json:"mean_decision_correctness"`
	MeanRewriteUtility        float64 `json:"mean_rewrite_utility"`
	MeanToolCallCorrectness   float64 `json:"mean_tool_call_correctness"`
	NumScored                 int     `json:"num_scored"`
}

// CollectEmit returns an emit-callback compatible with the orchestrator
// signature that appends every `agentTrajectory` payload to the supplied
// slice. Non-trajectory events (legacy `agenticHop` etc.) are ignored —
// the new envelope is the source of truth.
func CollectEmit(out *[]chat.TrajectoryEvent) func(map[string]any) {
	return func(d map[string]any) {
		if v, ok := d["agentTrajectory"]; ok {
			if t, ok := v.(chat.TrajectoryEvent); ok {
				*out = append(*out, t)
			}
		}
	}
}

// AggregateTrajectory walks a slice of TrajectoryRecord per orchestrator
// mode and returns one TrajectoryAggregate per mode. Records without a
// score (judge skipped) are counted in `Count` but not in `NumScored`.
func AggregateTrajectory(records []TrajectoryRecord) map[string]TrajectoryAggregate {
	by := make(map[string]TrajectoryAggregate)
	type sums struct {
		decomp, decision, rewrite, toolcall     float64
		nDecomp, nDecision, nRewrite, nToolCall int
	}
	totals := make(map[string]*sums)

	for _, r := range records {
		agg, ok := by[r.Mode]
		if !ok {
			agg = TrajectoryAggregate{Mode: r.Mode}
		}
		agg.Count++
		by[r.Mode] = agg

		if r.Score == nil {
			continue
		}
		t, ok := totals[r.Mode]
		if !ok {
			t = &sums{}
			totals[r.Mode] = t
		}
		if r.Score.DecompositionCoverage != nil {
			t.decomp += *r.Score.DecompositionCoverage
			t.nDecomp++
		}
		if r.Score.DecisionCorrectness != nil {
			t.decision += *r.Score.DecisionCorrectness
			t.nDecision++
		}
		if r.Score.RewriteUtility != nil {
			t.rewrite += *r.Score.RewriteUtility
			t.nRewrite++
		}
		if r.Score.ToolCallCorrectness != nil {
			t.toolcall += *r.Score.ToolCallCorrectness
			t.nToolCall++
		}
		if r.Score.DecompositionCoverage != nil || r.Score.DecisionCorrectness != nil || r.Score.RewriteUtility != nil || r.Score.ToolCallCorrectness != nil {
			agg = by[r.Mode]
			agg.NumScored++
			by[r.Mode] = agg
		}
	}
	for mode, t := range totals {
		agg := by[mode]
		if t.nDecomp > 0 {
			agg.MeanDecompositionCoverage = t.decomp / float64(t.nDecomp)
		}
		if t.nDecision > 0 {
			agg.MeanDecisionCorrectness = t.decision / float64(t.nDecision)
		}
		if t.nRewrite > 0 {
			agg.MeanRewriteUtility = t.rewrite / float64(t.nRewrite)
		}
		if t.nToolCall > 0 {
			agg.MeanToolCallCorrectness = t.toolcall / float64(t.nToolCall)
		}
		by[mode] = agg
	}
	return by
}
