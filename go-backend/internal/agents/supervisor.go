package agents

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/safego"
	"github.com/justrag/go-backend/internal/vector"
)

// supervisorRRFK is the reciprocal-rank-fusion constant used when
// merging multiple specialists' result lists. Matches the k=60 the
// retrieval pipeline uses in vector.FuseRRFWeighted so the two fusion
// stages behave consistently.
const supervisorRRFK = 60

// SupervisorTimeout is the per-specialist execute timeout. Mirrors the
// MCP per-tool timeout — one specialist call should never tie up the
// whole chat for longer than this.
const SupervisorTimeout = 12 * time.Second

// ErrNoSpecialistAvailable is returned by Supervisor.Run when no
// registered specialist matches the routing rule. Treated as a hard
// fail by the chat orchestrator (the supervisor can't proceed without
// at least one retrieval pass), but logged so the misconfiguration is
// visible.
var ErrNoSpecialistAvailable = errors.New("supervisor: no specialist available for routing")

// SupervisorResult is the aggregated output from one supervisor run:
// the union of all specialist-produced chunks (deduplicated by ID by
// the caller) plus the optional Notes payload (joined newline-
// separated when multiple specialists produce notes).
type SupervisorResult struct {
	Specialist string
	Chunks     []vector.SearchChunk
	Notes      string
}

// Supervisor coordinates specialist dispatch. v1 implements a simple
// rule-based router: enumeration intent → Enumerator; anything else →
// Retriever. The DAG-aware multi-specialist mode (one specialist per
// node) is reserved for a follow-up that actually exercises the
// per-node fan-out — v1 stays single-pass to keep the integration
// surface small.
type Supervisor struct {
	Retriever  Agent
	Enumerator Agent
	Classifier EnumerationClassifier
}

// NewSupervisor wires the supervisor's dependencies. A nil Classifier
// reduces routing to "always retriever", which is a reasonable
// fallback when the chat layer doesn't expose its enumeration
// classifier (e.g. unit tests).
func NewSupervisor(retriever, enumerator Agent, c EnumerationClassifier) *Supervisor {
	return &Supervisor{Retriever: retriever, Enumerator: enumerator, Classifier: c}
}

// Run dispatches one query to the specialist the routing rule
// selects. Returns the specialist name + its output. Caller is
// responsible for deduplicating chunks across multiple Run calls if
// they batch them.
func (s *Supervisor) Run(ctx context.Context, in Input) (SupervisorResult, error) {
	if s == nil || s.Retriever == nil {
		return SupervisorResult{}, ErrNoSpecialistAvailable
	}

	specialist := s.Retriever
	intent := "retriever"
	if s.Classifier != nil && s.Enumerator != nil && s.Classifier(in.Query, in.Language) {
		specialist = s.Enumerator
		intent = "enumerator"
	}

	dispatchCtx, cancel := context.WithTimeout(ctx, SupervisorTimeout)
	defer cancel()

	start := time.Now()
	out, err := specialist.Execute(dispatchCtx, in)
	latency := time.Since(start)
	observability.ObserveAgentExecutionSeconds(intent, latency.Seconds())

	if err != nil {
		observability.RecordAgentDispatch(intent, "error")
		logctx.From(ctx).Warn("supervisor: specialist returned error",
			"specialist", intent, "error", err, "latency_ms", latency.Milliseconds())
		return SupervisorResult{Specialist: intent}, fmt.Errorf("supervisor: %s: %w", intent, err)
	}
	observability.RecordAgentDispatch(intent, "ok")
	logctx.From(ctx).Info("supervisor.dispatch",
		"specialist", intent, "chunks", len(out.Chunks),
		"latency_ms", latency.Milliseconds())
	return SupervisorResult{
		Specialist: intent,
		Chunks:     out.Chunks,
		Notes:      out.Notes,
	}, nil
}

// RunMulti dispatches the relevant specialists in parallel and merges
// their results via reciprocal-rank fusion. The retriever always runs;
// the enumerator joins it when the enumeration classifier fires. This
// is the minimal-v1 of multi-specialist dispatch: with only two
// specialists, "enumeration queries get both" captures the compound-
// query win without a multi-label classifier or an extra LLM call.
//
// Fail-open: each specialist runs under its own SupervisorTimeout and a
// panic guard; as long as at least one specialist returns, RunMulti
// merges what it has. Only when every specialist fails does it return
// an error (the chat orchestrator then surfaces the failure). This is
// looser than Run, which hard-fails on its single specialist — the
// Agent contract documents that one specialist's error "does NOT abort
// the chat".
//
// The merge deduplicates by chunk ID (a chunk both specialists found is
// RRF-boosted) and preserves each chunk's original fields + score so
// downstream score-sensitive logic (IsLowConfidence,
// TruncateChunksToFit) still sees real reranker-scale scores.
func (s *Supervisor) RunMulti(ctx context.Context, in Input) (SupervisorResult, error) {
	if s == nil || s.Retriever == nil {
		return SupervisorResult{}, ErrNoSpecialistAvailable
	}

	specialists := []Agent{s.Retriever}
	if s.Classifier != nil && s.Enumerator != nil && s.Classifier(in.Query, in.Language) {
		specialists = append(specialists, s.Enumerator)
	}

	type dispatchResult struct {
		name string
		out  Output
		err  error
	}
	results := make([]dispatchResult, len(specialists))

	var wg sync.WaitGroup
	// i and sp are per-iteration variables (Go 1.22+ loop-scope semantics,
	// confirmed by the module's `go 1.26` directive), so the closure captures
	// each iteration's own values without explicit parameter passing — matching
	// the convention in chat/multipass.go and ai/rerank_qwen3.go.
	//
	// wg.Go (not safego.GoCtx) on purpose: a specialist panic must surface
	// as results[i].err so the failure-counting fallback below sees it —
	// RecoverError captures it as an error, whereas GoCtx would only log it
	// and leave results[i] looking like a silent success.
	for i, sp := range specialists {
		wg.Go(func() {
			defer safego.RecoverError(&results[i].err)

			// Per-specialist timeout; intentionally layers UNDER any parent
			// deadline on ctx — whichever expires first cancels the dispatch.
			dispatchCtx, cancel := context.WithTimeout(ctx, SupervisorTimeout)
			defer cancel()

			start := time.Now()
			out, err := sp.Execute(dispatchCtx, in)
			latency := time.Since(start)
			observability.ObserveAgentExecutionSeconds(sp.Name(), latency.Seconds())

			results[i] = dispatchResult{name: sp.Name(), out: out, err: err}
			if err != nil {
				observability.RecordAgentDispatch(sp.Name(), "error")
				logctx.From(ctx).Warn("supervisor: specialist returned error (multi)",
					"specialist", sp.Name(), "error", err, "latency_ms", latency.Milliseconds())
				return
			}
			observability.RecordAgentDispatch(sp.Name(), "ok")
		})
	}
	wg.Wait()

	var (
		lists    [][]vector.SearchChunk
		names    []string
		notes    []string
		firstErr error
		failures int
	)
	for _, r := range results {
		if r.err != nil {
			failures++
			if firstErr == nil {
				firstErr = r.err
			}
			continue
		}
		names = append(names, r.name)
		if len(r.out.Chunks) > 0 {
			lists = append(lists, r.out.Chunks)
		}
		if r.out.Notes != "" {
			notes = append(notes, r.out.Notes)
		}
	}

	if len(names) == 0 {
		return SupervisorResult{}, fmt.Errorf("supervisor: all %d specialist(s) failed: %w", len(specialists), firstErr)
	}

	merged := mergeSpecialistChunksRRF(supervisorRRFK, lists...)
	specialistLabel := strings.Join(names, "+")
	logctx.From(ctx).Info("supervisor.dispatch_multi",
		"specialists", specialistLabel,
		"dispatched", len(specialists),
		"failed", failures,
		"chunks", len(merged),
	)
	return SupervisorResult{
		Specialist: specialistLabel,
		Chunks:     merged,
		Notes:      strings.Join(notes, "\n"),
	}, nil
}

// mergeSpecialistChunksRRF fuses several specialists' ranked chunk
// lists with reciprocal rank fusion (score += 1/(k+rank+1) per list,
// mirroring vector.FuseRRFWeighted). Specialist outputs are already
// reranked by their own pipelines, so their absolute scores are not
// comparable across specialists — rank-based fusion is the correct,
// scale-invariant merge.
//
// Chunks are deduplicated by ID; a chunk appearing in multiple lists
// accumulates RRF contributions (so cross-specialist agreement floats
// to the top). The retained copy is the higher-Score one, so the
// SearchChunk handed downstream carries the stronger evidence's
// reranker score + full metadata (FileName, VectorScore, …). Output is
// ordered best-first by fused score, ties broken by ID for
// determinism.
func mergeSpecialistChunksRRF(k int, lists ...[]vector.SearchChunk) []vector.SearchChunk {
	if k < 1 {
		k = supervisorRRFK
	}
	type agg struct {
		chunk vector.SearchChunk
		score float64
	}
	merged := make(map[string]*agg)
	for _, list := range lists {
		for rank, c := range list {
			if c.ID == "" {
				continue
			}
			contrib := 1.0 / float64(k+rank+1)
			a, ok := merged[c.ID]
			if !ok {
				cp := c
				merged[c.ID] = &agg{chunk: cp, score: contrib}
				continue
			}
			a.score += contrib
			if c.Score > a.chunk.Score {
				a.chunk = c
			}
		}
	}
	if len(merged) == 0 {
		return nil
	}

	type scored struct {
		id    string
		score float64
	}
	ranked := make([]scored, 0, len(merged))
	for id, a := range merged {
		ranked = append(ranked, scored{id: id, score: a.score})
	}
	slices.SortFunc(ranked, func(a, b scored) int {
		return cmp.Or(
			cmp.Compare(b.score, a.score), // score descending
			cmp.Compare(a.id, b.id),       // id ascending, deterministic tie-break
		)
	})

	out := make([]vector.SearchChunk, 0, len(ranked))
	for _, r := range ranked {
		out = append(out, merged[r.id].chunk)
	}
	return out
}

// MergeChunksRRF is the exported form of mergeSpecialistChunksRRF for
// callers outside the supervisor (the agent-team orchestrator merges its
// specialists' chunk lists with the same rank fusion). k <= 0 falls back
// to supervisorRRFK.
func MergeChunksRRF(k int, lists ...[]vector.SearchChunk) []vector.SearchChunk {
	return mergeSpecialistChunksRRF(k, lists...)
}
