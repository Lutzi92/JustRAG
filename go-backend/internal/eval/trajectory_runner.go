package eval

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/chat"
	"github.com/justrag/go-backend/internal/prompts"
	"github.com/justrag/go-backend/internal/vector"
)

// TrajectoryMode is the orchestrator selector for `cmd/eval --trajectory`.
//
//   - "off"               — runs PrepareChatContext directly; CRAG branch
//     decisions are captured but no orchestrator
//     multi-step logic.
//   - "agentic"           — dispatches to RunAgenticChat.
//   - "plan_execute"      — dispatches to RunPlanExecuteChat with the legacy
//     flat planner.
//   - "plan_execute_dag"  — same as plan_execute but with chat_plan_execute_dag
//     forced on; exercises the Phase 3 §3.1 DAG planner.
//   - "supervisor"        — dispatches to RunSupervisorChat (Phase 3 §3.2).
//
// "all" expands to off + agentic + plan_execute + plan_execute_dag +
// supervisor so a single command produces a comparison set.
type TrajectoryMode string

const (
	TrajectoryModeOff            TrajectoryMode = "off"
	TrajectoryModeAgentic        TrajectoryMode = "agentic"
	TrajectoryModePlanExecute    TrajectoryMode = "plan_execute"
	TrajectoryModePlanExecuteDAG TrajectoryMode = "plan_execute_dag"
	TrajectoryModeSupervisor     TrajectoryMode = "supervisor"
)

// TrajectoryRunDeps is the dependency bag the trajectory eval runner needs.
// Mirrors the deps cmd/eval/main.go already wires; passing it through a
// struct lets the runner stay independent of the CLI's option-parsing
// layer and lets unit tests inject fakes.
type TrajectoryRunDeps struct {
	AIResolver     *ai.ConfigResolver
	SearchService  vector.Searcher
	SiteReader     chat.SiteConfigReader
	KbSystemPrompt func(ctx context.Context, kbID string) string // may be nil
	PlanningModel  string                                        // empty = inherit KB default
}

// RunTrajectory runs one question through one orchestrator mode and
// returns a TrajectoryRecord. The record's `events` slice is populated
// from the orchestrator's emit callback; `Score` is left nil — the
// caller decides whether to invoke the LLM-as-judge judges (write
// results back into Score before persisting).
func RunTrajectory(ctx context.Context, deps TrajectoryRunDeps, q Question, mode TrajectoryMode) TrajectoryRecord {
	rec := TrajectoryRecord{QuestionID: q.ID, Mode: string(mode)}
	var events []chat.TrajectoryEvent
	emit := CollectEmit(&events)

	kbSystemPrompt := ""
	if deps.KbSystemPrompt != nil {
		kbSystemPrompt = deps.KbSystemPrompt(ctx, q.KbID)
	}

	switch mode {
	case TrajectoryModeAgentic:
		params := chat.AgenticChatParams{
			KbID:           q.KbID,
			Query:          q.Question,
			Language:       q.Language,
			KbSystemPrompt: kbSystemPrompt,
			PlanningModel:  deps.PlanningModel,
			MaxHops:        chat.ChatAgenticMaxHops(ctx, deps.SiteReader),
			Plateau:        chat.ResolvePlateauConfig(ctx, deps.SiteReader),
		}
		if _, err := chat.RunAgenticChat(ctx, deps.AIResolver, deps.SearchService, params, emit); err != nil {
			events = append(events, chat.TrajectoryEvent{Stage: "answer", Decision: "orchestrator_error", Reason: err.Error()})
		}

	case TrajectoryModePlanExecute, TrajectoryModePlanExecuteDAG:
		params := chat.PlanExecuteParams{
			KbID:           q.KbID,
			Query:          q.Question,
			Language:       q.Language,
			KbSystemPrompt: kbSystemPrompt,
			PlanningModel:  deps.PlanningModel,
			MaxSubQueries:  chat.ChatPlanExecuteMaxSubQueries(ctx, deps.SiteReader),
			MaxIterations:  chat.ChatPlanExecuteMaxIterations(ctx, deps.SiteReader),
			TokenBudget:    chat.ChatPlanExecuteTokenBudget(ctx, deps.SiteReader),
			Plateau:        chat.ResolvePlateauConfig(ctx, deps.SiteReader),
			DAG:            mode == TrajectoryModePlanExecuteDAG,
			MaxDAGDepth:    chat.ChatPlanExecuteMaxDAGDepth(ctx, deps.SiteReader),
			MaxDAGNodes:    chat.ChatPlanExecuteMaxDAGNodes(ctx, deps.SiteReader),
		}
		if _, err := chat.RunPlanExecuteChat(ctx, deps.AIResolver, deps.SearchService, params, emit); err != nil {
			events = append(events, chat.TrajectoryEvent{Stage: "answer", Decision: "orchestrator_error", Reason: err.Error()})
		}

	case TrajectoryModeSupervisor:
		params := chat.SupervisorChatParams{
			KbID:           q.KbID,
			Query:          q.Question,
			Language:       q.Language,
			KbSystemPrompt: kbSystemPrompt,
			PlanningModel:  deps.PlanningModel,
		}
		if _, err := chat.RunSupervisorChat(ctx, deps.AIResolver, deps.SearchService, params, emit); err != nil {
			events = append(events, chat.TrajectoryEvent{Stage: "answer", Decision: "orchestrator_error", Reason: err.Error()})
		}

	case TrajectoryModeOff:
		params := chat.ChatContextParams{
			KbID:           q.KbID,
			SearchQuery:    q.Question,
			Language:       q.Language,
			KbSystemPrompt: kbSystemPrompt,
			QueryType:      q.QueryType,
			Emit:           emit,
		}
		if _, err := chat.PrepareChatContext(ctx, deps.AIResolver, deps.SearchService, deps.SiteReader, params); err != nil {
			events = append(events, chat.TrajectoryEvent{Stage: "answer", Decision: "prepare_error", Reason: err.Error()})
		}

	default:
		events = append(events, chat.TrajectoryEvent{Stage: "answer", Decision: "unknown_mode", Reason: string(mode)})
	}

	rec.Events = events
	return rec
}

// TrajectoryJudgeCompleter is the minimal LLM-call surface the trajectory
// judges need. The cmd/eval CLI's judgeCompleterAdapter satisfies it; tests
// inject deterministic stubs.
type TrajectoryJudgeCompleter interface {
	Complete(ctx context.Context, prompt, systemPrompt string) (string, error)
}

// JudgeTrajectory runs the trajectory judges (decomposition, decision,
// rewrite — Phase 1 §1.2; tool-call — Phase 2 §2.2) for one record and
// populates Score in place. Each judge is fail-open: a parse or LLM
// error logs an entry in JudgeErrors and leaves the corresponding
// metric nil so the aggregator can distinguish "judge skipped" from
// "judge said zero".
//
// goldAnswer and expectedTools are optional. Empty values still let
// the decision / rewrite / tool-call judges run with degraded
// discrimination ("is this defensible") rather than zero-score-ing
// the trajectory; that mirrors the way the rest of the eval treats
// missing ground truth.
func JudgeTrajectory(ctx context.Context, judge TrajectoryJudgeCompleter, lang, question, goldAnswer string, expectedTools []string, rec *TrajectoryRecord) {
	if rec == nil {
		return
	}
	score := &TrajectoryScore{}
	if planQueries := planSubQueries(rec.Events); len(planQueries) > 0 {
		s, err := runJudge01(ctx, judge,
			prompts.TrajectoryDecompositionSystemPrompt(lang),
			prompts.TrajectoryDecompositionUserPrompt(question, planQueries),
		)
		if err != nil {
			score.JudgeErrors = append(score.JudgeErrors, "decomposition: "+err.Error())
		} else {
			score.DecompositionCoverage = &s
		}
	}

	decision := agentDecisionLabel(rec.Events)
	if decision != "" {
		s, err := runJudge01(ctx, judge,
			prompts.TrajectoryDecisionSystemPrompt(lang),
			prompts.TrajectoryDecisionUserPrompt(question, goldAnswer, decision),
		)
		if err != nil {
			score.JudgeErrors = append(score.JudgeErrors, "decision: "+err.Error())
		} else {
			score.DecisionCorrectness = &s
		}
	}

	if rewrite := agentRewrite(rec.Events); rewrite != "" {
		s, err := runJudge01(ctx, judge,
			prompts.TrajectoryRewriteSystemPrompt(lang),
			prompts.TrajectoryRewriteUserPrompt(question, rewrite),
		)
		if err != nil {
			score.JudgeErrors = append(score.JudgeErrors, "rewrite: "+err.Error())
		} else {
			score.RewriteUtility = &s
		}
	}

	// Phase 2 §2.2: tool-call correctness. Always run when at least one
	// tool call is observable in the trajectory; also run with an empty
	// call list when the gold-truth ExpectedTools is non-empty so a
	// "should-have-called-X-but-didn't" failure is still scored.
	calls := agentToolCalls(rec.Events)
	if len(calls) > 0 || len(expectedTools) > 0 {
		s, err := runJudge01(ctx, judge,
			prompts.TrajectoryToolCallSystemPrompt(lang),
			prompts.TrajectoryToolCallUserPrompt(question, expectedTools, calls),
		)
		if err != nil {
			score.JudgeErrors = append(score.JudgeErrors, "tool_call: "+err.Error())
		} else {
			score.ToolCallCorrectness = &s
		}
	}

	if score.DecompositionCoverage != nil || score.DecisionCorrectness != nil || score.RewriteUtility != nil || score.ToolCallCorrectness != nil || len(score.JudgeErrors) > 0 {
		rec.Score = score
	}
}

// runJudge01 invokes the judge LLM with the given prompts and parses the
// strict-JSON `{"score":0..1}` response shape. Out-of-range scores are
// clamped to [0,1].
func runJudge01(ctx context.Context, judge TrajectoryJudgeCompleter, sysPrompt, userPrompt string) (float64, error) {
	out, err := judge.Complete(ctx, userPrompt, sysPrompt)
	if err != nil {
		return 0, err
	}
	out = strings.TrimSpace(out)
	// Some local LLMs wrap JSON in ```json fences; strip them defensively.
	out = strings.TrimPrefix(out, "```json")
	out = strings.TrimPrefix(out, "```")
	out = strings.TrimSuffix(out, "```")
	out = strings.TrimSpace(out)

	var parsed struct {
		Score     float64 `json:"score"`
		Rationale string  `json:"rationale"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		return 0, fmt.Errorf("parse judge JSON: %w (raw=%q)", err, out)
	}
	if parsed.Score < 0 {
		return 0, nil
	}
	if parsed.Score > 1 {
		return 1, nil
	}
	return parsed.Score, nil
}

// planSubQueries extracts the planner's sub-query list from a trajectory.
// Returns the queries from the first `plan` stage event with a non-empty
// Queries slice.
func planSubQueries(events []chat.TrajectoryEvent) []string {
	for _, e := range events {
		if e.Stage == "plan" && len(e.Queries) > 0 {
			return e.Queries
		}
	}
	return nil
}

// agentDecisionLabel maps the orchestrator's terminal `answer`-stage
// decision to a token class the judge prompt expects:
// "answered" / "abstained" / "rewrote_then_answered". CRAG abstain
// trumps the orchestrator's outcome; CRAG rewrite-then-proceed trumps
// the plain "answered" label.
func agentDecisionLabel(events []chat.TrajectoryEvent) string {
	rewrote := false
	for _, e := range events {
		if e.Stage == "decision" {
			switch e.Decision {
			case "crag_abstain":
				return "abstained"
			case "crag_rewrite", "crag_rewrite_complete", "crag_proceed_after_rewrite":
				rewrote = true
			}
		}
	}
	for _, e := range events {
		if e.Stage == "answer" {
			if e.Decision == "abstain" {
				return "abstained"
			}
			if rewrote {
				return "rewrote_then_answered"
			}
			return "answered"
		}
	}
	if rewrote {
		return "rewrote_then_answered"
	}
	return ""
}

// agentRewrite returns the rewritten query a CRAG rewrite branch produced,
// or empty if no rewrite happened. The query is carried on the
// `crag_rewrite_complete` decision event (Phase 1 §1.1 semantics).
func agentRewrite(events []chat.TrajectoryEvent) string {
	for _, e := range events {
		if e.Stage == "decision" && e.Decision == "crag_rewrite_complete" && e.Query != "" {
			return e.Query
		}
	}
	return ""
}

// agentToolCalls walks the trajectory and extracts MCP tool-call events
// (Phase 2 §2.1: stage=decision, decision=agent_tool_call). The result
// is the small {tool, args_summary} list the Phase 2 §2.2 judge consumes.
// Returns nil when the trajectory contains no tool calls; the judge
// caller treats that the same as `[]` semantically.
func agentToolCalls(events []chat.TrajectoryEvent) []prompts.ToolCallSummary {
	var out []prompts.ToolCallSummary
	for _, e := range events {
		if e.Stage != "decision" || e.Decision != "agent_tool_call" {
			continue
		}
		// Reason is the tool name set by the orchestrator (e.g.
		// "kb_search"). The full args are not yet surfaced on the
		// trajectory event — the judge gets a one-line summary built
		// from Findings count + reason. When richer args become
		// available (e.g. via a structured `tool_args` field on the
		// event), this helper is the single place to extend.
		summary := ""
		if e.Findings > 0 {
			summary = fmtFindings(e.Findings)
		}
		out = append(out, prompts.ToolCallSummary{
			Tool:        e.Reason,
			ArgsSummary: summary,
		})
	}
	return out
}

func fmtFindings(n int) string {
	if n == 1 {
		return "returned 1 chunk"
	}
	return "returned " + itoa(n) + " chunks"
}

// itoa is a small dependency-free integer formatter.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	negative := n < 0
	if negative {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if negative {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// WriteTrajectoryJSONL writes a slice of TrajectoryRecord, one per line,
// in newline-delimited JSON. Caller owns the file handle.
func WriteTrajectoryJSONL(w io.Writer, records []TrajectoryRecord) error {
	bw := bufio.NewWriter(w)
	enc := json.NewEncoder(bw)
	for i, r := range records {
		if err := enc.Encode(r); err != nil {
			return fmt.Errorf("encode record %d: %w", i, err)
		}
	}
	return bw.Flush()
}

// WriteTrajectoryReport writes the trajectory records and an aggregate
// summary to two paths. recordsPath is JSONL; aggregatePath is a JSON
// object keyed by orchestrator mode.
func WriteTrajectoryReport(recordsPath, aggregatePath string, records []TrajectoryRecord) error {
	rf, err := os.Create(recordsPath)
	if err != nil {
		return fmt.Errorf("create records file: %w", err)
	}
	if werr := WriteTrajectoryJSONL(rf, records); werr != nil {
		_ = rf.Close()
		return werr
	}
	if cerr := rf.Close(); cerr != nil {
		return fmt.Errorf("close records file: %w", cerr)
	}

	af, err := os.Create(aggregatePath)
	if err != nil {
		return fmt.Errorf("create aggregate file: %w", err)
	}
	defer af.Close()
	enc := json.NewEncoder(af)
	enc.SetIndent("", "  ")
	if err := enc.Encode(AggregateTrajectory(records)); err != nil {
		return fmt.Errorf("encode aggregate: %w", err)
	}
	return nil
}
