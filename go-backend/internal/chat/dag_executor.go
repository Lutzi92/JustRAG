package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/safego"
	"github.com/justrag/go-backend/internal/vector"
)

// ErrPlanCycle is returned when the LLM-generated plan contains a cycle
// (self-reference, two-cycle, or transitive). Callers fall back to the
// flat-plan executor and increment `rag_plan_dag_invalid_total`. The
// alternative — hard failing the chat request — would surface a rare
// LLM hiccup as a 5xx; the user is better served by a degraded plan.
var ErrPlanCycle = errors.New("dag: plan contains a cycle")

// ErrPlanTooDeep is returned when a plan's longest chain exceeds the
// admin-configured depth cap. Mirrors ErrPlanCycle: caller falls back.
var ErrPlanTooDeep = errors.New("dag: plan exceeds max depth")

// ErrPlanTooWide is returned when a plan has more nodes than the
// admin-configured node cap.
var ErrPlanTooWide = errors.New("dag: plan exceeds max nodes")

// DAGSearchResult captures the per-node retrieval outcome the executor
// returns up to plan-execute. The chunks are merged into the
// orchestrator's accumulator via the existing dedup helper; the
// excerpt is stashed for child-node interpolation.
type DAGSearchResult struct {
	NodeID     string
	Query      string
	Chunks     []vector.SearchChunk
	TopExcerpt string // first ~300 chars of the highest-scored chunk
	Err        error
}

// DAGExecResult is the executor's full output: per-node results plus
// the metrics the caller needs to feed observability. The structure
// stays ordered by topo-level (level 0 first) so the trajectory event
// can render an ASCII tree of the actual execution path.
type DAGExecResult struct {
	Nodes         []DAGSearchResult
	MaxDepth      int
	ParallelNodes int // max number of nodes that ran simultaneously
}

// DAGExecuteParams collects the executor's inputs. Kept as a struct so
// the call site (plan-execute) doesn't drown in positional args; new
// knobs (e.g. per-node timeout) get a field without rippling through.
type DAGExecuteParams struct {
	Plan          ai.Plan
	KbID          string
	FileIDs       []string
	PlanningModel string
	MaxNodes      int
	MaxDepth      int
	// ToolRunner, when non-nil, is preferred over the legacy runNodeFn
	// for nodes that specify a Tool field other than "" or "kb_search".
	// Lets the AP-B3 tool-aware planner dispatch through the MCP
	// registry. Backward compatible: when ToolRunner is nil OR the
	// node didn't set Tool, every node still goes through runNodeFn
	// exactly as before.
	ToolRunner DAGToolRunner
	// Critic, when non-nil, is the AP-D3 inter-level critic. It
	// runs AFTER each completed level (except the last — no
	// pending nodes to critique). Critic decisions can add or
	// prune nodes (subject to width / depth caps) or short-circuit
	// the executor with `answer_now`. Nil disables the critic
	// path entirely; legacy plans run unchanged.
	//
	// Question is the user's original question; the executor
	// passes it through to the critic so prompt assembly stays
	// in one place.
	Critic   DAGCritic
	Question string
}

// DAGToolRunner is the executor's escape hatch into the MCP registry.
// The chat package wires it to MCPDispatcher.Dispatch in production
// and a fake in tests. Returns SearchChunks because that's the type
// the plan-execute accumulator expects; tools that produce non-chunk
// output (calculator's Text, sql_query's Structured) wrap their
// payload in a synthetic chunk before returning so the rest of the
// pipeline doesn't need a special case.
type DAGToolRunner interface {
	RunTool(ctx context.Context, kbID, tool string, args map[string]any, query string) ([]vector.SearchChunk, error)
}

// DAGCritic is the AP-D3 inter-level critic interface. The executor
// calls it AFTER each completed level (except the last — no point
// critiquing when no work remains) with the question, the plan
// state, and the per-node findings. Production wires
// ai.CritiqueDAGProgress; tests inject a fake.
//
// On any error the executor treats the result as
// `CritiqueActionContinue` — the original plan keeps running.
// The fail-open posture is the same as everywhere else in the
// post-response stack.
type DAGCritic interface {
	Critique(ctx context.Context, question string, completed []DAGCompletedNode, pending []DAGPendingNode, existingIDs, completedIDs map[string]bool) (DAGCritiqueDecision, error)
}

// DAGCompletedNode is the chat-package shape of the completed-node
// summary the critic sees. Mirrors prompts.CritiqueDAGCompletedNode
// across the package boundary so chat doesn't import prompts for
// the interface contract.
type DAGCompletedNode struct {
	NodeID     string
	Query      string
	TopExcerpt string
	Err        bool
}

// DAGPendingNode is the chat-package shape of the pending-node
// summary the critic sees.
type DAGPendingNode struct {
	NodeID string
	Query  string
}

// DAGCritiqueDecision is the executor-side view of one critic
// result. Action mirrors ai.CritiqueAction; the chat package can
// switch on the string without importing ai.
type DAGCritiqueDecision struct {
	Action       string // "continue" | "add_nodes" | "prune_nodes" | "answer_now"
	NewNodes     []ai.PlanNode
	PruneNodeIDs []string
	Reason       string
}

// runNodeFn is the per-node retrieval shape. Production wires it to
// vector.SearchService.Search via the searchInvoker interface; tests
// inject deterministic stubs.
type runNodeFn func(ctx context.Context, query string, fileIDs []string, planningModel string) ([]vector.SearchChunk, error)

// ExecuteDAG runs every node in the plan respecting the dependency
// edges. Returns ErrPlanCycle / ErrPlanTooDeep / ErrPlanTooWide when
// the graph fails validation, in which case `result` is zero-valued
// and the caller falls back to flat-plan execution.
//
// Within a level (set of nodes whose dependencies have all completed),
// nodes run in parallel via errgroup. A failed node short-circuits the
// level (returns the error from errgroup.Wait); siblings already
// running finish their work but their results are discarded — same
// semantics as a stdlib errgroup, deliberately not papered over.
func ExecuteDAG(ctx context.Context, params DAGExecuteParams, run runNodeFn) (DAGExecResult, error) {
	plan := params.Plan
	if len(plan.Nodes) == 0 {
		return DAGExecResult{}, nil
	}
	maxNodes := params.MaxNodes
	if maxNodes <= 0 {
		maxNodes = 6
	}
	maxDepth := params.MaxDepth
	if maxDepth <= 0 {
		maxDepth = 3
	}
	if len(plan.Nodes) > maxNodes {
		return DAGExecResult{}, fmt.Errorf("%w: %d nodes (max %d)", ErrPlanTooWide, len(plan.Nodes), maxNodes)
	}

	levels, err := topoLevels(plan.Nodes)
	if err != nil {
		return DAGExecResult{}, err
	}
	if len(levels) > maxDepth {
		return DAGExecResult{}, fmt.Errorf("%w: depth=%d, max=%d", ErrPlanTooDeep, len(levels), maxDepth)
	}

	// Track the widest level for observability — operators want to see
	// "how parallel does our planner actually go".
	parallelNodes := 0
	for _, lvl := range levels {
		if len(lvl) > parallelNodes {
			parallelNodes = len(lvl)
		}
	}

	results := make(map[string]DAGSearchResult, len(plan.Nodes))
	var resultsMu sync.Mutex
	ordered := make([]DAGSearchResult, 0, len(plan.Nodes))
	// AP-D3 bookkeeping: completed-node IDs feed the critic's
	// `completedIDs` map (it can't ask to prune an already-run
	// node), and existing-node IDs feed the dedupe path that
	// prevents the critic from emitting a node ID that collides
	// with the original plan.
	completedNodeIDs := make(map[string]bool, len(plan.Nodes))
	existingNodeIDs := make(map[string]bool, len(plan.Nodes))
	for _, n := range plan.Nodes {
		existingNodeIDs[n.ID] = true
	}

	budget := TurnBudgetFromContext(ctx)
	// AP-D3: index-based loop so the critic can mutate `levels`
	// mid-flight (insert a new level after the current one when
	// add_nodes fires; filter pending levels when prune_nodes
	// fires). Range-over-slice would freeze the slice header at
	// loop start.
	answerNow := false
	for levelIdx := 0; levelIdx < len(levels); levelIdx++ {
		lvl := levels[levelIdx]
		if len(lvl) == 0 {
			// Critic-prune may have emptied a level. Skip cleanly.
			continue
		}
		// AP-A3: budget check between topological levels. Within a
		// level the parallel goroutines all share the same budget
		// snapshot — checking once before the level dispatch is the
		// right granularity (a per-node check would just race against
		// itself). On exhaustion we stop dispatching new levels and
		// return what we've accumulated; the caller's finalize step
		// handles partial results.
		if kind := budget.CheckExceeded(ctx, "plan_execute"); kind != "" {
			break
		}
		// Fail-fast at the level boundary: errgroup.WithContext cancels
		// gctx as soon as any node returns a non-nil error, so sibling
		// nodes still in flight on this level observe context.Canceled and
		// abort early. That's intentional — a level is treated as failed
		// the moment one node fails, and finalize() below still returns the
		// partial results accumulated so far. If per-node best-effort
		// (run every sibling to completion regardless of peers) is ever
		// wanted, swap errgroup for a sync.WaitGroup + per-node error slice.
		g, gctx := errgroup.WithContext(ctx)
		// Build every dependent-node interpolation BEFORE launching any
		// sibling goroutine: buildNodeQuery reads the shared results map
		// unsynchronized, and the goroutines launched below write to it
		// under resultsMu. Interleaving the build with the launches would
		// race a map read against a sibling's map write. Deterministic —
		// no extra LLM call. Parents may have errored on an earlier
		// level; in that case we proceed with whatever excerpts we do
		// have (a missing parent excerpt just means the original query
		// gets passed through verbatim).
		queries := make([]string, len(lvl))
		for i, node := range lvl {
			queries[i] = buildNodeQuery(node, results)
		}
		for i, node := range lvl {
			query := queries[i]
			g.Go(func() (rerr error) {
				// errgroup.Go has no panic recovery — a nil deref deep in
				// the search path would crash the process mid-plan. Convert
				// any panic into an error that g.Wait() then surfaces
				// through the existing search_error branch below.
				defer safego.RecoverError(&rerr)

				// AP-B3: tool-aware dispatch. When the planner picked a
				// non-default tool AND the executor has a ToolRunner, go
				// through the MCP registry. Otherwise fall back to the
				// legacy kb_search runner. The "kb_search" name short-
				// circuits to the legacy runner too — it's the same code
				// path with one less indirection.
				var chunks []vector.SearchChunk
				toolLabel := node.Tool
				if toolLabel == "" {
					toolLabel = "kb_search"
				}
				if params.ToolRunner != nil && node.Tool != "" && node.Tool != "kb_search" {
					chunks, rerr = params.ToolRunner.RunTool(gctx, params.KbID, node.Tool, node.Args, query)
				} else {
					chunks, rerr = run(gctx, query, params.FileIDs, params.PlanningModel)
				}
				observability.RecordPlanDAGToolDistribution(toolLabel)
				excerpt := topExcerpt(chunks)
				resultsMu.Lock()
				results[node.ID] = DAGSearchResult{
					NodeID:     node.ID,
					Query:      query,
					Chunks:     chunks,
					TopExcerpt: excerpt,
					Err:        rerr,
				}
				resultsMu.Unlock()
				return rerr
			})
		}
		if werr := g.Wait(); werr != nil {
			// One node failed: surface to the caller so it can decide
			// whether to bail or proceed with whatever accumulated. We
			// still record the failure under `result="search_error"`.
			observability.RecordPlanDAGNode("search_error")
			logctx.From(ctx).Warn("dag: node search failed",
				"level", levelIdx, "error", werr)
			return DAGExecResult{
				Nodes:         finalize(plan.Nodes, results),
				MaxDepth:      len(levels),
				ParallelNodes: parallelNodes,
			}, werr
		}
		// From here on `results` is read without resultsMu (finalize() at
		// return, and the critic's results[n.ID] reads below). That is safe:
		// g.Wait() above establishes a happens-before edge with every
		// goroutine's resultsMu-guarded write, so all writes from this level
		// are visible to this goroutine. resultsMu is only needed to guard the
		// concurrent writes *during* a level's execution.
		//
		// Mark this level's nodes as completed. Done OUTSIDE g.Wait
		// because the critic needs the full set, and the goroutine
		// loop sets results[node.ID] directly without touching this
		// map (which only the critic reads).
		for _, n := range lvl {
			completedNodeIDs[n.ID] = true
		}

		// AP-D3 inter-level critic. Fires after EVERY completed
		// level — even the last one — because `add_nodes` can
		// extend the plan from the final level just as easily as
		// from an earlier one. The cost is one extra LLM call per
		// turn in the steady-continue case; the upside is that the
		// critic can rescue an under-decomposed plan that would
		// otherwise stop short.
		if params.Critic != nil {
			completed := make([]DAGCompletedNode, 0, len(completedNodeIDs))
			for _, n := range plan.Nodes {
				if !completedNodeIDs[n.ID] {
					continue
				}
				r := results[n.ID]
				completed = append(completed, DAGCompletedNode{
					NodeID:     n.ID,
					Query:      r.Query,
					TopExcerpt: r.TopExcerpt,
					Err:        r.Err != nil,
				})
			}
			pending := make([]DAGPendingNode, 0)
			for i := levelIdx + 1; i < len(levels); i++ {
				for _, n := range levels[i] {
					pending = append(pending, DAGPendingNode{NodeID: n.ID, Query: n.Query})
				}
			}
			decision, cerr := params.Critic.Critique(ctx, params.Question, completed, pending, existingNodeIDs, completedNodeIDs)
			if cerr != nil {
				// Fail-open: keep going with the original plan.
				logctx.From(ctx).Warn("dag critic: failed; treating as continue", "error", cerr)
			} else {
				switch decision.Action {
				case "answer_now":
					answerNow = true
				case "add_nodes":
					if len(plan.Nodes)+len(decision.NewNodes) > maxNodes {
						observability.RecordCritiqueDAGAction("cap_violation")
						logctx.From(ctx).Info("dag critic: add_nodes rejected by width cap",
							"existing", len(plan.Nodes), "requested", len(decision.NewNodes), "cap", maxNodes)
						break
					}
					if len(levels)+1 > maxDepth {
						observability.RecordCritiqueDAGAction("cap_violation")
						logctx.From(ctx).Info("dag critic: add_nodes rejected by depth cap",
							"current_depth", len(levels), "cap", maxDepth)
						break
					}
					// Append to plan; insert a new level right
					// after the current one so the new nodes run
					// at the next iteration. The new nodes'
					// depends_on can reference any completed node,
					// and `buildNodeQuery` will interpolate the
					// parent excerpts via the existing helper.
					plan.Nodes = append(plan.Nodes, decision.NewNodes...)
					for _, n := range decision.NewNodes {
						existingNodeIDs[n.ID] = true
					}
					inserted := append([][]ai.PlanNode{decision.NewNodes}, levels[levelIdx+1:]...)
					levels = append(levels[:levelIdx+1], inserted...)
					observability.AddCritiqueDAGNodesAdded(len(decision.NewNodes))
					if len(decision.NewNodes) > parallelNodes {
						parallelNodes = len(decision.NewNodes)
					}
				case "prune_nodes":
					pruneSet := make(map[string]bool, len(decision.PruneNodeIDs))
					for _, id := range decision.PruneNodeIDs {
						pruneSet[id] = true
					}
					prunedCount := 0
					for i := levelIdx + 1; i < len(levels); i++ {
						filtered := levels[i][:0]
						for _, n := range levels[i] {
							if pruneSet[n.ID] {
								prunedCount++
								continue
							}
							filtered = append(filtered, n)
						}
						levels[i] = filtered
					}
					observability.AddCritiqueDAGNodesPruned(prunedCount)
				case "continue":
					// Default — no-op. Already counted by
					// RecordCritiqueDAGAction in the AI layer.
				}
			}
		}

		if answerNow {
			break
		}
	}

	// All nodes done — compose the ordered output in topo order so the
	// trajectory's ASCII tree matches execution order.
	for _, lvl := range levels {
		for _, n := range lvl {
			r := results[n.ID]
			ordered = append(ordered, r)
			if r.Err == nil {
				observability.RecordPlanDAGNode("succeeded")
			}
		}
	}

	return DAGExecResult{
		Nodes:         ordered,
		MaxDepth:      len(levels),
		ParallelNodes: parallelNodes,
	}, nil
}

// topoLevels returns the plan's nodes grouped by topological level —
// level 0 nodes have no dependencies, level i+1 nodes depend on at
// least one level-i (or earlier) node and on no later nodes.
//
// Cycle detection is integrated: if the algorithm makes no progress
// while unvisited nodes remain, return ErrPlanCycle. Self-references
// trip the same check on the first pass.
//
// We do NOT use Kahn's algorithm verbatim because we want stable
// ordering within a level (input order) so trajectory rendering is
// deterministic — Kahn's ready-set iteration order is map-iteration,
// which Go intentionally randomizes.
func topoLevels(nodes []ai.PlanNode) ([][]ai.PlanNode, error) {
	if len(nodes) == 0 {
		return nil, nil
	}
	// Map for O(1) ID validation + parent lookup.
	byID := make(map[string]ai.PlanNode, len(nodes))
	for _, n := range nodes {
		if _, dup := byID[n.ID]; dup {
			return nil, fmt.Errorf("dag: duplicate node id %q", n.ID)
		}
		byID[n.ID] = n
	}
	// Reject references to non-existent nodes — this is a planner bug.
	for _, n := range nodes {
		for _, d := range n.DependsOn {
			if _, ok := byID[d]; !ok {
				return nil, fmt.Errorf("dag: node %q references missing parent %q", n.ID, d)
			}
			if d == n.ID {
				return nil, fmt.Errorf("%w: self-reference at %q", ErrPlanCycle, n.ID)
			}
		}
	}

	visited := make(map[string]bool, len(nodes))
	var levels [][]ai.PlanNode
	for len(visited) < len(nodes) {
		var current []ai.PlanNode
		for _, n := range nodes { // input order → stable trajectory
			if visited[n.ID] {
				continue
			}
			ready := true
			for _, d := range n.DependsOn {
				if !visited[d] {
					ready = false
					break
				}
			}
			if ready {
				current = append(current, n)
			}
		}
		if len(current) == 0 {
			return nil, ErrPlanCycle
		}
		for _, n := range current {
			visited[n.ID] = true
		}
		levels = append(levels, current)
	}
	return levels, nil
}

// buildNodeQuery interpolates parent-node excerpts into a child node's
// query string. For dependent nodes the prefix becomes:
//
//	"Given that <parent_excerpt>, <node.query>"
//
// Multiple parents are joined with "; ". Empty parent excerpts (parent
// returned 0 chunks) are skipped — better to send the original query
// than to send "Given that , …" which would just confuse retrieval.
func buildNodeQuery(node ai.PlanNode, results map[string]DAGSearchResult) string {
	if len(node.DependsOn) == 0 {
		return node.Query
	}
	excerpts := make([]string, 0, len(node.DependsOn))
	for _, d := range node.DependsOn {
		r, ok := results[d]
		if !ok || r.TopExcerpt == "" {
			continue
		}
		excerpts = append(excerpts, r.TopExcerpt)
	}
	if len(excerpts) == 0 {
		return node.Query
	}
	return "Given that " + strings.Join(excerpts, "; ") + ", " + node.Query
}

// topExcerpt returns the first ~300 characters of the highest-scored
// chunk in the slice. Used as the parent-node payload that the child
// query interpolates. 300 is enough to anchor the dependent search
// without ballooning the LLM context.
func topExcerpt(chunks []vector.SearchChunk) string {
	if len(chunks) == 0 {
		return ""
	}
	// Caller hands us the search result in score-descending order
	// (vector.SearchService produces this), so chunks[0] is the top.
	c := chunks[0]
	const maxBytes = 300
	if len(c.Content) <= maxBytes {
		return c.Content
	}
	return c.Content[:maxBytes] + "…"
}

// finalize is the failure-path helper: when one node errors mid-DAG,
// we still want to surface the partial results to the caller (the
// orchestrator can choose to use the chunks that did arrive). Builds
// a slice in input-node order with empty entries for nodes that never
// ran.
func finalize(nodes []ai.PlanNode, results map[string]DAGSearchResult) []DAGSearchResult {
	out := make([]DAGSearchResult, 0, len(nodes))
	for _, n := range nodes {
		if r, ok := results[n.ID]; ok {
			out = append(out, r)
		} else {
			out = append(out, DAGSearchResult{NodeID: n.ID, Query: n.Query})
		}
	}
	return out
}
