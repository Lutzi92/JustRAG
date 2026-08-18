// Command eval runs retrieval-only evaluation of the JustRAG vector pipeline
// against a JSONL golden set and emits a JSON + human-readable report.
//
// Usage:
//
//	eval --golden ../eval/golden/example.jsonl [--top-k 10] [--output eval-report.json] [--concurrency 1] [--question-id <id>]
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/justrag/go-backend/internal/agentteams"
	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/chat"
	"github.com/justrag/go-backend/internal/config"
	"github.com/justrag/go-backend/internal/database"
	"github.com/justrag/go-backend/internal/eval"
	"github.com/justrag/go-backend/internal/vector"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	goldenPath := flag.String("golden", "", "Path to the JSONL golden set")
	topK := flag.Int("top-k", 10, "Top-k cutoff for retrieval metrics")
	outputPath := flag.String("output", "eval-report.json", "Output JSON report path")
	outputFormat := flag.String("output-format", "json", `Output format: "json" (default, the existing rich Report shape) or "ragas" (RAGAS dataset shape: [{question, answer, contexts, ground_truth}, ...] for external benchmarking).`)
	concurrency := flag.Int("concurrency", 1, "Concurrent questions (currently ignored; reserved)")
	singleID := flag.String("question-id", "", "If set, run only the question with this id")
	judge := flag.Bool("judge", false, "Run LLM-as-judge evaluation (answer generation + faithfulness + answer relevance + context precision)")
	judgeModel := flag.String("judge-model", "", "Model override for judge prompts; empty = use the KB's default chat model")
	productionContext := flag.Bool("production-context", false, "When set, retrieval and answer generation run through chat.PrepareChatContext (production parity: CRAG, enumeration pre-pass, contextual prefix, sandwich order, abstain/low-confidence notices).")
	enhance := flag.String("enhance", "", `Query-enhancement mode: "rewrite", "expand", "spell", or "" (none).`)
	hyde := flag.Bool("hyde", false, "Enable HyDE query expansion (production-context mode only for CRAG/enumeration interplay).")
	multiQuery := flag.Bool("multi-query", false, "Enable multi-query retrieval.")
	stepBack := flag.Bool("step-back", false, "Enable step-back retrieval (one broader query, gated to complex_reasoning). Forces step-back on for this run regardless of the step_back_enabled site_config; lets grid search A/B the feature without DB mutation.")
	cragOverride := flag.String("crag", "", `CRAG override: "on" forces grading+rewrite regardless of KB config; "off" disables it; empty = KB config. Only effective in --production-context mode.`)
	enumerationOverride := flag.String("enumeration", "", `Enumeration pre-pass override: "on" forces run; "off" forces skip; empty = IsEnumerationQuery classifier. Only effective in --production-context mode.`)
	rerankAlphaOverride := flag.Float64("rerank-blend-alpha", -1, `Per-run override for rerank_blend_alpha (the GLOBAL alpha). <0 = use the value from site_configs. Use this for grid search alongside --rrf-weight-vector / --rrf-weight-bm25. Per-query-type alpha overrides (rerank_blend_alpha_lookup etc.) are NOT overridden by this flag — set them via site_configs if you want to test them.`)
	rrfWeightVectorOverride := flag.Float64("rrf-weight-vector", -1, `Per-run override for rrf_weight_vector. <0 = use the value from site_configs.`)
	rrfWeightBM25Override := flag.Float64("rrf-weight-bm25", -1, `Per-run override for rrf_weight_bm25. <0 = use the value from site_configs.`)
	trajectoryMode := flag.Bool("trajectory", false, `Phase 1 §1.2 trajectory eval: run each question through the selected orchestrator(s), capture the per-decision trajectory events, and write a JSONL alongside the standard report. Implies --production-context.`)
	orchestrator := flag.String("orchestrator", "all", `Orchestrator(s) to exercise under --trajectory: "off" | "agentic" | "plan_execute" | "plan_execute_dag" | "supervisor" | "all". Default "all" runs every mode in turn so a single command produces a comparison set.`)
	trajectoryOutputPath := flag.String("trajectory-output", "eval-trajectory.jsonl", `Path for the trajectory JSONL output. The aggregate report is written next to it with a ".aggregate.json" suffix.`)
	depthBuckets := flag.Bool("depth-buckets", false, `P9: enable position-aware retrieval analysis. When set, the report includes a "depth_buckets" section with per-quartile counts of relevant vs non-relevant chunks retrieved (chunkIndex/totalChunks bucketing). Useful for spotting position bias in the retrieval pipeline (e.g. "all our hits come from the first 25% of every file"). Off by default to keep the on-disk report shape byte-stable for diff tools.`)
	nodeKindFilter := flag.String("node-kind", "", `Phase F (RAPTOR) ablation: restrict retrieval to a single node_kind. "leaf" reproduces pre-RAPTOR behaviour; "summary" runs against summaries only; "" (default) lets both compete in one pool — production parity. Eval-only — the chat handler never sets this.`)
	depthBucketsMinChunks := flag.Int("depth-buckets-min-chunks", 4, `Min totalChunks required for a chunk to count toward the depth-bucket aggregate. Suppresses noise from short files where bucketing has no useful signal. Pass 1 to disable filtering. Only effective with --depth-buckets.`)
	orchestratorDispatch := flag.Bool("orchestrator-dispatch", true, `With --production-context: route each question through the same orchestrator predicate production uses (Supervisor / Plan-Execute / Plan-Execute-DAG / Agentic / standard fallback) and record per-question 'agent' + per-orchestrator aggregates in the report. Default true. Set to false to reproduce pre-2026-05 retrieval-only behaviour for byte-stable diffs against historical eval runs. Ignored when --production-context is unset.`)
	teamID := flag.String("team-id", "", "Dispatch every question through this user-created agent team (requires --production-context; team must be attached + enabled on the golden set's KB)")
	flag.Parse()

	if *teamID != "" && !*productionContext {
		slog.Error("--team-id requires --production-context")
		os.Exit(2)
	}
	if *teamID != "" && *trajectoryMode {
		slog.Error("team-id is not supported with --trajectory")
		os.Exit(2)
	}

	var forceEnumeration *bool
	switch *enumerationOverride {
	case "on":
		b := true
		forceEnumeration = &b
	case "off":
		b := false
		forceEnumeration = &b
	case "":
		// default classifier
	default:
		slog.Error("invalid --enumeration value", "value", *enumerationOverride)
		os.Exit(2)
	}

	if *cragOverride != "" && *cragOverride != "on" && *cragOverride != "off" {
		slog.Error("invalid --crag value", "value", *cragOverride)
		os.Exit(2)
	}

	if *goldenPath == "" {
		slog.Error("--golden is required")
		os.Exit(2)
	}
	if _, err := os.Stat(*goldenPath); err != nil {
		slog.Error("--golden path is not readable", "path", *goldenPath, "error", err)
		os.Exit(2)
	}

	questions, err := eval.LoadGoldenSet(*goldenPath)
	if err != nil {
		slog.Error("failed to load golden set", "error", err)
		os.Exit(1)
	}
	if *singleID != "" {
		filtered := questions[:0]
		for _, q := range questions {
			if q.ID == *singleID {
				filtered = append(filtered, q)
			}
		}
		questions = filtered
		if len(questions) == 0 {
			slog.Error("no question with that id", "id", *singleID)
			os.Exit(1)
		}
	}

	cfg, err := config.Load()
	if err != nil {
		slog.Error("config load", "error", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	db, err := database.Connect(ctx, cfg.DB, cfg.VectorDB)
	if err != nil {
		slog.Error("database connect", "error", err)
		os.Exit(1)
	}
	defer db.Main.Close()
	if db.Vector != db.Main {
		defer db.Vector.Close()
	}

	aiResolver := ai.NewConfigResolver(ai.NewStore(db.Main))
	chatStore := chat.NewStore(db.Main)

	// Build the per-run overlay map from CLI flags. <0 means "leave the
	// site_configs value alone". A non-empty overlay map causes the
	// search pipeline to use these values instead of whatever the DB has,
	// without mutating production state.
	overlays := map[string]string{}
	if *rerankAlphaOverride >= 0 {
		overlays["rerank_blend_alpha"] = strconv.FormatFloat(*rerankAlphaOverride, 'f', -1, 64)
	}
	if *rrfWeightVectorOverride >= 0 {
		overlays["rrf_weight_vector"] = strconv.FormatFloat(*rrfWeightVectorOverride, 'f', -1, 64)
	}
	if *rrfWeightBM25Override >= 0 {
		overlays["rrf_weight_bm25"] = strconv.FormatFloat(*rrfWeightBM25Override, 'f', -1, 64)
	}

	var searchReader vector.SiteConfigReader = chatStore
	if len(overlays) > 0 {
		searchReader = &overlaySiteConfig{inner: chatStore, overlays: overlays}
		slog.Info("eval: applying site_config overlays for this run", "overlays", overlays)
	}

	searchService := vector.NewSearchService(db.Vector, db.Main, aiResolver,
		vector.WithSiteConfigReader(searchReader),
	)

	var siteReader chat.SiteConfigReader = chatStore
	if *cragOverride != "" {
		siteReader = &cragOverrideReader{inner: chatStore, override: *cragOverride}
	}

	type evalAdapter interface {
		Search(ctx context.Context, q eval.Question, k int) ([]eval.RetrievedChunk, error)
		ContentsForQuestion(questionID string, k int) (contents []string, fileNames []string, ok bool)
	}

	var adapter evalAdapter
	if *productionContext {
		flags := eval.EvalFlags{
			Enhance:                 *enhance,
			HyDE:                    *hyde,
			MultiQuery:              *multiQuery,
			StepBack:                *stepBack,
			ForceEnumerationPrepass: forceEnumeration,
		}
		// KB-system-prompt accessor - same closure shape as the trajectory
		// runner uses below - so the dispatched orchestrators/team see the
		// prompt production wires in via tryDeepChat.
		getKbSystemPrompt := func(ctx context.Context, kbID string) string {
			if sp, err := chatStore.GetKBSystemPrompt(ctx, kbID); err == nil && sp != nil {
				return *sp
			}
			return ""
		}
		if *teamID != "" {
			teamStore := agentteams.NewStore(db.Main)
			adapter = eval.NewTeamDispatchAdapter(
				aiResolver,
				searchService,
				siteReader,
				teamStore,
				*teamID,
				getKbSystemPrompt,
			)
			slog.Info("eval: team-dispatch mode on", "team_id", *teamID)
		} else if *orchestratorDispatch {
			adapter = eval.NewOrchestratorDispatchAdapter(
				aiResolver,
				searchService,
				siteReader,
				getKbSystemPrompt,
				flags,
			)
			slog.Info("eval: orchestrator-dispatch mode on (production-parity routing)")
		} else {
			adapter = eval.NewProductionContextAdapter(
				aiResolver,
				searchService,
				siteReader,
				flags,
			)
		}
	} else {
		if *cragOverride != "" || *enumerationOverride != "" {
			slog.Warn("--crag / --enumeration require --production-context; ignored in legacy mode")
		}
		if *productionContext && *nodeKindFilter != "" {
			slog.Warn("--node-kind currently effective in legacy mode only; ignored under --production-context")
		}
		adapter = &legacySearchAdapter{
			svc: searchService,
			opts: vector.SearchOptions{
				Enhance:        *enhance,
				HyDE:           *hyde,
				MultiQuery:     *multiQuery,
				StepBack:       *stepBack,
				NodeKindFilter: *nodeKindFilter,
			},
		}
	}

	// ------------------------------------------------------------------
	// Trajectory mode (Phase 1 §1.2): run each question through the
	// requested orchestrator(s), collect per-decision events, write a
	// JSONL + aggregate alongside the standard report. The standard
	// retrieval eval continues in the same run so latency / recall
	// numbers stay comparable; trajectory output is purely additive.
	// ------------------------------------------------------------------
	if *trajectoryMode {
		modes := selectTrajectoryModes(*orchestrator)
		if len(modes) == 0 {
			slog.Error("invalid --orchestrator value", "value", *orchestrator, "supported", []string{"off", "agentic", "plan_execute", "all"})
			os.Exit(2)
		}
		traj := runTrajectoryEval(ctx, eval.TrajectoryRunDeps{
			AIResolver:    aiResolver,
			SearchService: searchService,
			SiteReader:    siteReader,
			KbSystemPrompt: func(ctx context.Context, kbID string) string {
				if sp, err := chatStore.GetKBSystemPrompt(ctx, kbID); err == nil && sp != nil {
					return *sp
				}
				return ""
			},
		}, questions, modes)
		// If --judge is on, run the trajectory judges (decomposition,
		// decision, rewrite) per record. The eval CLI's existing
		// judgeCompleterAdapter is used for the LLM call (deterministic,
		// optional model override). Without --judge we leave Score nil and
		// the aggregator reports counts only.
		if *judge {
			qByID := make(map[string]eval.Question, len(questions))
			for _, q := range questions {
				qByID[q.ID] = q
			}
			for i := range traj {
				rec := &traj[i]
				q, ok := qByID[rec.QuestionID]
				if !ok {
					continue
				}
				ja := judgeCompleterAdapter{resolver: aiResolver, kbID: q.KbID, modelOverride: *judgeModel}
				eval.JudgeTrajectory(ctx, ja, q.Language, q.Question, "", q.ExpectedTools, rec)
			}
		}
		aggPath := *trajectoryOutputPath + ".aggregate.json"
		if werr := eval.WriteTrajectoryReport(*trajectoryOutputPath, aggPath, traj); werr != nil {
			slog.Error("write trajectory report", "error", werr)
			os.Exit(1)
		}
		slog.Info("trajectory report written",
			"records_path", *trajectoryOutputPath,
			"aggregate_path", aggPath,
			"records", len(traj),
			"modes", modes,
		)
	}

	started := time.Now()
	rep, err := eval.RunEval(ctx, adapter, questions, *topK, *concurrency)
	if err != nil {
		slog.Error("run eval", "error", err)
		os.Exit(1)
	}
	rep.GoldenPath = *goldenPath

	if *judge {
		for i := range rep.Questions {
			qr := &rep.Questions[i]
			if qr.Error != "" {
				continue
			}
			contents, fileNames, ok := adapter.ContentsForQuestion(qr.Question.ID, *topK)
			if !ok || len(contents) == 0 {
				qr.Judge = &eval.JudgeMetrics{JudgeErrors: []string{"content_loader: no cached chunks for question"}}
				continue
			}
			qr.Contents = contents
			answerAdapter := answerCompleterAdapter{resolver: aiResolver, kbID: qr.Question.KbID}
			judgeAdapter := judgeCompleterAdapter{resolver: aiResolver, kbID: qr.Question.KbID, modelOverride: *judgeModel}

			var answer string
			var answerErr error
			// Both ProductionContextAdapter and OrchestratorDispatchAdapter
			// expose ChatContextForQuestion (the latter delegates to its
			// embedded prod adapter). When a full ChatContext is cached we
			// use it for production parity; otherwise fall through to the
			// generic content-based answer path (e.g. orchestrator-branch
			// questions or legacy adapters).
			type chatCtxProvider interface {
				ChatContextForQuestion(questionID string) (*chat.ChatContext, bool)
			}
			if ccp, ok := adapter.(chatCtxProvider); ok {
				if chatCtx, hit := ccp.ChatContextForQuestion(qr.Question.ID); hit {
					userPrompt := fmt.Sprintf("%s\n\nQUESTION:\n%s", chatCtx.Context, qr.Question.Question)
					answer, answerErr = answerAdapter.Complete(ctx, userPrompt, chatCtx.SystemPrompt)
				} else {
					answer, answerErr = eval.GenerateAnswer(ctx, answerAdapter, qr.Question.Question, qr.Retrieved, contents, fileNames, qr.Question.Language)
				}
			} else {
				answer, answerErr = eval.GenerateAnswer(ctx, answerAdapter, qr.Question.Question, qr.Retrieved, contents, fileNames, qr.Question.Language)
			}
			if answerErr != nil {
				qr.Judge = &eval.JudgeMetrics{JudgeErrors: []string{"answer: " + answerErr.Error()}}
				continue
			}
			jm := eval.NewJudge(judgeAdapter).Evaluate(ctx, qr.Question, answer, qr.Retrieved, contents)
			qr.Judge = &jm
		}
		// Re-aggregate so judge means populate.
		rep.Aggregate = eval.Aggregate(rep.Questions, *topK)
		rep.RouteAggregates = eval.AggregateByRoute(rep.Questions, *topK)
		rep.OrchestratorAggregates = eval.AggregateByOrchestrator(rep.Questions, *topK)
	}

	// P9: position-aware retrieval analysis. Off by default — when
	// requested, compute the per-quartile bucket aggregate over the
	// already-collected per-question reports.
	if *depthBuckets {
		dbr := eval.AggregateDepthBuckets(rep.Questions, *topK, *depthBucketsMinChunks)
		rep.DepthBuckets = &dbr
		slog.Info("depth buckets computed",
			"eligible_questions", dbr.EligibleQuestions,
			"min_total_chunks", dbr.MinTotalChunks,
		)
	}

	// Write report to file. --output-format selects the shape.
	f, err := os.Create(*outputPath)
	if err != nil {
		slog.Error("create output", "error", err, "path", *outputPath)
		os.Exit(1)
	}
	switch *outputFormat {
	case "ragas":
		// RAGAS dataset shape: [{question, answer, contexts, ground_truth}, ...]
		// for external benchmarking. The rich Report fields (retrieval
		// metrics, judge errors, latencies) are NOT in this format —
		// operators who want both shapes run two passes.
		data, jerr := json.MarshalIndent(eval.ExportRAGAS(rep), "", "  ")
		if jerr != nil {
			_ = f.Close()
			slog.Error("marshal ragas", "error", jerr)
			os.Exit(1)
		}
		if _, werr := f.Write(data); werr != nil {
			_ = f.Close()
			slog.Error("write ragas", "error", werr)
			os.Exit(1)
		}
	case "json", "":
		if err := eval.WriteJSONReport(f, rep); err != nil {
			_ = f.Close()
			slog.Error("write json report", "error", err)
			os.Exit(1)
		}
	default:
		_ = f.Close()
		slog.Error("unknown --output-format", "value", *outputFormat, "supported", []string{"json", "ragas"})
		os.Exit(1)
	}
	if closeErr := f.Close(); closeErr != nil {
		slog.Error("close output", "error", closeErr, "path", *outputPath)
		os.Exit(1)
	}

	// Human summary to stdout.
	_ = eval.WriteHumanSummary(os.Stdout, rep)
	fmt.Fprintf(os.Stdout, "\nTotal wall time: %s\nJSON report: %s\n", time.Since(started), *outputPath)

	if rep.Errors > 0 {
		os.Exit(1)
	}
}

// legacySearchAdapter adapts vector.SearchService to eval.Searcher.
// It calls the baseline retrieval path with caller-supplied SearchOptions
// (honouring --enhance / --hyde / --multi-query / --step-back) so metrics
// reflect "minimal retrieval" quality without the chat-layer extras (CRAG,
// enumeration pre-pass, contextual prefix, sandwich order).
// The cache field stores the full SearchChunk results per question ID so the
// judge layer can look up content and file names without extra DB calls.
type legacySearchAdapter struct {
	svc   vector.Searcher
	opts  vector.SearchOptions
	cache map[string][]vector.SearchChunk
}

func (a *legacySearchAdapter) Search(ctx context.Context, q eval.Question, k int) ([]eval.RetrievedChunk, error) {
	result, err := a.svc.Search(ctx, q.KbID, q.Question, k, a.opts)
	if err != nil {
		return nil, err
	}
	if a.cache == nil {
		a.cache = make(map[string][]vector.SearchChunk)
	}
	a.cache[q.ID] = result.Chunks
	out := make([]eval.RetrievedChunk, 0, len(result.Chunks))
	for _, c := range result.Chunks {
		idx, total := eval.ChunkPositionFromMetadata(c.Metadata)
		out = append(out, eval.RetrievedChunk{
			FileID:      c.FileID,
			FileName:    c.FileName,
			Score:       c.Score,
			ChunkIndex:  idx,
			TotalChunks: total,
		})
	}
	return out, nil
}

// ContentsForQuestion returns ordered content + filename slices matching
// the cache entry for questionID, trimmed to limit k.
func (a *legacySearchAdapter) ContentsForQuestion(questionID string, k int) (contents []string, fileNames []string, ok bool) {
	chunks, found := a.cache[questionID]
	if !found {
		return nil, nil, false
	}
	if k > 0 && k < len(chunks) {
		chunks = chunks[:k]
	}
	contents = make([]string, len(chunks))
	fileNames = make([]string, len(chunks))
	for i, c := range chunks {
		contents[i] = c.Content
		fileNames[i] = c.FileName
	}
	return contents, fileNames, true
}

// cragOverrideReader wraps a chat.SiteConfigReader to force CRAG on or
// off without modifying site_configs. When override is "on"/"off",
// GetSiteConfigValue intercepts the "crag_enabled" key and returns
// "true"/"false" respectively. All other keys delegate to the inner
// reader.
type cragOverrideReader struct {
	inner    chat.SiteConfigReader
	override string // "on" | "off"
}

func (w *cragOverrideReader) GetSiteConfigValue(ctx context.Context, key string) (*string, error) {
	if key == "crag_enabled" {
		var v string
		switch w.override {
		case "on":
			v = "true"
		case "off":
			v = "false"
		default:
			return w.inner.GetSiteConfigValue(ctx, key)
		}
		return &v, nil
	}
	return w.inner.GetSiteConfigValue(ctx, key)
}

// overlaySiteConfig wraps a SiteConfigReader so that a fixed set of keys
// returns CLI-flag-supplied values instead of whatever is in the DB.
// Used by the eval CLI for grid-search over fusion / blend knobs without
// mutating production site_configs. Satisfies both vector.SiteConfigReader
// and chat.SiteConfigReader since they share GetSiteConfigValue.
type overlaySiteConfig struct {
	inner    vector.SiteConfigReader
	overlays map[string]string
}

func (o *overlaySiteConfig) GetSiteConfigValue(ctx context.Context, key string) (*string, error) {
	if v, ok := o.overlays[key]; ok {
		return &v, nil
	}
	return o.inner.GetSiteConfigValue(ctx, key)
}

// answerCompleterAdapter runs ai.GenerateCompletion (default temp 0.2) for a
// specific KB.
type answerCompleterAdapter struct {
	resolver *ai.ConfigResolver
	kbID     string
}

func (a answerCompleterAdapter) Complete(ctx context.Context, prompt, systemPrompt string) (string, error) {
	res, err := ai.GenerateCompletion(ctx, a.resolver, prompt, systemPrompt, a.kbID, false)
	if err != nil {
		return "", err
	}
	return res.Content, nil
}

// judgeCompleterAdapter runs ai.GenerateCompletionWithModelDeterministic
// (temperature=0) with an optional judge-model override.
type judgeCompleterAdapter struct {
	resolver      *ai.ConfigResolver
	kbID          string
	modelOverride string
}

func (a judgeCompleterAdapter) Complete(ctx context.Context, prompt, systemPrompt string) (string, error) {
	res, err := ai.GenerateCompletionWithModelDeterministic(ctx, a.resolver, prompt, systemPrompt, a.kbID, false, a.modelOverride)
	if err != nil {
		return "", err
	}
	return res.Content, nil
}

// selectTrajectoryModes maps the --orchestrator flag to the slice of
// modes the runner iterates over. Returns nil for an unrecognized value
// so the caller can surface a structured error.
func selectTrajectoryModes(value string) []eval.TrajectoryMode {
	switch value {
	case "off":
		return []eval.TrajectoryMode{eval.TrajectoryModeOff}
	case "agentic":
		return []eval.TrajectoryMode{eval.TrajectoryModeAgentic}
	case "plan_execute":
		return []eval.TrajectoryMode{eval.TrajectoryModePlanExecute}
	case "plan_execute_dag":
		return []eval.TrajectoryMode{eval.TrajectoryModePlanExecuteDAG}
	case "supervisor":
		return []eval.TrajectoryMode{eval.TrajectoryModeSupervisor}
	case "all", "":
		return []eval.TrajectoryMode{
			eval.TrajectoryModeOff,
			eval.TrajectoryModeAgentic,
			eval.TrajectoryModePlanExecute,
			eval.TrajectoryModePlanExecuteDAG,
			eval.TrajectoryModeSupervisor,
		}
	default:
		return nil
	}
}

// runTrajectoryEval iterates the question set across every requested
// orchestrator mode and returns one TrajectoryRecord per (question,
// mode) tuple. The runner is sequential — orchestrators each issue
// 2–5 LLM calls + N searches, so concurrency would just turn the
// CLI into an unbounded LLM-traffic generator.
func runTrajectoryEval(ctx context.Context, deps eval.TrajectoryRunDeps, questions []eval.Question, modes []eval.TrajectoryMode) []eval.TrajectoryRecord {
	out := make([]eval.TrajectoryRecord, 0, len(questions)*len(modes))
	for _, m := range modes {
		for _, q := range questions {
			rec := eval.RunTrajectory(ctx, deps, q, m)
			out = append(out, rec)
		}
	}
	return out
}
