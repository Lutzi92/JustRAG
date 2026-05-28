package eval

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/chat"
	"github.com/justrag/go-backend/internal/vector"
)

// chatContextProvider is the subset of adapter behaviour the in-process
// judge loop relies on. Both ProductionContextAdapter and
// OrchestratorDispatchAdapter satisfy it; the latter falls back to its
// embedded ProductionContextAdapter for standard-branch questions and
// returns (nil, false) for orchestrator-branch questions (no full
// ChatContext is constructed in that case).
type chatContextProvider interface {
	Searcher
	ChatContextForQuestion(questionID string) (*chat.ChatContext, bool)
	ContentsForQuestion(questionID string, k int) (contents []string, fileNames []string, ok bool)
}

// RunDeps holds the runtime dependencies needed to execute a production-context
// eval in-process. It mirrors the set cmd/eval/main.go wires when constructing
// a ProductionContextAdapter and the judge-mode completers.
//
// The runner constructs a private SearchService per run so that installing
// the frozen SnapshotConfigReader does NOT mutate the process-wide shared
// SearchService used by live chat traffic (which would be a data race and
// would redirect all concurrent searches to the eval snapshot for the entire
// run duration).
//
// EmbeddingCache may be nil — the eval runner intentionally does not share
// the live cache so embedding calls made during eval are isolated and do not
// pollute the cache warmed by production traffic.
type RunDeps struct {
	AIResolver     *ai.ConfigResolver
	VectorDB       *pgxpool.Pool
	MainDB         *pgxpool.Pool
	EmbeddingCache *ai.EmbeddingCache // may be nil; eval runs are isolated
	GoldenSetStore *GoldenSetStore    // loads the named fixture from the DB
	// KbSystemPrompt may be nil. When set, the orchestrator-dispatch
	// adapter calls it to resolve the KB system prompt for plan-execute /
	// supervisor / agentic dispatch. When nil, orchestrators run without
	// a KB system prompt — eval still works, but the LLM context drifts
	// slightly from production where the prompt is always present.
	KbSystemPrompt func(ctx context.Context, kbID string) string
}

// applyRunKBOverride sets every question's KbID to runKB in place.
// See RunInProcessFromRecord for the rationale.
func applyRunKBOverride(qs []Question, runKB string) {
	for i := range qs {
		qs[i].KbID = runKB
	}
}

// RunInProcessFromRecord runs a full production-context eval based on the
// fields captured in an eval.Run row and returns the marshaled Report JSON.
// Dependencies are injected by the caller (the worker wires them at dispatch).
//
// The production adapter always uses zero EvalFlags (no enhance, no HyDE, no
// multi-query, no enumeration override) so KB site-config governs all knobs via
// the frozen SnapshotConfigReader — exactly the "no extra flags" behaviour that
// in-app eval is meant to exercise.
func RunInProcessFromRecord(ctx context.Context, r Run, deps RunDeps) (json.RawMessage, error) {
	// 1. Load golden set from the DB record referenced by the run row.
	if r.GoldenSetID == nil {
		return nil, fmt.Errorf("run %s has no golden_set_id", r.ID)
	}
	gs, err := deps.GoldenSetStore.Get(ctx, *r.GoldenSetID)
	if err != nil {
		return nil, fmt.Errorf("load golden set: %w", err)
	}
	questions, err := ParseGoldenSetContent(gs.Content)
	if err != nil {
		return nil, fmt.Errorf("parse golden set content: %w", err)
	}

	// eval_runs.kb_id (the KB the operator picked in the UI) is the
	// authoritative target for an in-app run — it overrides the per-question
	// kb_id baked into the stored golden set so the same fixture can be
	// re-evaluated against a different KB (e.g. one re-ingested with a new
	// embedder) without re-authoring the JSON.
	applyRunKBOverride(questions, r.KBID.String())

	// 2. Parse ConfigSnapshot into the frozen site-config map.
	var snapValues map[string]*string
	if len(r.ConfigSnapshot) > 0 && string(r.ConfigSnapshot) != "null" {
		if err := json.Unmarshal(r.ConfigSnapshot, &snapValues); err != nil {
			return nil, fmt.Errorf("parse config snapshot: %w", err)
		}
	}
	siteReader := NewSnapshotConfigReader(snapValues)

	// 3. Construct a private SearchService for this eval run and wire the
	//    frozen config reader into it. Using a private instance (not the
	//    process-wide shared one) prevents two problems:
	//      a) The shared service's siteConfig field would be visible to all
	//         concurrent searches for the duration of the run (12–35 min),
	//         silently routing live traffic through the snapshot.
	//      b) Writing s.siteConfig without synchronization while concurrent
	//         reads occur is a Go data race (flagged by go test -race).
	svc := vector.NewSearchService(deps.VectorDB, deps.MainDB, deps.AIResolver,
		vector.WithEmbeddingCache(deps.EmbeddingCache),
		vector.WithSiteConfigReader(siteReader),
	)

	// 4. Build the orchestrator-dispatch adapter so the eval mirrors
	//    production dispatch (Supervisor / Plan-Execute / Agentic /
	//    standard fallback). EvalFlags are zero: no --enhance, no
	//    --hyde, no --multi-query, no --enumeration override. CRAG and
	//    other pipeline knobs come from the frozen site-config snapshot,
	//    and orchestrator selection follows the snapshot's gate values.
	adapter := NewOrchestratorDispatchAdapter(
		deps.AIResolver,
		svc,
		siteReader,
		deps.KbSystemPrompt,
		EvalFlags{}, // all zero — KB config governs
	)

	// 5. Run retrieval eval (concurrency=1 for the worker path).
	topK := r.TopK
	if topK <= 0 {
		topK = 10
	}

	judgeEnabled := r.JudgeEnabled

	if !judgeEnabled {
		rep, err := RunEval(ctx, adapter, questions, topK, 1)
		if err != nil {
			return nil, fmt.Errorf("run eval: %w", err)
		}
		rep.GoldenPath = gs.Name
		return json.Marshal(rep)
	}

	// 6. Judge path — mirrors cmd/eval/main.go's judge loop.
	//    The production-adapter path (ChatContextForQuestion) is used so the
	//    LLM answer is generated from the exact system prompt and context text
	//    that the chat handler would produce (production parity).
	rep, err := runEvalWithProductionJudge(ctx, adapter, questions, topK, deps.AIResolver)
	if err != nil {
		return nil, fmt.Errorf("run eval with judge: %w", err)
	}
	rep.GoldenPath = gs.Name
	return json.Marshal(rep)
}

// runEvalWithProductionJudge mirrors the judge loop from cmd/eval/main.go
// exactly, using the production-context adapter's ChatContextForQuestion to
// obtain the pre-assembled system prompt + context for answer generation.
// This keeps full production parity (same prompt, same chunks) without an
// extra retrieval pass.
func runEvalWithProductionJudge(
	ctx context.Context,
	adapter chatContextProvider,
	questions []Question,
	k int,
	resolver *ai.ConfigResolver,
) (Report, error) {
	// First run retrieval-only eval to populate the adapter's cache.
	rep, err := RunEval(ctx, adapter, questions, k, 1)
	if err != nil {
		return rep, err
	}

	// Now run the judge loop, mirroring cmd/eval/main.go lines 161–198.
	for i := range rep.Questions {
		qr := &rep.Questions[i]
		if qr.Error != "" {
			continue
		}
		chatCtx, hit := adapter.ChatContextForQuestion(qr.Question.ID)
		if !hit || chatCtx == nil {
			qr.Judge = &JudgeMetrics{JudgeErrors: []string{"no cached ChatContext for question"}}
			continue
		}

		// Production parity: run the LLM against the exact system prompt and
		// context text that the chat handler would have used.
		answerAdapter := &inprocAnswerCompleterForKB{resolver: resolver, kbID: qr.Question.KbID}
		userPrompt := fmt.Sprintf("%s\n\nQUESTION:\n%s", chatCtx.Context, qr.Question.Question)
		answer, answerErr := answerAdapter.Complete(ctx, userPrompt, chatCtx.SystemPrompt)
		if answerErr != nil {
			qr.Judge = &JudgeMetrics{JudgeErrors: []string{"answer: " + answerErr.Error()}}
			continue
		}

		contents, _, ok := adapter.ContentsForQuestion(qr.Question.ID, k)
		if !ok {
			contents = nil
		}

		judgeAdapter := &inprocJudgeCompleterForKB{resolver: resolver, kbID: qr.Question.KbID}
		jm := NewJudge(judgeAdapter).Evaluate(ctx, qr.Question, answer, qr.Retrieved, contents)
		qr.Judge = &jm
	}

	// Re-aggregate so judge means populate.
	rep.Aggregate = Aggregate(rep.Questions, k)
	rep.RouteAggregates = AggregateByRoute(rep.Questions, k)
	rep.OrchestratorAggregates = AggregateByOrchestrator(rep.Questions, k)
	return rep, nil
}

// ---------------------------------------------------------------------------
// internal completer adapters (mirror cmd/eval/main.go's answerCompleterAdapter
// and judgeCompleterAdapter, scoped per KB so model selection follows KB config)
// ---------------------------------------------------------------------------

// inprocAnswerCompleterForKB generates answers using the KB's default model.
type inprocAnswerCompleterForKB struct {
	resolver *ai.ConfigResolver
	kbID     string
}

func (a *inprocAnswerCompleterForKB) Complete(ctx context.Context, prompt, systemPrompt string) (string, error) {
	res, err := ai.GenerateCompletion(ctx, a.resolver, prompt, systemPrompt, a.kbID, false)
	if err != nil {
		return "", err
	}
	return res.Content, nil
}

// inprocJudgeCompleterForKB uses deterministic generation scoped to a KB.
type inprocJudgeCompleterForKB struct {
	resolver *ai.ConfigResolver
	kbID     string
}

func (a *inprocJudgeCompleterForKB) Complete(ctx context.Context, prompt, systemPrompt string) (string, error) {
	res, err := ai.GenerateCompletionWithModelDeterministic(ctx, a.resolver, prompt, systemPrompt, a.kbID, false, "")
	if err != nil {
		return "", err
	}
	return res.Content, nil
}
