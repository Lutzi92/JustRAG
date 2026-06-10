package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/prompts"
)

// CritiqueAction is the closed enum the AP-D3 critic emits per call.
type CritiqueAction string

const (
	CritiqueActionContinue   CritiqueAction = "continue"
	CritiqueActionAddNodes   CritiqueAction = "add_nodes"
	CritiqueActionPruneNodes CritiqueAction = "prune_nodes"
	CritiqueActionAnswerNow  CritiqueAction = "answer_now"
)

// CritiqueResult is the structured output of one critic LLM call.
// On parse failure or LLM error the chat layer treats it as
// `continue` (no-op) — the original plan keeps executing. That
// fail-open posture is the same as everywhere else in the
// post-response stack.
type CritiqueResult struct {
	Action       CritiqueAction `json:"action"`
	NewNodes     []PlanNode     `json:"new_nodes,omitempty"`
	PruneNodeIDs []string       `json:"prune_node_ids,omitempty"`
	Reason       string         `json:"reason,omitempty"`
}

// critiqueDAGSpec is the Structured-Outputs contract for the AP-D3
// critic, matching the prompt's documented verdict shape. new_nodes
// items carry exactly the fields normaliseCritiqueResult copies
// (id/query/depends_on/reason — never tool/args); the action enum
// mirrors the CritiqueAction constants. parseCritiqueDAGJSON stays
// tolerant for the json_object fallback path.
var critiqueDAGSpec = &StructuredSpec{
	Name: "critique_dag_verdict",
	Schema: json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {"type": "string", "enum": ["continue", "add_nodes", "prune_nodes", "answer_now"]},
			"new_nodes": {
				"type": "array",
				"items": {
					"type": "object",
					"properties": {
						"id": {"type": "string"},
						"query": {"type": "string"},
						"depends_on": {"type": "array", "items": {"type": "string"}},
						"reason": {"type": "string"}
					},
					"required": ["id", "query", "depends_on", "reason"],
					"additionalProperties": false
				}
			},
			"prune_node_ids": {"type": "array", "items": {"type": "string"}},
			"reason": {"type": "string"}
		},
		"required": ["action", "new_nodes", "prune_node_ids", "reason"],
		"additionalProperties": false
	}`),
}

// CritiqueDAGProgress runs one inter-level critic call. The plan
// + completed-node findings + pending-node list go into the
// prompt; the LLM picks one of four actions. The chat layer's
// dag_executor consumes the result and adjusts the plan for the
// next level.
//
// modelOverride empty → resolver default. Operators usually
// point this at a small/fast model — the critic is
// classification, not reasoning.
//
// Defensive normalisation:
//   - unrecognised action coerces to "continue" (safest no-op)
//   - new_nodes whose ID collides with an existing plan node drop
//   - new_nodes with empty query or empty ID drop
//   - prune_node_ids that match completed-node IDs drop (can only
//     prune what hasn't run)
//
// existingNodeIDs is the union of original-plan + already-added
// node IDs; new_nodes can only add IDs NOT in this set.
// completedNodeIDs is what's already run; prune_node_ids that
// match here are no-ops and get filtered (the LLM occasionally
// asks to prune a node that just finished).
func CritiqueDAGProgress(
	ctx context.Context,
	resolver *ConfigResolver,
	question string,
	completed []prompts.CritiqueDAGCompletedNode,
	pending []prompts.CritiqueDAGPendingNode,
	existingNodeIDs map[string]bool,
	completedNodeIDs map[string]bool,
	kbID, lang, modelOverride string,
) (CritiqueResult, error) {
	start := time.Now()
	defer func() {
		observability.ObserveCritiqueDAGSeconds(time.Since(start).Seconds())
	}()

	sys := prompts.CritiqueDAGSystemPrompt(lang)
	user := prompts.CritiqueDAGUserPrompt(question, completed, pending)

	res, err := resolver.structuredCompletionFn(ctx, user, sys, kbID, modelOverride, critiqueDAGSpec)
	if err != nil {
		observability.RecordCritiqueDAGAction("error")
		logctx.From(ctx).Warn("critique_dag: completion failed; treating as continue", "error", err)
		return CritiqueResult{Action: CritiqueActionContinue}, fmt.Errorf("critique_dag: completion: %w", err)
	}
	if res == nil || strings.TrimSpace(res.Content) == "" {
		observability.RecordCritiqueDAGAction("error")
		return CritiqueResult{Action: CritiqueActionContinue}, fmt.Errorf("critique_dag: empty completion")
	}

	parsed, perr := parseCritiqueDAGJSON(res.Content)
	if perr != nil {
		observability.RecordCritiqueDAGAction("error")
		logctx.From(ctx).Warn("critique_dag: parse failed; treating as continue",
			"error", perr, "raw", firstNChars(res.Content, 240))
		return CritiqueResult{Action: CritiqueActionContinue}, fmt.Errorf("critique_dag: parse: %w", perr)
	}

	out := normaliseCritiqueResult(parsed, existingNodeIDs, completedNodeIDs)

	observability.RecordCritiqueDAGAction(string(out.Action))
	logctx.From(ctx).Info("rag.critique_dag",
		"stage", "critique_dag",
		"action", out.Action,
		"new_nodes", len(out.NewNodes),
		"prune_ids", len(out.PruneNodeIDs),
		"duration_ms", time.Since(start).Milliseconds(),
	)
	return out, nil
}

// normaliseCritiqueResult enforces the closed-enum + ID-uniqueness
// contract on the LLM's output. Tested in critique_dag_test.go.
func normaliseCritiqueResult(in CritiqueResult, existingIDs, completedIDs map[string]bool) CritiqueResult {
	out := CritiqueResult{Reason: strings.TrimSpace(in.Reason)}

	switch CritiqueAction(strings.ToLower(strings.TrimSpace(string(in.Action)))) {
	case CritiqueActionContinue, CritiqueActionAddNodes, CritiqueActionPruneNodes, CritiqueActionAnswerNow:
		out.Action = CritiqueAction(strings.ToLower(strings.TrimSpace(string(in.Action))))
	default:
		// Unknown action coerces to continue — the safest no-op.
		out.Action = CritiqueActionContinue
		return out
	}

	if out.Action == CritiqueActionAddNodes {
		seen := make(map[string]bool, len(in.NewNodes))
		for _, n := range in.NewNodes {
			id := strings.TrimSpace(n.ID)
			query := strings.TrimSpace(n.Query)
			if id == "" || query == "" {
				continue
			}
			if existingIDs[id] || seen[id] {
				continue
			}
			seen[id] = true
			deps := make([]string, 0, len(n.DependsOn))
			for _, d := range n.DependsOn {
				d = strings.TrimSpace(d)
				if d == "" {
					continue
				}
				// Dependency must be on an EXISTING node — pending
				// nodes can't be cited as parents because they
				// haven't run yet, and the recursive-CTE topology
				// computation would need them in the levels above
				// the new node, which we can't guarantee.
				if !existingIDs[d] && !seen[d] {
					continue
				}
				deps = append(deps, d)
			}
			out.NewNodes = append(out.NewNodes, PlanNode{
				ID:        id,
				Query:     query,
				DependsOn: deps,
				Reason:    strings.TrimSpace(n.Reason),
			})
		}
		// Cap at 2 per call (matches prompt) — defence against
		// runaway add_nodes growth even though width-cap catches
		// it later.
		if len(out.NewNodes) > 2 {
			out.NewNodes = out.NewNodes[:2]
		}
		// If normalisation dropped every new node, demote action
		// to continue so the executor doesn't spin a no-op
		// "add_nodes with empty list" branch.
		if len(out.NewNodes) == 0 {
			out.Action = CritiqueActionContinue
		}
	}

	if out.Action == CritiqueActionPruneNodes {
		seen := make(map[string]bool, len(in.PruneNodeIDs))
		for _, id := range in.PruneNodeIDs {
			id = strings.TrimSpace(id)
			if id == "" || seen[id] {
				continue
			}
			// Don't allow pruning completed nodes — they already
			// contributed findings. The LLM occasionally asks for
			// this when it thinks an earlier node was redundant
			// in hindsight; the executor honours only forward-
			// looking prunes.
			if completedIDs[id] {
				continue
			}
			seen[id] = true
			out.PruneNodeIDs = append(out.PruneNodeIDs, id)
		}
		if len(out.PruneNodeIDs) == 0 {
			out.Action = CritiqueActionContinue
		}
	}

	return out
}

func parseCritiqueDAGJSON(text string) (CritiqueResult, error) {
	var out CritiqueResult
	trimmed := strings.TrimSpace(text)
	if err := json.Unmarshal([]byte(trimmed), &out); err == nil {
		return out, nil
	}
	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start >= 0 && end > start {
		if err := json.Unmarshal([]byte(trimmed[start:end+1]), &out); err == nil {
			return out, nil
		}
	}
	return CritiqueResult{}, fmt.Errorf("not valid JSON: %s", firstNChars(text, 120))
}
