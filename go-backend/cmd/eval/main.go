// Command eval runs retrieval-only evaluation of the JustRAG vector pipeline
// against a JSONL golden set and emits a JSON + human-readable report.
//
// Usage:
//
//	eval --golden ../eval/golden/example.jsonl [--top-k 10] [--output eval-report.json] [--concurrency 1] [--question-id <id>] [--baseline prev.json]
//	eval --pairwise-a A.json --pairwise-b B.json [--judge-model <m>] [--pairwise-out pairwise.json]
//	eval [--pairwise-out pooled.json] --pairwise-pool pw1.json pw2.json
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
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/agentteams"
	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/chat"
	"github.com/justrag/go-backend/internal/chatpolicy"
	"github.com/justrag/go-backend/internal/config"
	"github.com/justrag/go-backend/internal/database"
	"github.com/justrag/go-backend/internal/eval"
	"github.com/justrag/go-backend/internal/files"
	"github.com/justrag/go-backend/internal/recencylister"
	"github.com/justrag/go-backend/internal/tabular"
	"github.com/justrag/go-backend/internal/tabular/sqlexec"
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
	orchestratorDispatch := flag.Bool("orchestrator-dispatch", true, `With --production-context: route each question through the same orchestrator predicate production uses (Supervisor / Plan-Execute / Plan-Execute-DAG / Agentic / standard fallback) and record per-question 'agent' + per-orchestrator aggregates in the report. Default true. Set to false to reproduce pre-2026-05 retrieval-only behaviour for byte-stable diffs against historical eval runs, except that recency-listing-shaped questions now exercise the recency lister and window scoping, which pre-2026-09 runs did not. Ignored when --production-context is unset.`)
	teamID := flag.String("team-id", "", "Dispatch every question through this user-created agent team (requires --production-context; team must be attached + enabled on the golden set's KB)")
	keepRawFlag := flag.String("keep-raw", "", `Multi-turn replay override for the rewrite⊕raw retrieval lane (ruling W2-R10): "on" forces chat_condense_keep_raw_enabled on for this run, "off" forces it off, "" (default) reads the live site_config — same three-way shape as --crag. Only effective on a golden set with turns (a multi-turn replay); ignored otherwise.`)
	baselinePath := flag.String("baseline", "", "Path to a previous eval-report.json. When set, prints a per-route delta table and exits 3 if recall or MRR dropped beyond --regress-recall-pp / --regress-mrr-pp (overall or on any route present in both reports). Runs with question errors exit 1 before the delta is computed.")
	regressRecallPP := flag.Float64("regress-recall-pp", eval.DefaultRegressionThresholds.RecallPP, "Max tolerated mean-recall drop vs --baseline, in percentage points.")
	regressMRRPP := flag.Float64("regress-mrr-pp", eval.DefaultRegressionThresholds.MRRPP, "Max tolerated MRR drop vs --baseline, in percentage points.")
	refreshBM25Stats := flag.Bool("refresh-bm25-stats", false, "Before running, recompute BM25 statistics (vector.BM25StatsRefresher.RefreshKB) for every KB referenced by the golden set, across every dim table that has rows for that KB, so an A/B never runs against missing/stale stats.")
	bm25ModeOverride := flag.String("bm25-mode", "", `Wave-2 Task 6 / ruling W2-R10: per-run override for bm25_scoring_mode ("ts_rank" | "bm25"). Empty = read the live site_config. Applied the same way as --rerank-blend-alpha (wraps the vector-layer site-config reader; no site_configs mutation) — combine with --refresh-bm25-stats when testing "bm25" against a golden set whose KBs haven't had a stats refresh yet.`)
	bm25TieredBoostOverride := flag.String("bm25-tiered-boost", "", `Per-run override for bm25_tiered_boost_enabled ("on" | "off"). Empty = read the live site_config. Same overlay mechanism as --bm25-mode.`)
	longContextModeOverride := flag.String("longcontext-mode", "", `Wave-3 ruling W3-R6: per-run override for chat_longcontext_mode ("flat" | "map_reduce") — which consumer the OrchLongContext orchestrator uses. Empty = read the live site_config (whose default is "map_reduce" since Wave 5 / W5-R1; an unrecognised stored value normalises to "flat"). This is a CHAT-layer key, so it wraps siteReader like --crag (not the vector-layer overlay). Only has an effect when chat_longcontext_enabled is on and the question trips the global-synthesis classifier.`)
	longContextEnabled := flag.String("longcontext", "", `Wave-3 ruling W3-R5: per-run override for chat_longcontext_enabled ("on" | "off"). Empty = read the live site_config. Chat-layer key, applied through the same overlay as --longcontext-mode. "on" puts OrchLongContext at the top of the eval orchestrator ladder for questions the global-synthesis classifier accepts, so a global-synthesis golden set can be measured without mutating site_configs.`)
	conflictSurfacing := flag.String("conflict-surfacing", "", `Wave-5 ruling W5-R7: per-run override for chat_conflict_surfacing_enabled ("on" | "off"). Empty = read the live site_config. Chat-layer key, applied through the same overlay as --longcontext. "on" makes every turn whose assembled set spans >= 2 distinct files run the fast-tier conflict / supersession pass, and records the resulting report per question as "conflicts" in the JSON report (the same bare array a chat turn persists and streams). Effective on the standard PrepareChatContext path and, under --orchestrator-dispatch, on the Supervisor path.`)
	policyJSON := flag.String("policy", "", `Wave-6 W6-R6/W6-R7: per-run overlay for chat_orchestrator_policy (a JSON array of routing rules, e.g. [{"when":{"query_type":["complex_reasoning"]},"orchestrator":"supervisor","mode":"force"}]). Validated with chatpolicy.ValidateOrchestratorPolicyJSON BEFORE the run — an invalid document is a usage error (exit 2), never a silently ignored flag. Empty (default) = read the live site_config, i.e. the ladder is unchanged. Applied through the same chat-layer overlay as --conflict-surfacing, so it mutates no site_configs row; passing --chat-overlay chat_orchestrator_policy=... as well is an error (use --policy, which validates). REQUIRES --production-context --orchestrator-dispatch=true (the default) to actually be applied — a run in any other shape never reads chat_orchestrator_policy and is rejected as a usage error, EXCEPT --trajectory, where the document is accepted but is informational only (it reaches the report's policy_rule join key, not SelectOrchestratorWithPolicy's routing decision).`)
	var chatOverlayFlags chatOverlayFlag
	flag.Var(&chatOverlayFlags, "chat-overlay", `Wave-6 W6-R18: per-run override of ONE chat-layer site_config key (the reader PrepareChatContext and the orchestrators receive), applied through the same overlay as --conflict-surfacing; repeatable; key must be non-empty and contain no '='; only keys read through the chat-layer reader are affected — vector-layer keys keep their own flags.`)
	goldenQueryType := flag.Bool("golden-query-type", false, `Forward each golden row's curated "query_type" label into the retrieval pipeline (chat.ChatContextParams.QueryType) instead of letting the pipeline classify the question. Default false so existing --production-context reports keep their historical shape. Does NOT affect orchestrator dispatch, which classifies independently — if a question does not reach the intended orchestrator, rewrite the question, not the label.`)
	recencyBoostOverride := flag.String("recency-boost", "", `Wave 2 Task 8: per-run override for recency_boost_enabled ("on" | "off"). Empty = read the live site_config. Same overlay mechanism as --bm25-tiered-boost (a vector-layer key, applied via the searchReader overlay, not the chat-level siteReader). Lets the CERT recency fixture A/B the recency prior without a site_configs mutation.`)
	printKeywordSQL := flag.String("print-keyword-sql", "", `Diagnostic mode (Wave-3 Task 7): print the keyword arm's SQL for this query — for BOTH scoring modes (ts_rank and bm25), with the KB's real resolved settings (chunk table, text-search config, simple arm, tiered boost, k1/b, dim-keyed stats tables) — as one JSON document on stdout, then exit 0. Requires --kb-id. Runs no search, no LLM call, and needs no golden set; --top-k sets the statement's LIMIT (pass 50 to match the legacy pre-rerank candidate depth the keyword arm actually runs with at top-k 10 with a reranker; a non-positive value falls back to 50). Each mode carries both the parameterised SQL and an "executable_sql" with the placeholders inlined, so it can be handed straight to EXPLAIN (ANALYZE, BUFFERS).`)
	printKeywordSQLKBID := flag.String("kb-id", "", "KB id for --print-keyword-sql. Ignored in every other mode (the golden set carries its own kb_id per question).")
	pairwiseA := flag.String("pairwise-a", "", `Offline pairwise preference mode (ruling W4-R4), side A: path to a judged eval report (a run made with --judge, so every question carries judge.answer). Requires --pairwise-b. Compares the two reports' persisted answers question by question with an LLM preference judge — every pair judged TWICE with the positions swapped, counting a win only when both orders agree (position debias); pairs the judge flips on are ties. Prints win/tie/loss counts, the win rate with a 95% Wilson interval, a per-route breakdown and a per-question table. Runs no retrieval and generates no answers; short-circuits before --golden and always exits 0 on a completed comparison (measurement, not a gate). --judge-model selects the judge.`)
	pairwiseB := flag.String("pairwise-b", "", "Pairwise preference mode, side B: the report compared against --pairwise-a. The reported win rate is A's — a win rate below 0.5 means B produced the better answers.")
	pairwiseOut := flag.String("pairwise-out", "", "Optional path for the pairwise result as JSON (per-pair verdicts incl. both orders' reasoning, counts, Wilson interval, per-route breakdown). Empty = human-readable output only. Also the output path of --pairwise-pool (the pooled report).")
	pairwisePool := flag.String("pairwise-pool", "", `Wave-5 ruling W5-R1: pool two or more finished --pairwise-out JSONs into ONE win/tie/loss tally, with the win rate, the tie rate and the 95% Wilson interval RECOMPUTED on the pooled decisive pairs (never averaged across runs, which would weight a pair with 4 decisive verdicts like one with 16). Usage: --pairwise-out pooled.json --pairwise-pool a.json b.json — the first path is the flag value, the rest are positional, so EVERY other flag must come BEFORE them (Go's flag parsing stops at the first positional argument; a flag placed after the paths is rejected with an error rather than silently swallowed as a path). Ties stay out of every denominator, as in --pairwise-a/-b. Prints the pooled counts from BOTH sides' view (W5-R1 is stated from side B's) plus a per-input and a per-route pooled table. Reads only files: no retrieval, no judge, no database. Exits 0 on a completed pooling (measurement, not a gate), 2 on a usage error. Pooling assumes every input assigned the SAME configuration to side A — the pairwise JSON carries no report paths, so that cannot be verified here.`)
	flag.Parse()

	// The offline modes (--print-keyword-sql, --pairwise-pool,
	// --pairwise-a/-b) short-circuit before --golden is required and before
	// any config/DB setup. runOfflineMode keeps their precedence, their
	// usage-error/failure exit codes and their "a completed comparison exits
	// 0 whichever side won" contract; see cmd/eval/offline_modes.go.
	if handled, code := runOfflineMode(offlineFlags{
		printKeywordSQL: *printKeywordSQL,
		kbID:            *printKeywordSQLKBID,
		topK:            *topK,
		pairwisePool:    *pairwisePool,
		poolExtraPaths:  flag.Args(),
		pairwiseA:       *pairwiseA,
		pairwiseB:       *pairwiseB,
		pairwiseOut:     *pairwiseOut,
		judgeModel:      *judgeModel,
	}); handled {
		if code != 0 {
			os.Exit(code)
		}
		return
	}

	var baseline *eval.Report
	if *baselinePath != "" {
		bf, err := os.Open(*baselinePath)
		if err != nil {
			slog.Error("--baseline path is not readable", "path", *baselinePath, "error", err)
			os.Exit(2)
		}
		rep, err := eval.ReadJSONReport(bf)
		_ = bf.Close()
		if err != nil {
			slog.Error("--baseline is not a cmd/eval JSON report", "path", *baselinePath, "error", err)
			os.Exit(2)
		}
		baseline = &rep
	}

	if *teamID != "" && !*productionContext {
		slog.Error("--team-id requires --production-context")
		os.Exit(2)
	}
	if *teamID != "" && *trajectoryMode {
		slog.Error("team-id is not supported with --trajectory")
		os.Exit(2)
	}

	// nil = leave the decision to the classifier.
	forceEnumeration, ok := parseBoolChoice(*enumerationOverride)
	if !ok {
		slog.Error("invalid --enumeration value", "value", *enumerationOverride)
		os.Exit(2)
	}

	// One list, checked in the order these flags were checked individually,
	// so a command line with two bad values still reports the same one.
	if bad, found := firstInvalidChoice([]choiceFlag{
		{name: "--crag", value: *cragOverride, allowed: []string{"on", "off"}},
		{name: "--bm25-mode", value: *bm25ModeOverride, allowed: []string{"ts_rank", "bm25"}},
		{name: "--longcontext-mode", value: *longContextModeOverride, allowed: []string{"flat", "map_reduce"}},
		{name: "--longcontext", value: *longContextEnabled, allowed: []string{"on", "off"}},
		{name: "--bm25-tiered-boost", value: *bm25TieredBoostOverride, allowed: []string{"on", "off"}},
		{name: "--conflict-surfacing", value: *conflictSurfacing, allowed: []string{"on", "off"}},
		{name: "--recency-boost", value: *recencyBoostOverride, allowed: []string{"on", "off"}},
	}); found {
		slog.Error("invalid "+bad.name+" value", "value", bad.value)
		os.Exit(2)
	}

	// W6-R18: parse the repeated --chat-overlay key=value pairs once, here,
	// so a malformed pair exits 2 with the same usage-error shape as every
	// other CLI validation above, rather than surfacing later as a puzzling
	// site_config read.
	extraChatOverlays, err := parseChatOverlayFlags(chatOverlayFlags)
	if err != nil {
		slog.Error("invalid --chat-overlay value", "error", err)
		os.Exit(2)
	}

	// W6-R6: --policy is validated by the same parser the save path and the
	// reader use, so a run can never measure a document production would
	// reject. It reaches the pipeline as an ordinary chat overlay, which is
	// why the generic --chat-overlay escape hatch must not ALSO set the key:
	// the two would race on the same map entry and the unvalidated one would
	// win (buildChatOverlays merges extra last).
	if err := validatePolicyFlag(*policyJSON, extraChatOverlays); err != nil {
		slog.Error("invalid --policy value", "error", err)
		os.Exit(2)
	}

	// S12 (Wave-6 final review): --policy is silently a no-op unless the run
	// actually reaches SelectOrchestratorWithPolicy. Off the --trajectory
	// path (where it is informational only — it reaches the report's
	// policy_rule join key, but is not what "ran"), that means
	// --production-context --orchestrator-dispatch=true; nothing on the
	// retrieval-only or --orchestrator-dispatch=false path reads
	// chat_orchestrator_policy at all. Catch it here rather than letting an
	// operator run a long measurement that measures nothing.
	if err := checkPolicyFlagReachable(*policyJSON, *productionContext, *orchestratorDispatch, *trajectoryMode); err != nil {
		slog.Error(err.Error())
		os.Exit(2)
	}

	// nil = read chat_condense_keep_raw_enabled from site_configs.
	keepRaw, ok := parseBoolChoice(*keepRawFlag)
	if !ok {
		slog.Error("invalid --keep-raw value", "value", *keepRawFlag)
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
		// A per-turn id ("MT01#t2") names a conversation row's expanded
		// turn, which doesn't exist yet at this pre-expand stage — match
		// the conversation prefix (before "#") so the whole conversation
		// survives filtering and ExpandTurns can still build that turn's
		// History from its predecessors. A plain id ("MT01" or a
		// single-turn question's own id) matches exactly, since it has no
		// "#" to strip.
		conversationID := *singleID
		if i := strings.IndexByte(*singleID, '#'); i >= 0 {
			conversationID = (*singleID)[:i]
		}
		filtered := questions[:0]
		for _, q := range questions {
			if q.ID == conversationID {
				filtered = append(filtered, q)
			}
		}
		questions = filtered
		if len(questions) == 0 {
			slog.Error("no question with that id", "id", *singleID)
			os.Exit(1)
		}
	}

	loaded := questions
	questions = eval.ExpandTurns(questions)
	hasTurns := len(questions) != len(loaded)

	if *singleID != "" && strings.Contains(*singleID, "#") {
		// Narrow down from "the whole conversation" (kept above so
		// ExpandTurns had the full turn sequence) to just the requested
		// turn.
		filtered := questions[:0]
		for _, q := range questions {
			if q.ID == *singleID {
				filtered = append(filtered, q)
			}
		}
		questions = filtered
		if len(questions) == 0 {
			slog.Error("no turn with that id", "id", *singleID)
			os.Exit(1)
		}
	}

	if hasTurns && !*productionContext {
		slog.Error("golden set has turns; --multi-turn replay requires --production-context")
		os.Exit(2)
	}
	if hasTurns && *teamID != "" {
		slog.Error("--team-id is not supported with a golden set that has turns (multi-turn replay always runs the standard PrepareChatContext path)")
		os.Exit(2)
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

	if *refreshBM25Stats {
		refreshBM25StatsForGoldenSet(ctx, db.Vector, db.Main, questions)
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
	// bm25_scoring_mode / bm25_tiered_boost_enabled are internal/vector
	// site_config keys (read via KBVectorConfig, not chat.SiteConfigReader),
	// so they go through the same searchReader overlay as the rerank/RRF
	// knobs above rather than the chat-level cragOverrideReader pattern —
	// wrapping siteReader would have no effect on vector.SearchService's
	// mode resolution.
	if *bm25ModeOverride != "" {
		overlays["bm25_scoring_mode"] = *bm25ModeOverride
	}
	if *bm25TieredBoostOverride != "" {
		overlays["bm25_tiered_boost_enabled"] = strconv.FormatBool(*bm25TieredBoostOverride == "on")
	}
	if *recencyBoostOverride != "" {
		overlays["recency_boost_enabled"] = strconv.FormatBool(*recencyBoostOverride == "on")
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
	// chat_longcontext_enabled / chat_longcontext_mode are CHAT-layer keys
	// (chat.ChatLongContextEnabled / ChatLongContextMode), so they wrap
	// siteReader rather than the vector-layer overlay above. Chained after
	// the CRAG wrapper so both overrides compose. One wrapper carries both
	// keys — a second wrapper would be indistinguishable but harder to read.
	if chatOverlays := buildChatOverlays(*longContextEnabled, *longContextModeOverride, *conflictSurfacing, *policyJSON, extraChatOverlays); len(chatOverlays) > 0 {
		siteReader = &chatOverlayReader{inner: siteReader, overlays: chatOverlays}
		slog.Info("eval: applying chat site_config overlays for this run", "overlays", chatOverlays)
	}

	type evalAdapter interface {
		Search(ctx context.Context, q eval.Question, k int) ([]eval.RetrievedChunk, error)
		ContentsForQuestion(questionID string, k int) (contents []string, fileNames []string, ok bool)
	}

	// Carry 3 (Phase 4 Task 7): nil unless --production-context builds the
	// router below — trajectory mode stays router-free otherwise, matching the
	// adapters' "no --production-context ⇒ no router" convention.
	var trajTabularRouter *chat.TabularRouter
	var adapter evalAdapter
	if *productionContext {
		flags := eval.EvalFlags{
			Enhance:                 *enhance,
			HyDE:                    *hyde,
			MultiQuery:              *multiQuery,
			StepBack:                *stepBack,
			ForceEnumerationPrepass: forceEnumeration,
			GoldenQueryType:         *goldenQueryType,
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
		// R30/R51: build the deterministic tabular router ONCE, for the
		// default dispatch path. The eval binary has only the main pool,
		// so the read-only executor wraps it in a READ ONLY transaction —
		// and every statement is validated before it is ever executed.
		// Deliberately NOT wired into the --orchestrator-dispatch=false
		// branch: that branch exists to produce byte-stable diffs against
		// pre-2026-05 retrieval-only runs and must stay router-free.
		tabularRouter := chat.NewTabularRouter(
			tabular.NewCatalog(db.Main),
			sqlexec.NewReadOnly(db.Main),
			func(ctx context.Context, req ai.TabularSQLRequest, kbID, model string) (ai.TabularSQLProposal, error) {
				return ai.GenerateTabularSQL(ctx, aiResolver, req, kbID, model)
			},
			func(ctx context.Context) chat.TabularRouterConfig {
				return chat.ResolveTabularRouterConfig(ctx, chatStore)
			},
		)
		trajTabularRouter = tabularRouter
		// Wave 2 Task 8 (W2-R9): the deterministic recency-listing path
		// ("Welche neuen Meldungen gibt es?") needs a RecencyLister to
		// fire under cmd/eval at all — without it, PrepareChatContext
		// silently skips window scoping and the listing addendum
		// (applyRecencyListing no-ops on a nil lister), and the CERT
		// fixture's headline mechanism never exercises. Built once, like
		// tabularRouter above, and wired into every --production-context
		// branch that runs the standard PrepareChatContext path.
		recencyLister := recencylister.New(db.Main)
		// Wave 5 Task 5 (W5-R7): the conflict / supersession pass decides
		// DIRECTION from each source's date line, and those dates come from
		// a chat.FileDateLookup. internal/app wires exactly this adapter for
		// production; without it every date renders "unknown" and no
		// supersession can ever be reported with `newer` set, which would
		// make the CERT NEU/UPDATE measurement vacuous. Inert when
		// chat_conflict_surfacing_enabled is off (PrepareChatContext never
		// reaches FileDates then).
		fileDates := &fileDatesAdapter{store: files.NewStore(db.Main)}
		if hasTurns {
			// Turn rows bypass orchestrator dispatch and teams entirely
			// (validated above: --team-id is already rejected when
			// hasTurns) and always run the standard PrepareChatContext
			// path — the same one production's condense→retrieve follow-up
			// handling uses. MultiTurnAdapter sits in front of a plain
			// ProductionContextAdapter, condensing each turn's query (or
			// carrying over the previous turn's sources for an answer_ref
			// reformat) before delegating.
			prod := eval.NewProductionContextAdapter(
				aiResolver,
				searchService,
				siteReader,
				flags,
				eval.WithTabularRouter(tabularRouter),
				eval.WithRecencyLister(recencyLister),
				eval.WithFileDates(fileDates),
			)
			adapter = eval.NewMultiTurnAdapter(prod, aiResolver, siteReader, keepRaw)
			slog.Info("eval: multi-turn replay mode on (golden set has turns; standard PrepareChatContext path, no orchestrator dispatch, no team)")
		} else if *teamID != "" {
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
				eval.WithTabularRouter(tabularRouter),
				eval.WithRecencyLister(recencyLister),
				eval.WithFileDates(fileDates),
			)
			slog.Info("eval: orchestrator-dispatch mode on (production-parity routing)")
		} else {
			// Router-free by design: this is the byte-stable
			// retrieval-only comparison branch (TabularRouter materially
			// changes retrieval and post-dates old byte-stable reports,
			// so it stays excluded here — same reasoning as the
			// orchestrator-dispatch branch above).
			//
			// RecencyLister is wired here too, unlike the router: with
			// --orchestrator-dispatch=false this branch is the ONLY way
			// to exercise recency listing without the LLM query-type
			// classifier's run-to-run non-determinism in the loop (Wave 2
			// Task 8 fix round 1 — the controller's requested
			// dispatch-off rerun found 0 "rag.recency_listing.fired"
			// events here before this line existed, since recency
			// listing is standard-path-only and this branch previously
			// carried no lister at all). Unlike TabularRouter, there is
			// no pre-existing byte-stable report to preserve compat with
			// here: RecencyLister was introduced by this same task, so
			// including it changes nothing anyone was already diffing
			// against.
			adapter = eval.NewProductionContextAdapter(
				aiResolver,
				searchService,
				siteReader,
				flags,
				eval.WithRecencyLister(recencyLister),
				eval.WithFileDates(fileDates),
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
			TabularRouter: trajTabularRouter,
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
		rep.TurnKindAggregates = eval.AggregateByTurnKind(rep.Questions, *topK)
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
	if baseline != nil {
		th := eval.RegressionThresholds{RecallPP: *regressRecallPP, MRRPP: *regressMRRPP}
		regs := eval.CheckRegression(*baseline, rep, th)
		fmt.Fprintln(os.Stdout)
		_ = eval.WriteDeltaTable(os.Stdout, *baseline, rep, regs)
		if len(regs) > 0 {
			for _, r := range regs {
				slog.Error("eval.regression", "route", r.Route, "metric", r.Metric, "baseline", r.Baseline, "candidate", r.Candidate, "delta_pp", r.DeltaPP)
			}
			os.Exit(3)
		}
	}
}

// refreshBM25StatsForGoldenSet recomputes BM25 statistics (per-KB doc
// count/avg length, per-term document frequency) for every KB referenced by
// the golden set, across every dim table that actually has rows for that
// KB — the intersection of vector.ListChunkTableDimensions and "has rows
// for this KB", per the --refresh-bm25-stats flag's contract. Runs before
// RunEval so an A/B never measures against missing or stale stats (per-KB
// staleness detection, W2-R5, is otherwise only driven by the worker's
// maintenance sweep). Best-effort: a bad KB id or a refresh failure is
// logged and skipped rather than aborting the whole eval run.
func refreshBM25StatsForGoldenSet(ctx context.Context, vectorDB, mainDB *pgxpool.Pool, questions []eval.Question) {
	kbIDs := map[string]struct{}{}
	for _, q := range questions {
		if q.KbID != "" {
			kbIDs[q.KbID] = struct{}{}
		}
	}
	if len(kbIDs) == 0 {
		return
	}

	dims, err := vector.NewChunkService(vectorDB).ListChunkTableDimensions(ctx)
	if err != nil {
		slog.Error("--refresh-bm25-stats: list chunk table dimensions failed", "error", err)
		return
	}

	// Controller-found gap: the bm25_kb_stats_<dim>/bm25_term_stats_<dim>
	// side tables are normally created by the server/worker boot path
	// (migrate.EnsureVectorTables), which cmd/eval never runs — so
	// RefreshKB failed with "relation ... does not exist" against a dev
	// DB that had never booted the main app for a given dim.
	// EnsureBM25StatsTables is idempotent (CREATE TABLE IF NOT EXISTS
	// under an advisory lock), so calling it once per dim here is safe
	// even when the tables already exist.
	for _, dim := range dims {
		if err := vector.EnsureBM25StatsTables(ctx, vector.PgxpoolExec{Pool: vectorDB}, dim); err != nil {
			slog.Error("--refresh-bm25-stats: ensure stats tables failed", "dim", dim, "error", err)
		}
	}

	refresher := vector.NewBM25StatsRefresher(vectorDB, mainDB)
	for kbIDStr := range kbIDs {
		kbID, perr := uuid.Parse(kbIDStr)
		if perr != nil {
			slog.Warn("--refresh-bm25-stats: skipping golden-set kb id (not a UUID)", "kb_id", kbIDStr, "error", perr)
			continue
		}
		for _, dim := range dims {
			table := vector.GetVectorTableName(dim)
			if !vector.IsValidVectorTableName(table) {
				continue
			}
			var hasRows bool
			checkErr := vectorDB.QueryRow(ctx,
				fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM "%s" WHERE kb_id = $1)`, table),
				kbID,
			).Scan(&hasRows)
			if checkErr != nil || !hasRows {
				continue
			}
			if err := refresher.RefreshKB(ctx, kbID, dim); err != nil {
				slog.Error("--refresh-bm25-stats: refresh failed", "kb_id", kbIDStr, "dim", dim, "error", err)
				continue
			}
			slog.Info("--refresh-bm25-stats: refreshed", "kb_id", kbIDStr, "dim", dim)
		}
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

// chatOverlayReader wraps a chat.SiteConfigReader and serves a fixed set of
// chat-layer keys from an in-memory map for one run, without mutating
// site_configs — the chat-side twin of overlaySiteConfig (which serves the
// internal/vector reader). Any key not in the overlay delegates to the inner
// reader. Wrappers compose, so it can sit on top of cragOverrideReader.
type chatOverlayReader struct {
	inner    chat.SiteConfigReader
	overlays map[string]string
}

// buildChatOverlays turns the chat-layer CLI flags (the two long-context
// ones and --conflict-surfacing) into the overlay map, then merges in extra
// (the parsed --chat-overlay key=value pairs, W6-R18) LAST. An empty named
// flag contributes NO entry, which is the whole point: an entry with an
// empty value would pin the key to the zero value for the run instead of
// delegating to the live site_config. Because extra is merged last, a
// --chat-overlay entry for a key one of the three named flags ALSO sets
// wins over that named flag (documented precedence, not map-iteration
// luck) — e.g. --conflict-surfacing on --chat-overlay
// chat_conflict_surfacing_enabled=false ends with the key false.
func buildChatOverlays(longContextEnabled, longContextMode, conflictSurfacing, policyJSON string, extra map[string]string) map[string]string {
	overlays := map[string]string{}
	switch longContextEnabled {
	case "on":
		overlays["chat_longcontext_enabled"] = "true"
	case "off":
		overlays["chat_longcontext_enabled"] = "false"
	}
	if longContextMode != "" {
		overlays["chat_longcontext_mode"] = longContextMode
	}
	switch conflictSurfacing {
	case "on":
		overlays["chat_conflict_surfacing_enabled"] = "true"
	case "off":
		overlays["chat_conflict_surfacing_enabled"] = "false"
	}
	// W6-R6. Empty contributes no entry for the same reason as above: an
	// empty overlay value would pin the key to "no policy" for the run
	// instead of delegating to the live site_config. validatePolicyFlag has
	// already rejected an --policy that collides with a --chat-overlay for
	// the same key, so the merge below cannot silently drop a validated
	// document.
	if strings.TrimSpace(policyJSON) != "" {
		overlays[chatOrchestratorPolicyKey] = policyJSON
	}
	for k, v := range extra {
		overlays[k] = v
	}
	return overlays
}

// chatOrchestratorPolicyKey is the site_config key --policy overlays. Named
// once so the flag, the collision check and the overlay builder cannot drift.
const chatOrchestratorPolicyKey = "chat_orchestrator_policy"

// validatePolicyFlag checks the --policy document with the same validator the
// admin save path runs (chatpolicy.ValidateOrchestratorPolicyJSON) and refuses
// the ambiguous command line where --chat-overlay ALSO sets the policy key.
//
// Empty is always valid: it means "read the live site_config", the documented
// default. The collision is an error rather than a precedence rule because the
// two flags disagree on validation — --chat-overlay would smuggle an
// unvalidated document past this check and win the merge.
func validatePolicyFlag(policyJSON string, extra map[string]string) error {
	if _, collides := extra[chatOrchestratorPolicyKey]; collides {
		return fmt.Errorf("--chat-overlay %s=... conflicts with --policy; use --policy (it validates the document)", chatOrchestratorPolicyKey)
	}
	if strings.TrimSpace(policyJSON) == "" {
		return nil
	}
	return chatpolicy.ValidateOrchestratorPolicyJSON(policyJSON)
}

// checkPolicyFlagReachable rejects a --policy value that the run cannot
// possibly apply, per the final-review S12 finding: nothing on the
// retrieval-only path or the --orchestrator-dispatch=false path reads
// chat_orchestrator_policy, so a run in either shape silently measures
// nothing while looking like a real measurement. --trajectory is exempted
// (not required to also carry --production-context/--orchestrator-dispatch)
// because the policy document there is informational only — it reaches the
// per-decision policy_rule join key, never SelectOrchestratorWithPolicy's
// actual routing decision for a trajectory run — so it is never a silent
// no-op there, only a lesser measurement than --production-context
// --orchestrator-dispatch=true would give.
func checkPolicyFlagReachable(policyJSON string, productionContext, orchestratorDispatch, trajectoryMode bool) error {
	if strings.TrimSpace(policyJSON) == "" {
		return nil
	}
	if trajectoryMode {
		return nil
	}
	if productionContext && orchestratorDispatch {
		return nil
	}
	return fmt.Errorf("--policy has no effect without --production-context --orchestrator-dispatch=true (or --trajectory, where it is informational only) — chat_orchestrator_policy is never read on the retrieval-only or --orchestrator-dispatch=false path")
}

// chatOverlayFlag is a repeatable flag.Value collecting raw --chat-overlay
// key=value strings in the order they appeared on the command line.
// Validation and key=value splitting happen once, in
// parseChatOverlayFlags, after flag.Parse() returns — a Set() error would
// abort flag.Parse() itself with a less useful message than the
// usage-error/exit(2) pattern every other CLI validation in this file
// uses, so Set() only collects.
type chatOverlayFlag []string

func (f *chatOverlayFlag) String() string {
	if f == nil {
		return ""
	}
	return strings.Join(*f, ",")
}

func (f *chatOverlayFlag) Set(v string) error {
	*f = append(*f, v)
	return nil
}

// parseChatOverlayFlags splits each raw "key=value" pair from repeated
// --chat-overlay flags into a map. A later entry for the same key wins
// (matches Go flag semantics elsewhere: last one wins), which is also what
// makes --chat-overlay's precedence over the three named chat-overlay
// flags well-defined in buildChatOverlays (a single, consistent
// last-write-wins rule end to end). strings.Cut splits at the FIRST '=',
// so the key half can never itself contain '=' — only a missing '=' or an
// empty key before it are rejected.
func parseChatOverlayFlags(raw []string) (map[string]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(raw))
	for _, pair := range raw {
		key, value, ok := strings.Cut(pair, "=")
		if !ok {
			return nil, fmt.Errorf("--chat-overlay %q is missing '=' (want key=value)", pair)
		}
		if key == "" {
			return nil, fmt.Errorf("--chat-overlay %q has an empty key", pair)
		}
		out[key] = value
	}
	return out, nil
}

func (w *chatOverlayReader) GetSiteConfigValue(ctx context.Context, key string) (*string, error) {
	if v, ok := w.overlays[key]; ok {
		return &v, nil
	}
	return w.inner.GetSiteConfigValue(ctx, key)
}

// fileDatesAdapter implements chat.FileDateLookup over the main-DB files
// store, exactly like internal/app/routes.go's identically-named adapter:
// internal/files must not import internal/chat, so the two flat date structs
// meet in one copy per entrypoint. cmd/eval needs it so a
// --conflict-surfacing run sees the same published_at/created_at date lines
// a real chat turn sees.
type fileDatesAdapter struct {
	store *files.PGStore
}

func (a *fileDatesAdapter) FileDatesByIDs(ctx context.Context, ids []string) (map[string]chat.FileDates, error) {
	if a.store == nil {
		return nil, nil
	}
	rows, err := a.store.FileDatesByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string]chat.FileDates, len(rows))
	for id, d := range rows {
		out[id] = chat.FileDates{CreatedAt: d.CreatedAt, PublishedAt: d.PublishedAt}
	}
	return out, nil
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
