package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/justrag/go-backend/internal/aibudget"
	"github.com/justrag/go-backend/internal/prompts"
)

// planQueriesResponse is the shape PlanQueries expects from the LLM.
type planQueriesResponse struct {
	Queries   []string `json:"queries"`
	Rationale string   `json:"rationale"`
}

// planQueriesSpec is the Structured-Outputs contract for the flat
// planner, matching planQueriesResponse. parsePlanQueriesJSON stays
// tolerant for the json_object fallback path.
var planQueriesSpec = &StructuredSpec{
	Name: "plan_queries",
	Schema: json.RawMessage(`{
		"type": "object",
		"properties": {
			"queries": {"type": "array", "items": {"type": "string"}},
			"rationale": {"type": "string"}
		},
		"required": ["queries", "rationale"],
		"additionalProperties": false
	}`),
}

// planQueriesDAGSpec is the Structured-Outputs contract for the
// non-tool-aware DAG planner. Node items carry exactly the fields the
// PlanQueriesDAGSystemPrompt documents (id/query/depends_on/reason);
// PlanNode's Tool/Args fields belong to the tool-aware variant, which
// cannot use strict mode (see PlanQueriesDAGToolAware) and therefore
// never sends this spec.
var planQueriesDAGSpec = &StructuredSpec{
	Name: "plan_queries_dag",
	Schema: json.RawMessage(`{
		"type": "object",
		"properties": {
			"nodes": {
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
			"rationale": {"type": "string"}
		},
		"required": ["nodes", "rationale"],
		"additionalProperties": false
	}`),
}

// PlanNode is one entry in the Phase 3 §3.1 DAG-shaped plan. Each node
// names a sub-query and its dependencies on other nodes' outputs.
// Nodes whose `DependsOn` is empty are independent — they execute in
// parallel; a node with non-empty `DependsOn` waits for its parents to
// finish so the parents' top chunk can be interpolated into the
// child's query.
//
// AP-B3 fields: Tool and Args make the node tool-aware. Empty Tool
// means "use the legacy kb_search runner" — preserves backward
// compatibility for plans produced by the non-tool-aware planner.
// When Tool is set, the executor dispatches through the MCP registry
// instead of vector.Search; Args is forwarded verbatim as the tool's
// JSON arguments.
type PlanNode struct {
	ID        string         `json:"id"`
	Query     string         `json:"query"`
	DependsOn []string       `json:"depends_on,omitempty"`
	Reason    string         `json:"reason,omitempty"`
	Tool      string         `json:"tool,omitempty"`
	Args      map[string]any `json:"args,omitempty"`
}

// Plan is the structured shape PlanQueriesDAG returns. The caller
// (DAG executor) is responsible for cycle detection and topological
// scheduling — this struct is just the wire/LLM-output format.
type Plan struct {
	Nodes     []PlanNode `json:"nodes"`
	Rationale string     `json:"rationale"`
}

// FlattenToFlatPlan converts a flat sub-query list (the legacy
// PlanQueries shape) into a no-edges DAG. Used when the DAG planner
// is disabled or has degraded to fallback — the executor still gets a
// uniform Plan input.
func FlattenToFlatPlan(queries []string, rationale string) Plan {
	nodes := make([]PlanNode, len(queries))
	for i, q := range queries {
		nodes[i] = PlanNode{
			ID:    fmt.Sprintf("n%d", i+1),
			Query: q,
		}
	}
	return Plan{Nodes: nodes, Rationale: rationale}
}

// PlanQueriesDAG decomposes a complex_reasoning question into a DAG of
// sub-queries via a single LLM call. The returned Plan may have:
//   - independent nodes (DependsOn empty) the executor runs in parallel;
//   - dependent nodes whose query references the parent's top chunk via
//     deterministic interpolation at execute time.
//
// Cycle detection is the executor's job — this function does NOT
// validate the graph; LLM output may emit cycles (rare in practice but
// observed). The caller's expected behavior is: log
// `rag_plan_dag_invalid_total` and fall back to FlattenToFlatPlan.
//
// modelOverride empty → resolver default. Token usage from the
// completion is recorded against aibudget.Add(ctx, ...) when an
// aibudget counter is attached to ctx; no-op otherwise.
func PlanQueriesDAG(ctx context.Context, resolver *ConfigResolver, kbID, question, language, modelOverride string) (Plan, error) {
	sys := prompts.PlanQueriesDAGSystemPrompt(language)
	user := prompts.PlanQueriesUserPrompt(question)

	res, err := resolver.structuredCompletionFn(ctx, user, sys, kbID, modelOverride, planQueriesDAGSpec)
	if err != nil {
		return Plan{}, fmt.Errorf("plan_queries_dag: completion: %w", err)
	}
	if res == nil || res.Content == "" {
		return Plan{}, fmt.Errorf("plan_queries_dag: empty completion")
	}
	aibudget.Add(ctx, res.PromptTokens+res.CompletionTokens)

	parsed, perr := parsePlanDAGJSON(res.Content)
	if perr != nil {
		return Plan{}, fmt.Errorf("plan_queries_dag: parse: %w", perr)
	}

	// Defensive normalization: trim whitespace, drop nodes with empty
	// IDs or queries (the LLM occasionally emits placeholders).
	out := Plan{Rationale: strings.TrimSpace(parsed.Rationale)}
	for _, n := range parsed.Nodes {
		id := strings.TrimSpace(n.ID)
		query := strings.TrimSpace(n.Query)
		if id == "" || query == "" {
			continue
		}
		dep := make([]string, 0, len(n.DependsOn))
		for _, d := range n.DependsOn {
			if t := strings.TrimSpace(d); t != "" {
				dep = append(dep, t)
			}
		}
		out.Nodes = append(out.Nodes, PlanNode{
			ID:        id,
			Query:     query,
			DependsOn: dep,
			Reason:    strings.TrimSpace(n.Reason),
			Tool:      strings.TrimSpace(n.Tool),
			Args:      n.Args,
		})
	}
	return out, nil
}

// PlanQueriesDAGToolAware is the AP-B3 variant that asks the LLM to
// pick a tool per node from a supplied catalog. The catalog is
// rendered into the system prompt as `name: description` lines so
// the LLM sees the same shape it'd see in MCP's `tools/list`.
//
// Validation against the catalog (Tool name exists, required args
// present) happens at dispatch time — the executor catches mistakes
// and falls back to the legacy runner. This keeps the validator
// simple here: the LLM gets one chance per call, the executor is
// the safety net.
//
// modelOverride empty → resolver default. Same aibudget accounting
// as PlanQueriesDAG.
func PlanQueriesDAGToolAware(ctx context.Context, resolver *ConfigResolver, kbID, question, language, modelOverride string, toolCatalog []prompts.PlanToolEntry) (Plan, error) {
	sys := prompts.PlanQueriesDAGToolAwareSystemPrompt(language, toolCatalog)
	user := prompts.PlanQueriesUserPrompt(question)

	// Deliberately NOT structured: each node's `args` is a free-form
	// per-tool JSON object (map[string]any). Strict-mode json_schema
	// requires `additionalProperties: false` on every object level,
	// which would force args to be empty and break calculator /
	// sql_query / chunk_read nodes whose arguments carry the work.
	res, err := resolver.completionFn(ctx, user, sys, kbID, modelOverride)
	if err != nil {
		return Plan{}, fmt.Errorf("plan_queries_dag_tool_aware: completion: %w", err)
	}
	if res == nil || res.Content == "" {
		return Plan{}, fmt.Errorf("plan_queries_dag_tool_aware: empty completion")
	}
	aibudget.Add(ctx, res.PromptTokens+res.CompletionTokens)

	parsed, perr := parsePlanDAGJSON(res.Content)
	if perr != nil {
		return Plan{}, fmt.Errorf("plan_queries_dag_tool_aware: parse: %w", perr)
	}

	out := Plan{Rationale: strings.TrimSpace(parsed.Rationale)}
	for _, n := range parsed.Nodes {
		id := strings.TrimSpace(n.ID)
		query := strings.TrimSpace(n.Query)
		// Tool-aware mode: query may legitimately be empty for tools
		// like calculator (the args carry the work). Only require ID.
		if id == "" {
			continue
		}
		dep := make([]string, 0, len(n.DependsOn))
		for _, d := range n.DependsOn {
			if t := strings.TrimSpace(d); t != "" {
				dep = append(dep, t)
			}
		}
		out.Nodes = append(out.Nodes, PlanNode{
			ID:        id,
			Query:     query,
			DependsOn: dep,
			Reason:    strings.TrimSpace(n.Reason),
			Tool:      strings.TrimSpace(n.Tool),
			Args:      n.Args,
		})
	}
	return out, nil
}

func parsePlanDAGJSON(text string) (Plan, error) {
	var out Plan
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
	return Plan{}, fmt.Errorf("not valid JSON: %s", firstNChars(text, 120))
}

// PlanQueries decomposes a complex_reasoning question into 1..N
// independent sub-queries via a single LLM call. Returns the (possibly
// empty) sub-query list, a short rationale, and any error.
//
// Empty queries list: the LLM said "no decomposition possible" — caller
// treats this as a Plan-stage stub and falls back to a single-query
// baseline search using the original question.
//
// modelOverride empty → resolver default. Token usage from the
// completion is recorded against aibudget.Add(ctx, ...) when an
// aibudget counter is attached to ctx; no-op otherwise.
func PlanQueries(ctx context.Context, resolver *ConfigResolver, kbID, question, language, modelOverride string) ([]string, string, error) {
	sys := prompts.PlanQueriesSystemPrompt(language)
	user := prompts.PlanQueriesUserPrompt(question)

	res, err := resolver.structuredCompletionFn(ctx, user, sys, kbID, modelOverride, planQueriesSpec)
	if err != nil {
		return nil, "", fmt.Errorf("plan_queries: completion: %w", err)
	}
	if res == nil || res.Content == "" {
		return nil, "", fmt.Errorf("plan_queries: empty completion")
	}
	aibudget.Add(ctx, res.PromptTokens+res.CompletionTokens)

	parsed, perr := parsePlanQueriesJSON(res.Content)
	if perr != nil {
		return nil, "", fmt.Errorf("plan_queries: parse: %w", perr)
	}

	out := make([]string, 0, len(parsed.Queries))
	for _, q := range parsed.Queries {
		if t := strings.TrimSpace(q); t != "" {
			out = append(out, t)
		}
	}
	return out, strings.TrimSpace(parsed.Rationale), nil
}

func parsePlanQueriesJSON(text string) (planQueriesResponse, error) {
	var out planQueriesResponse
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
	return planQueriesResponse{}, fmt.Errorf("not valid JSON: %s", firstNChars(text, 120))
}
