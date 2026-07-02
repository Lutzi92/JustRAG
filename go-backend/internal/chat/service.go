package chat

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/prompts"
	"github.com/justrag/go-backend/internal/splitter"
	"github.com/justrag/go-backend/internal/vector"
)

// queryDecomposeTimeout caps the sub-question decomposition LLM call.
// Mirrors vector.enhanceQueryTimeout (15s) so the chat critical path
// has the same upper bound as the other pre-search LLM-enhancement
// calls (HyDE, MultiQuery, StepBack) when they run.
const queryDecomposeTimeout = 15 * time.Second

// ---------------------------------------------------------------------------
// Public types
// ---------------------------------------------------------------------------

// ChatContextParams holds the parameters for preparing chat context.
type ChatContextParams struct {
	KbID        string
	SearchQuery string
	Language    string // "de" or "en"
	Enhance     string // "rewrite", "expand", "spell", or ""
	FileIDs     []string
	HyDE        bool
	MultiQuery  bool
	// StepBack triggers step-back retrieval at search time when the query
	// is classified as complex_reasoning. Production callers normally
	// inherit this from the `step_back_enabled` site_config; the field
	// lets the eval CLI force-enable it for grid search regardless of
	// the live site_config state.
	StepBack       bool
	KbSystemPrompt string
	// ForceEnumerationPrepass overrides the IsEnumerationQuery
	// classifier. nil = use classifier (current production behavior).
	// &true = always run pre-pass. &false = always skip. Used by eval
	// ablation to isolate the pre-pass's contribution.
	ForceEnumerationPrepass *bool
	// QueryType is the upstream query-type classification (see
	// vector.QueryType* constants). Plumbed through to SearchOptions so
	// the search pipeline can surface it on the rag.search.stages event
	// and downstream adaptive logic (Phase 2) can dispatch on it.
	QueryType string
	// Emit is the optional trajectory-event sink. PrepareChatContext and
	// runCRAG forward CRAG branch decisions (proceed / rewrite / abstain)
	// through this callback so streaming clients can render the agent's
	// reasoning steps. Nil-safe: non-streaming callers (eval harness,
	// non-streaming SendMessage) leave it unset and the helper short-
	// circuits.
	Emit func(map[string]any)
	// GraphSubgraphChunkIDs are chunk IDs the AP-C4 graph router
	// resolved from the KB's knowledge graph (chat.ResolveGraphChunks).
	// When non-empty, they are forwarded to the search pipeline via
	// vector.SearchOptions.GraphChunkIDs, which folds them into RRF
	// alongside the BM25 + vector + sub-query lists. Empty (the
	// default) preserves the legacy behaviour: graph routing remains
	// diagnostic-only and only the trajectory event is emitted.
	GraphSubgraphChunkIDs []string
	// BridgeChunks forwards the bridge-evidence tally (chunk_id -> bridge
	// count) into vector.SearchOptions.BridgeChunks for post-rerank
	// multi-hop boosting. Nil (default) leaves the boost inert.
	BridgeChunks map[string]int
	// CurrentDateLine is the localized current-date line to append to the
	// answer system prompt (empty when chat_date_awareness_enabled is off).
	// Set at dispatch via SystemPromptDateLine.
	CurrentDateLine string
	// RecencyLister backs the deterministic recency-listing path for
	// "what is new / recently added" queries (see recency_listing.go).
	// Nil disables the path — public API / OpenAI-compat / mcpserver
	// callers leave it unset.
	RecencyLister RecencyLister
}

// ChatSource represents a single source document surfaced in a chat response.
type ChatSource struct {
	Index    int     `json:"index"`
	FileName string  `json:"fileName"`
	FileID   string  `json:"fileId"`
	Content  string  `json:"content"`
	Score    float64 `json:"score"`
	Pages    []int   `json:"pages"`
	// ChunkID is the original chunk row id (UUID text). Populated by
	// every orchestrator when the source is built. Not serialised to
	// the frontend (the user only sees Content) — used internally by
	// the Phase F citation-validator descendant resolver, which needs
	// the summary's chunk id to walk down raptor_parent_id.
	ChunkID string `json:"-"`
	// NodeKind is "leaf" for ordinary chunks and "summary" for Phase F
	// RAPTOR-built summary nodes. Empty == "leaf". Surfaced to the
	// answer prompt via renderSourceHeader so the LLM knows the
	// snippet is paraphrased prose (and should paraphrase its
	// claims, not quote directly).
	NodeKind string `json:"nodeKind,omitempty"`
	// TreeLevel is 0 for leaves, 1..N for RAPTOR summaries.
	TreeLevel int `json:"treeLevel,omitempty"`
	// DescendantContents is populated by the chat post-response path
	// when NodeKind == "summary": the verbatim text of every
	// transitive leaf descendant under this summary. The citation
	// validator's n-gram check ORs source content with descendants
	// so a paraphrased summary still validates when an underlying
	// leaf supports the cited claim. Not serialised to JSON — the
	// frontend only renders the user-visible summary Content.
	DescendantContents []string `json:"-"`
}

// renderSourceHeader returns the per-source prefix line used by the
// answer prompt in every orchestrator (deep / agentic / plan-execute
// / supervisor / standard). For leaves the output matches the legacy
// inline Sprintf calls byte-for-byte:
//
//	[N] [Source: file.pdf, p. 4]
//
// For Phase F RAPTOR summaries the level is tagged inline:
//
//	[N] (summary, level 2) [Source: file.pdf, p. 4]
func renderSourceHeader(idx int, fileName, pageAnnotation, nodeKind string, treeLevel int) string {
	if nodeKind == "community_summary" {
		return fmt.Sprintf("[%d] (knowledge-graph community) [Source: %s%s]", idx, fileName, pageAnnotation)
	}
	if nodeKind == "summary" {
		level := treeLevel
		if level <= 0 {
			level = 1
		}
		return fmt.Sprintf("[%d] (summary, level %d) [Source: %s%s]", idx, level, fileName, pageAnnotation)
	}
	return fmt.Sprintf("[%d] [Source: %s%s]", idx, fileName, pageAnnotation)
}

// ChatContext is the result of the RAG context preparation pipeline.
type ChatContext struct {
	EnhancedQuery string
	SystemPrompt  string
	Sources       []ChatSource
	Context       string // raw context text assembled from chunks
	// FinalChunks is the chunk list the LLM will see — post-CRAG,
	// post-neighbor-expansion, post-truncation, post-sandwich order.
	// Exposed so callers (eval harness; shadow-mode in Phase 3) can
	// compute retrieval metrics on what actually reached the prompt,
	// not on the pre-CRAG search result. The user-facing `Sources`
	// field is a lossy projection of this list.
	FinalChunks []vector.SearchChunk
	// Abstain is set when CRAG-style relevance grading determined the
	// retrieved context is not sufficient to answer (even after a query
	// rewrite). The system prompt already carries an abstain notice; this
	// flag exists so callers can also surface the decision in metrics or
	// UI badges without re-parsing the system prompt.
	Abstain bool
	// StructuredTable is set only by the corpus-table orchestrator. When
	// non-nil, the HTTP layer emits it as a {"structuredTable": …} SSE event
	// and persists it on the AI message. nil for every other orchestrator.
	StructuredTable *StructuredTable
}

// ---------------------------------------------------------------------------
// Token helpers
// ---------------------------------------------------------------------------

// CountTokensApprox returns a token count for text. Uses tiktoken (cl100k_base)
// via splitter.CountTokens, which falls back to a chars/4 heuristic only if the
// tokenizer fails to initialize. Kept under the historic name for callers that
// still reference it; new code should call splitter.CountTokens directly.
func CountTokensApprox(text string) int {
	return splitter.CountTokens(text)
}

// TruncateChunksToFit selects the highest-scored subset of chunks that fits
// within maxTokens. Survivors are returned in their original input order so
// downstream re-ordering (e.g. SandwichOrder) is unaffected.
//
// When all chunks fit, the input is returned unchanged.
//
// When the input is empty or maxTokens <= 0, returns an empty slice.
func TruncateChunksToFit(chunks []vector.SearchChunk, maxTokens int) []vector.SearchChunk {
	if len(chunks) == 0 || maxTokens <= 0 {
		return nil
	}

	// Pre-compute token counts once.
	tokens := make([]int, len(chunks))
	total := 0
	for i, c := range chunks {
		tokens[i] = splitter.CountTokens(c.Content)
		total += tokens[i]
	}
	if total <= maxTokens {
		return chunks
	}

	// Score-aware selection: greedily pick chunks with highest score that fit.
	// Tie-break by lower input index (preserves sandwich/original order on
	// ties so the result is deterministic).
	type idxScore struct {
		idx   int
		score float64
		toks  int
	}
	indexed := make([]idxScore, len(chunks))
	for i, c := range chunks {
		indexed[i] = idxScore{idx: i, score: c.Score, toks: tokens[i]}
	}
	slices.SortStableFunc(indexed, func(a, b idxScore) int {
		if c := cmp.Compare(b.score, a.score); c != 0 {
			return c
		}
		return cmp.Compare(a.idx, b.idx)
	})

	keep := make(map[int]struct{}, len(chunks))
	used := 0
	for _, e := range indexed {
		if used+e.toks > maxTokens {
			continue
		}
		keep[e.idx] = struct{}{}
		used += e.toks
	}

	// Restore original input order among survivors.
	out := make([]vector.SearchChunk, 0, len(keep))
	for i, c := range chunks {
		if _, ok := keep[i]; ok {
			out = append(out, c)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Ordering helpers
// ---------------------------------------------------------------------------

// SandwichOrder reorders chunks so that the highest-scored chunks appear at
// both ends of the slice. Even-indexed chunks go to the front (in order) and
// odd-indexed chunks go to the back (in reverse order).
//
// Example: [1st, 2nd, 3rd, 4th, 5th, 6th] → [1st, 3rd, 5th, 6th, 4th, 2nd]
func SandwichOrder(chunks []vector.SearchChunk) []vector.SearchChunk {
	if len(chunks) <= 2 {
		return chunks
	}

	front := make([]vector.SearchChunk, 0, (len(chunks)+1)/2)
	back := make([]vector.SearchChunk, 0, len(chunks)/2)
	for i, c := range chunks {
		if i%2 == 0 {
			front = append(front, c)
		} else {
			back = append(back, c)
		}
	}

	// Reverse back so highest-scored odd chunks end up at the end.
	for i, j := 0, len(back)-1; i < j; i, j = i+1, j-1 {
		back[i], back[j] = back[j], back[i]
	}

	return append(front, back...)
}

// ---------------------------------------------------------------------------
// Confidence detection
// ---------------------------------------------------------------------------

// IsLowConfidence returns true when the search result quality is too low to
// give a confident answer: no chunks, fewer than 3 chunks, or all chunks have
// a vector score below the 0.4 threshold.
func IsLowConfidence(chunks []vector.SearchChunk) bool {
	if len(chunks) < 3 {
		return true
	}
	for _, c := range chunks {
		if c.VectorScore >= 0.4 {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// CRAG (Corrective RAG) — config + decision
// ---------------------------------------------------------------------------

// cragConfig holds the runtime CRAG knobs read from site_configs, plus
// the Phase 2 §C adaptive_routing_enabled flag — bundled here so the
// existing readSiteConfigBatch pulls it in the same DB round-trip as
// the CRAG keys, rather than paying for a second serialized lookup
// per request when CRAG is on.
type cragConfig struct {
	enabled                bool
	minRelevant            int
	graderModel            string
	adaptiveRoutingEnabled bool
	stepBackEnabled        bool
}

// shouldBypassCRAGForRouting decides whether adaptive routing should disable
// CRAG for a given (queryType, enhance) pair. Callers must additionally check
// that CRAG is enabled and adaptiveRoutingEnabled — this helper only encodes
// the per-request eligibility rule. The bypass applies to:
//   - lookup: named-entity queries with one canonical answer; CRAG's grader
//     adds latency without recall benefit (Phase 2 §C).
//   - enumeration: BM25-seeded extraction (`internal/ai/enumeration.go`)
//     already enforces verified completeness, making per-chunk grading
//     redundant.
//
// In both cases an explicit Enhance signals "I want maximum quality" and
// overrides the latency optimization.
func shouldBypassCRAGForRouting(queryType string, enhance string) bool {
	if enhance != "" {
		return false
	}
	return queryType == vector.QueryTypeLookup || queryType == vector.QueryTypeEnumeration
}

// cragConfigKeys are the site_configs keys consumed by loadCRAGConfig.
var cragConfigKeys = []string{
	"crag_enabled",
	"crag_min_relevant_chunks",
	"crag_grader_model",
	"adaptive_routing_enabled",
	"step_back_enabled",
	// model_tier_fast is included in the batch so the grader-model
	// tier fallback (P7) doesn't pay an extra DB round-trip on the
	// CRAG hot path.
	"model_tier_fast",
}

// loadCRAGConfig reads CRAG configuration from site_configs. Defaults:
//   - enabled = false (opt-in because of the per-call latency budget)
//   - minRelevant = 3 (matches the default top-k)
//   - graderModel = "" (use the resolver's chat model)
//
// A nil reader yields defaults — same behaviour as if every key were absent.
// One DB round-trip when the reader supports batching; per-key fallback otherwise.
func loadCRAGConfig(ctx context.Context, reader SiteConfigReader) cragConfig {
	cfg := cragConfig{enabled: false, minRelevant: 3}
	if reader == nil {
		return cfg
	}

	values := readSiteConfigBatch(ctx, reader, cragConfigKeys)

	cfg.enabled = parseBool(values["crag_enabled"], false)
	cfg.minRelevant = parseInt(values["crag_min_relevant_chunks"], 3, 1, 50)
	cfg.graderModel = parseString(values["crag_grader_model"])
	// P7 fast-tier fallback: when the per-task crag_grader_model is
	// unset (or whitespace), inherit from model_tier_fast so a
	// deployment-wide fast model applies without re-pinning every
	// fast-tier key. Per-task value still wins when set.
	if cfg.graderModel == "" {
		cfg.graderModel = parseString(values["model_tier_fast"])
	}
	cfg.adaptiveRoutingEnabled = parseBool(values["adaptive_routing_enabled"], false)
	cfg.stepBackEnabled = parseBool(values["step_back_enabled"], false)

	return cfg
}

// cragAction is the branch the CRAG decision picked.
type cragAction string

const (
	cragProceed cragAction = "proceed"
	cragRewrite cragAction = "rewrite"
	cragAbstain cragAction = "abstain"
)

// decideCRAGAction encodes the CRAG branch table:
//
//	first round (alreadyRewritten=false):
//	  relevant >= min                 → proceed
//	  relevant >= 1 AND relevantFiles == 1 → proceed (single-doc pattern)
//	  relevant >= 1                   → rewrite (one retry with a rewritten query)
//	  ambiguous >= 1                  → proceed (fail-open: ai.GradeRelevance
//	                                    maps parse and LLM errors to
//	                                    GradeAmbiguous, so an all-ambiguous
//	                                    bucket is indistinguishable from a
//	                                    degraded grader. Blocking the answer
//	                                    would silently break the documented
//	                                    fail-open contract; trust retrieval
//	                                    and let the user see the chunks.)
//	  otherwise                       → abstain (all chunks explicitly graded
//	                                    irrelevant or no chunks at all — the
//	                                    KB genuinely lacks it)
//
//	second round (alreadyRewritten=true):
//	  relevant >= 1 OR ambiguous >= 1 → proceed (same fail-open rationale,
//	                                   plus the retry is already the fallback
//	                                   path — dropping usable material here
//	                                   compounds a single grader flake into
//	                                   a silent non-answer)
//	  otherwise → abstain
//
// The single-doc shortcut exists because a query that targets ONE document
// (e.g. "how does process X in Richtlinie Y work?") typically produces only
// 1-3 relevant chunks — all from that one document — surrounded by many
// off-topic chunks from other files. The count-only threshold
// (`min_relevant`, default 3) would see "only 2 relevant" and fire the
// rewrite; the rewrite then loses the target document entirely. Detecting
// that the relevant chunks are concentrated in a single file is a strong
// signal that retrieval hit the right place and a rewrite would hurt.
//
// minRelevant is the configurable threshold (site_config crag_min_relevant_chunks).
// relevantFiles is the count of distinct FileIDs among chunks marked relevant.
func decideCRAGAction(relevant, ambiguous, minRelevant, relevantFiles int, alreadyRewritten bool) cragAction {
	if alreadyRewritten {
		if relevant >= 1 || ambiguous >= 1 {
			return cragProceed
		}
		return cragAbstain
	}
	switch {
	case relevant >= minRelevant:
		return cragProceed
	case relevant >= 1 && relevantFiles == 1:
		// Single-doc pattern: the relevant chunks all come from one file.
		// The user is asking about that document; rewriting would search
		// with different terms and likely lose the target.
		return cragProceed
	case relevant >= 1:
		return cragRewrite
	case ambiguous >= 1:
		return cragProceed
	default:
		return cragAbstain
	}
}

// countRelevantFiles returns the number of distinct FileIDs across chunks
// whose grader verdict is GradeRelevant. Used by decideCRAGAction to
// detect the single-doc query pattern.
func countRelevantFiles(chunks []vector.SearchChunk) int {
	seen := make(map[string]struct{}, len(chunks))
	for _, c := range chunks {
		if c.Grade == ai.GradeRelevant && c.FileID != "" {
			seen[c.FileID] = struct{}{}
		}
	}
	return len(seen)
}

// runCRAG executes the CRAG decision loop. It mutates result in place when a
// rewrite-and-retry produces a fresher/graded chunk set. Returns true if the
// downstream system prompt must instruct the LLM to abstain.
//
// Errors from the rewrite or second search are non-fatal: we log and fall
// back to the original (already-graded) result so the user still gets *an*
// answer instead of a hard 5xx.
func runCRAG(
	ctx context.Context,
	aiResolver *ai.ConfigResolver,
	searchSvc vector.Searcher,
	cfg cragConfig,
	result *vector.SearchResult,
	params ChatContextParams,
) bool {
	if !cfg.enabled || !result.Graded {
		return false
	}

	relevant, ambiguous, irrelevant := countGrades(result.Chunks)
	relevantFiles := countRelevantFiles(result.Chunks)
	action := decideCRAGAction(relevant, ambiguous, cfg.minRelevant, relevantFiles, false)

	logctx.From(ctx).Info("rag.crag.decide",
		"round", 1,
		"action", string(action),
		"relevant", relevant,
		"ambiguous", ambiguous,
		"irrelevant", irrelevant,
		"relevant_files", relevantFiles,
		"min_relevant", cfg.minRelevant,
	)

	switch action {
	case cragProceed:
		observability.RecordCRAGDecision(ctx, observability.CRAGActionProceed)
		emitTrajectory(params.Emit,
			TrajectoryEvent{Stage: "decision", Decision: "crag_proceed", Reason: "relevant_chunks_sufficient"},
			nil,
		)
		return false

	case cragRewrite:
		observability.RecordCRAGDecision(ctx, observability.CRAGActionRewrite)
		emitTrajectory(params.Emit,
			TrajectoryEvent{Stage: "decision", Decision: "crag_rewrite", Reason: "low_relevance_retrying"},
			nil,
		)
		rewritten, err := ai.RewriteQuery(ctx, aiResolver, params.SearchQuery, params.KbID, params.Language)
		if err != nil || strings.TrimSpace(rewritten) == "" || rewritten == params.SearchQuery {
			// Rewrite produced nothing usable — proceed with the original
			// graded chunks rather than abstaining. The user still gets an
			// answer; the metric already records the rewrite attempt.
			return false
		}
		// Propagate the caller's Enhance setting into the retry. Dropping it
		// sounds clean on paper (the rewritten query is "already a
		// transformation"), but measurably hurt recall in practice — for
		// users relying on Enhance="expand" the retry lost the synonym/
		// variant boost that originally surfaced chunks the plain rewrite
		// doesn't. Accept the theoretical double-transform trade-off; it
		// restores answer completeness.
		retryOpts := vector.SearchOptions{
			Enhance:     params.Enhance,
			FileIDs:     params.FileIDs,
			HyDE:        params.HyDE,
			MultiQuery:  params.MultiQuery,
			Grade:       true,
			GraderModel: cfg.graderModel,
			QueryType:   params.QueryType,
		}
		retry, err := searchSvc.Search(ctx, params.KbID, rewritten, 0, retryOpts)
		if err != nil || retry == nil || len(retry.Chunks) == 0 {
			return false
		}
		// Adopt the second-round result. EnhancedQuery surfaces the
		// rewritten query to the UI so users can see what was actually
		// run.
		result.Chunks = retry.Chunks
		result.EnhancedQuery = rewritten
		result.Graded = retry.Graded
		result.ContextWindowSize = retry.ContextWindowSize
		result.TableName = retry.TableName

		emitTrajectory(params.Emit,
			TrajectoryEvent{
				Stage:    "decision",
				Decision: "crag_rewrite_complete",
				Query:    rewritten,
				Chunks:   chunkRefs(result.Chunks),
			},
			nil,
		)

		rel2, amb2, irr2 := countGrades(result.Chunks)
		relFiles2 := countRelevantFiles(result.Chunks)
		final := decideCRAGAction(rel2, amb2, cfg.minRelevant, relFiles2, true)
		logctx.From(ctx).Info("rag.crag.decide",
			"round", 2,
			"action", string(final),
			"relevant", rel2,
			"ambiguous", amb2,
			"irrelevant", irr2,
			"min_relevant", cfg.minRelevant,
		)
		// Record the final outcome in addition to the rewrite counter.
		// The rewrite counter tracks "we tried a rewrite"; proceed/abstain
		// track "what did we ultimately do". A rewrite-then-proceed thus
		// increments both `rewrite` and `proceed` — intentional, so
		// operators can see the rewrite path's hit rate.
		if final == cragAbstain {
			observability.RecordCRAGDecision(ctx, observability.CRAGActionAbstain)
			emitTrajectory(params.Emit,
				TrajectoryEvent{Stage: "decision", Decision: "crag_abstain", Reason: "rewrite_did_not_recover"},
				nil,
			)
			return true
		}
		observability.RecordCRAGDecision(ctx, observability.CRAGActionProceed)
		emitTrajectory(params.Emit,
			TrajectoryEvent{Stage: "decision", Decision: "crag_proceed_after_rewrite"},
			nil,
		)
		return false

	case cragAbstain:
		observability.RecordCRAGDecision(ctx, observability.CRAGActionAbstain)
		emitTrajectory(params.Emit,
			TrajectoryEvent{Stage: "decision", Decision: "crag_abstain", Reason: "no_relevant_chunks"},
			nil,
		)
		return true
	}
	return false
}

// countGrades is a chunk-aware wrapper around ai.CountByGrade that ignores
// chunks the grader didn't reach (Grade left as the empty string).
func countGrades(chunks []vector.SearchChunk) (relevant, ambiguous, irrelevant int) {
	for _, c := range chunks {
		switch c.Grade {
		case ai.GradeRelevant:
			relevant++
		case ai.GradeIrrelevant:
			irrelevant++
		case ai.GradeAmbiguous:
			ambiguous++
		}
	}
	return
}

// ---------------------------------------------------------------------------
// Page extraction from metadata
// ---------------------------------------------------------------------------

// pagesFromMetadata extracts a sorted list of page numbers from chunk metadata.
// It accepts both single values (float64 / int64) and slices stored under the
// "page" or "pages" keys.
func pagesFromMetadata(meta map[string]any) []int {
	var pages []int

	addFloat := func(v any) {
		switch n := v.(type) {
		case float64:
			pages = append(pages, int(n))
		case int:
			pages = append(pages, n)
		case int64:
			pages = append(pages, int(n))
		}
	}

	if v, ok := meta["page"]; ok {
		addFloat(v)
	}
	if v, ok := meta["pages"]; ok {
		switch p := v.(type) {
		case []any:
			for _, item := range p {
				addFloat(item)
			}
		case []int:
			pages = append(pages, p...)
		}
	}

	return pages
}

// ---------------------------------------------------------------------------
// Evidentiality compression (ECoRAG, T2-3)
// ---------------------------------------------------------------------------

// applyEvidentialityCompression runs the ECoRAG one-shot classifier
// over the post-rerank chunk list and drops chunks scoring below the
// operator's threshold. Best-effort: any failure (LLM error, parse
// error, missing scores) passes the original chunk through, so the
// pipeline never loses evidence to a misbehaving classifier.
//
// Why post-sandwich-order: the LLM scores each chunk independently
// so the input ordering doesn't affect the scores, but we run AFTER
// SandwichOrder so the surviving chunks keep their lost-in-the-
// middle-optimised position. Running before SandwichOrder would
// require a second reorder pass that adds no value.
func applyEvidentialityCompression(
	ctx context.Context,
	aiResolver *ai.ConfigResolver,
	siteConfig SiteConfigReader,
	params ChatContextParams,
	chunks []vector.SearchChunk,
) []vector.SearchChunk {
	contents := make([]string, len(chunks))
	for i, c := range chunks {
		// We feed the chunk Content only — the contextual prefix
		// is appended at prompt-assembly time and isn't part of
		// the evidentiality signal we want scored. (A chunk
		// without enrichment must be evaluable on its raw text.)
		contents[i] = c.Content
	}
	threshold := ChatContextCompressionThreshold(ctx, siteConfig)
	model := ChatContextCompressionModel(ctx, siteConfig)
	scores, err := ai.ScoreEvidentiality(ctx, aiResolver, params.SearchQuery, contents, params.KbID, params.Language, model)
	if err != nil {
		logctx.From(ctx).Warn("rag.context_compression.llm_error",
			"error", err,
			"chunk_count", len(chunks),
		)
		observability.RecordContextCompressionDecision("llm_error")
		return chunks
	}
	if len(scores) == 0 {
		observability.RecordContextCompressionDecision("empty")
		return chunks
	}

	// Filter: keep chunks whose score is at or above the threshold,
	// OR whose score is missing (LLM didn't return an entry —
	// safer to keep than to drop).
	kept := chunks[:0]
	var droppedCount int
	for i, c := range chunks {
		s, ok := scores[i]
		if !ok || s >= threshold {
			kept = append(kept, c)
			continue
		}
		droppedCount++
	}
	// Defensive: never drop EVERY chunk — a degenerate classifier
	// run could surface zero evidence and leave the answer LLM
	// with nothing to cite. When that happens, fall back to the
	// pre-filter set and log the anomaly.
	if len(kept) == 0 {
		logctx.From(ctx).Warn("rag.context_compression.all_dropped",
			"chunk_count", len(chunks),
			"threshold", threshold,
		)
		observability.RecordContextCompressionDecision("all_dropped")
		return chunks
	}
	if droppedCount == 0 {
		observability.RecordContextCompressionDecision("no_drops")
		return kept
	}
	logctx.From(ctx).Info("rag.context_compression.fired",
		"before", len(chunks),
		"after", len(kept),
		"dropped", droppedCount,
		"threshold", threshold,
	)
	observability.RecordContextCompressionDecision("fired")
	observability.RecordContextCompressionDropped(droppedCount)
	return kept
}

// ---------------------------------------------------------------------------
// Sub-question decomposition (DecomposeRAG, T1-1)
// ---------------------------------------------------------------------------

// maybeDecomposeQuery runs the sub-question decomposition LLM call when
// the operator flag is on and the query is a complex_reasoning one,
// appending the produced sub-questions to opts.SubQueries so the search
// pipeline fans them out as additional retrieval queries (folded into
// the RRF pool via runMultiQuerySearches / runMultiQueryBM25Searches).
//
// Best-effort: any failure short-circuits to the no-decomposition path
// and records the outcome via observability.RecordQueryDecomposeDecision.
// Trajectory events flow through params.Emit so streaming clients can
// surface the decomposition step.
//
// Pre-populated opts.SubQueries (e.g. when a hypothetical caller sets
// them before invoking PrepareChatContext) is treated as the caller
// having made a deliberate choice — decomposition skips with
// outcome="skipped_existing" rather than appending and risking
// double-decomposition. Plan-Execute does its own structured
// decomposition outside PrepareChatContext, so this guard is mostly
// defensive against future callers.
func maybeDecomposeQuery(
	ctx context.Context,
	aiResolver *ai.ConfigResolver,
	siteConfig SiteConfigReader,
	params ChatContextParams,
	opts *vector.SearchOptions,
) {
	if siteConfig == nil {
		return
	}
	if !QueryDecomposeEnabled(ctx, siteConfig) {
		// Don't record a metric for the disabled path — the flag's
		// state is itself the signal, and emitting a counter on every
		// chat turn for a disabled feature is pure noise.
		return
	}
	if params.QueryType != vector.QueryTypeComplexReasoning {
		observability.RecordQueryDecomposeDecision("skipped_route")
		return
	}
	if len(opts.SubQueries) > 0 {
		observability.RecordQueryDecomposeDecision("skipped_existing")
		return
	}

	decomposeCtx, cancel := context.WithTimeout(ctx, queryDecomposeTimeout)
	defer cancel()

	start := time.Now()
	model := QueryDecomposeModel(decomposeCtx, siteConfig)
	subQueries, err := ai.GenerateSubQueriesWithModel(decomposeCtx, aiResolver, params.SearchQuery, params.KbID, params.Language, model)
	observability.QueryDecomposeSeconds.Observe(time.Since(start).Seconds())

	if err != nil {
		observability.RecordQueryDecomposeDecision("llm_error")
		logctx.From(ctx).Warn("rag.decompose.llm_error",
			"error", err,
			"duration_ms", time.Since(start).Milliseconds(),
		)
		return
	}
	if len(subQueries) == 0 {
		observability.RecordQueryDecomposeDecision("empty_output")
		observability.RecordQueryDecomposeSubQueries(0)
		return
	}

	opts.SubQueries = append(opts.SubQueries, subQueries...)
	observability.RecordQueryDecomposeDecision("fired")
	observability.RecordQueryDecomposeSubQueries(len(subQueries))
	logctx.From(ctx).Info("rag.decompose.fired",
		"original_query", params.SearchQuery,
		"sub_queries", subQueries,
		"duration_ms", time.Since(start).Milliseconds(),
	)
	if params.Emit != nil {
		params.Emit(map[string]any{
			"type":        "decompose",
			"sub_queries": subQueries,
			"model":       model,
			"duration_ms": time.Since(start).Milliseconds(),
		})
	}
}

// ---------------------------------------------------------------------------
// PrepareChatContext
// ---------------------------------------------------------------------------

// PrepareChatContext runs the retrieval and context-assembly pipeline:
//  1. Load CRAG config (opt-in, off by default).
//  2. Vector/hybrid search (with grading when CRAG is enabled).
//  3. CRAG decision (proceed / rewrite-and-retry / abstain).
//  4. Token budget truncation (120 000 tokens).
//  5. Sandwich ordering.
//  6. Context string assembly with source annotations.
//  7. Low-confidence + abstain notices on the system prompt.
//  8. Mapping chunks to ChatSource.
//
// siteConfig is optional — pass nil from callers that don't want CRAG (e.g.
// public API or OpenAI-compat handlers without a site-config-aware store).
func PrepareChatContext(
	ctx context.Context,
	aiResolver *ai.ConfigResolver,
	searchSvc vector.Searcher,
	siteConfig SiteConfigReader,
	params ChatContextParams,
) (*ChatContext, error) {
	const defaultMaxTokens = 120_000
	maxTokens := defaultMaxTokens

	cragCfg := loadCRAGConfig(ctx, siteConfig)

	// Phase 2 §C tiered routing (+ enumeration extension, routing-latency
	// batch 2026-05-06): disable CRAG for queries where it adds latency
	// without recall benefit when adaptive routing is on. The disable
	// applies only when ALL of:
	//   1. site_config adaptive_routing_enabled=true (read in the same
	//      readSiteConfigBatch as the CRAG keys above)
	//   2. params.QueryType is "lookup" (one canonical answer) OR
	//      "enumeration" (BM25-seeded extraction already enforces
	//      completeness, see shouldBypassCRAGForRouting)
	//   3. params.Enhance is empty (an explicit user enhancement signals
	//      "I want maximum quality" and overrides the latency optimization)
	// CRAG was already off for this KB → action="n_a", no behavior change.
	// CRAG was on but the verdict didn't trigger → action="kept".
	routingAction := "n_a"
	if cragCfg.enabled && cragCfg.adaptiveRoutingEnabled {
		if shouldBypassCRAGForRouting(params.QueryType, params.Enhance) {
			cragCfg.enabled = false
			routingAction = "disabled_crag"
			logctx.From(ctx).Info("rag.adaptive_routing.disabled_crag",
				"query_type", params.QueryType,
			)
		} else {
			routingAction = "kept"
		}
	}
	observability.RecordAdaptiveRoutingDecision(routingAction)

	opts := vector.SearchOptions{
		Enhance:       params.Enhance,
		FileIDs:       params.FileIDs,
		HyDE:          params.HyDE,
		MultiQuery:    params.MultiQuery,
		StepBack:      params.StepBack || cragCfg.stepBackEnabled,
		Grade:         cragCfg.enabled,
		GraderModel:   cragCfg.graderModel,
		QueryType:     params.QueryType,
		GraphChunkIDs: params.GraphSubgraphChunkIDs,
		BridgeChunks:  params.BridgeChunks,
		HyPESearch:    HyPESearchEnabled(ctx, siteConfig),
	}

	// T2-1 long-context routing: keyword-classifier-gated wide
	// retrieval for global-synthesis questions. When all three
	// conditions match (operator flag on, query is complex_reasoning,
	// classifier fires) Search() raises top-k to LongContextTopK and
	// skips MMR / score filtering; the chat pipeline below uses a
	// wider token budget (ChatLongContextMaxTokens, default 100k)
	// instead of the standard 120k baseline already pinned via
	// maxTokens. The two budgets are intentionally separate: the
	// long-context budget can be raised independently of the
	// general pipeline ceiling.
	longContextRoute := ShouldRouteLongContext(ctx, siteConfig, params.QueryType, params.SearchQuery)
	if longContextRoute {
		opts.LongContextMode = true
		// Replace the token budget with the long-context budget.
		// The two are kept separate (constant vs. site_config) so
		// operators can raise the long-context window independently
		// of the general pipeline ceiling.
		maxTokens = ChatLongContextMaxTokens(ctx, siteConfig)
		observability.RecordLongContextRoute("fired")
		logctx.From(ctx).Info("rag.longcontext.fired",
			"query", params.SearchQuery,
			"max_tokens", maxTokens,
		)
		if params.Emit != nil {
			params.Emit(map[string]any{
				"type":       "longcontext_route",
				"query":      params.SearchQuery,
				"max_tokens": maxTokens,
			})
		}
	}

	// Community-primed global search: for gated global-synthesis
	// complex_reasoning turns, fetch the top-K KG community summaries and
	// inject their chunk ids via the GraphChunkIDs lane (fetched by id,
	// bypassing the WHERE exclusion) so the answer is primed with
	// corpus-level overviews. Best-effort: an empty/failed primer search
	// leaves normal retrieval untouched.
	if shouldInjectCommunityPrimer(ctx, siteConfig, params.QueryType, params.SearchQuery) {
		primerIDs := retrieveCommunityPrimerIDs(ctx, searchSvc, params.KbID, params.SearchQuery,
			ChatCommunitySearchTopK(ctx, siteConfig))
		if len(primerIDs) > 0 {
			opts.GraphChunkIDs = append(opts.GraphChunkIDs, primerIDs...)
			observability.RecordCommunityPrimer("injected", len(primerIDs))
		} else {
			// Count the firing (Add(0) would be a no-op and leave the
			// "empty" counter permanently stuck at zero).
			observability.RecordCommunityPrimer("empty", 1)
		}
	}

	// Deterministic recency listing: for "what is new / recently added"
	// queries, window-scope retrieval to recently created files and
	// fetch the complete file listing for the window (injected as a
	// system-prompt addendum below). Semantic retrieval alone returns an
	// arbitrary subset for these content-free queries — prod bug
	// 2026-07-02, "Welche neuen Meldungen gibt es?" listed 1 of many.
	recency := applyRecencyListing(ctx, siteConfig, params.RecencyLister,
		params.KbID, params.SearchQuery, &opts, time.Now())
	if recency.fired && params.Emit != nil {
		params.Emit(map[string]any{
			"type":         "recency_listing",
			"since":        recency.sinceISO,
			"files_listed": len(recency.entries),
			"truncated":    recency.truncated,
		})
	}

	// Sub-question decomposition (DecomposeRAG, T1-1). Fires only for
	// complex_reasoning queries when the operator flag is on. Distinct
	// from MultiQuery (paraphrase): decomposition produces semantically
	// DIFFERENT sub-questions whose answers combine to resolve the
	// original. Results fold into opts.SubQueries, which the search
	// pipeline runs as additional vector + (optionally) BM25 RAG-Fusion
	// queries via runMultiQuerySearches and runMultiQueryBM25Searches.
	//
	// Plan-Execute already performs structured decomposition at the
	// orchestration level and bypasses PrepareChatContext for its
	// planning step, so the in-place SubQueries check below is the
	// guard against double-decomposition when a hypothetical caller
	// pre-populates the field. The standard fallback path is the
	// primary beneficiary of this flag.
	maybeDecomposeQuery(ctx, aiResolver, siteConfig, params, &opts)

	// Pass 0 so Search() applies the admin-configured default_top_k from site_configs.
	result, err := searchSvc.Search(ctx, params.KbID, params.SearchQuery, 0, opts)
	if err != nil {
		return nil, fmt.Errorf("chat: search: %w", err)
	}

	abstain := runCRAG(ctx, aiResolver, searchSvc, cragCfg, result, params)

	// Expand chunks with neighboring content from same file.
	if result.ContextWindowSize > 0 && result.TableName != "" {
		result.Chunks = searchSvc.ExpandNeighbors(
			ctx, result.Chunks, result.ContextWindowSize,
			result.TableName, params.KbID,
		)
	}

	chunks := TruncateChunksToFit(result.Chunks, maxTokens)
	chunks = SandwichOrder(chunks)

	// T2-3 ECoRAG evidentiality compression: when the chunk pool is
	// large enough that distractor chunks crowd out genuine evidence,
	// one fast-tier LLM call scores every chunk on whether it
	// DIRECTLY supports answering the query (distinct from relevance,
	// which the reranker already captured). Chunks below the
	// threshold drop out before prompt assembly. Skipped on abstain
	// (nothing to answer), when the flag is off, or when the pool
	// is below the min-chunks threshold (small pools rarely have
	// drop-able distractors and aren't worth the LLM call).
	// T2-1: also skipped under long-context mode — the whole point
	// of the wide pool is to hand the LLM many chunks; re-filtering
	// here would undo the routing.
	if !abstain && !longContextRoute && ChatContextCompressionEnabled(ctx, siteConfig) {
		minChunks := ChatContextCompressionMinChunks(ctx, siteConfig)
		if len(chunks) >= minChunks {
			chunks = applyEvidentialityCompression(ctx, aiResolver, siteConfig, params, chunks)
		}
	}

	// G5 multi-pass extraction: when the chunk count is high enough that
	// the answer LLM will struggle with position bias, fan out one fast-tier
	// extraction call per chunk and replace each chunk's content with only
	// the verbatim sentences relevant to the query. Chunks with no relevant
	// sentences drop out, which both shortens the prompt and removes
	// distractor passages. Skipped on abstain (nothing to answer) and when
	// the flag is off / threshold not met. Best-effort: per-chunk LLM
	// failures pass the original chunk through unchanged.
	// T2-1: also skipped under long-context mode — fanning out N
	// per-chunk LLM calls at the wide-pool scale (≤200 chunks) is
	// the cost regime we explicitly want to avoid; the wide pool
	// goes to the answer LLM raw.
	if !abstain && !longContextRoute && MultipassExtractionEnabled(ctx, siteConfig) {
		minChunks := MultipassMinChunks(ctx, siteConfig)
		if len(chunks) >= minChunks {
			chunks = RunMultipassExtraction(
				ctx, aiResolver,
				params.KbID, params.SearchQuery,
				chunks,
				MultipassExtractionModel(ctx, siteConfig),
			)
		}
	}

	// Build context string with annotations.
	sources, contextText := buildChatSourcesAndContext(chunks)

	// Q2 sufficient-context gate: one fast-tier call judging whether the
	// assembled set as a WHOLE suffices to answer (Google ICLR 2025
	// "sufficient context"). Complements CRAG, which grades each chunk
	// independently — individually-relevant chunks can still be jointly
	// insufficient, the regime where models hallucinate instead of
	// abstaining. On "insufficient" the existing abstain plumbing takes
	// over (abstain notice in the system prompt, Abstain flag for
	// metrics/UI). Skipped when CRAG already decided to abstain, under
	// long-context routing (the wide pool is intentionally unfiltered),
	// and on empty context (rule 13 already covers that). Fail-open
	// inside JudgeContextSufficiency: judge errors never block answers.
	if !abstain && !longContextRoute && len(chunks) > 0 && ChatSufficientContextEnabled(ctx, siteConfig) {
		model := ResolveFastTierModel(ctx, siteConfig, "chat_sufficient_context_model")
		if !ai.JudgeContextSufficiency(ctx, aiResolver, params.KbID, params.SearchQuery, contextText, params.Language, model) {
			abstain = true
			logctx.From(ctx).Info("rag.sufficient_context.abstain",
				"chunks_in_context", len(chunks), "kb_id", params.KbID)
		}
	}

	// Enumeration pre-pass. When the query asks for an exhaustive list
	// ("In welchen Projekten…", "Wer arbeitet an…", "List all X that…")
	// we run a structured-extraction LLM call over the already-assembled
	// context and inject its verified match list into the prose pass as
	// a hard completeness contract. Without this the mid-range self-
	// hosted LLM reliably drops entries on longer lists — documented in
	// Chroma 2025's "Context Rot" study and reproduced on this KB with
	// 9 project matches → 6 cited in prose. Skipped for abstain paths
	// because there are no matches to enumerate then.
	var enumerationMatches []ai.EnumerationMatch
	// The recency listing supersedes the enumeration pre-pass: both are
	// completeness contracts, but the extraction pass can only verify
	// items already in context, which is exactly what recency queries
	// can't rely on. Two competing "authoritative list" addenda would
	// also conflict in the prompt.
	runEnumeration := !abstain && !recency.fired && IsEnumerationQuery(params.SearchQuery, params.Language)
	if params.ForceEnumerationPrepass != nil {
		runEnumeration = !abstain && *params.ForceEnumerationPrepass
	}
	if runEnumeration {
		// Map BM25-top file IDs (from the retrieval stage) to their
		// 1-based source_idx in the final chunk order. These become the
		// "pre-seeded candidates" hint the extraction prompt uses — BM25
		// anchors the LLM on exact-match evidence so under-recall
		// (observed failure mode on Gemma-class models: the LLM quietly
		// drops 1-2 borderline chunks) is much less likely.
		var preSeeded []int
		seedMeta := make(map[int]ai.SeedMeta)
		if len(result.KeywordMatchFileIDs) > 0 {
			wanted := make(map[string]bool, len(result.KeywordMatchFileIDs))
			for _, fid := range result.KeywordMatchFileIDs {
				wanted[fid] = true
			}
			// First source per keyword file; duplicates collapse to one
			// hint entry per file so the list stays short and focused.
			seen := make(map[string]bool, len(wanted))
			for i, c := range chunks {
				if wanted[c.FileID] && !seen[c.FileID] {
					srcIdx := i + 1
					preSeeded = append(preSeeded, srcIdx)
					seedMeta[srcIdx] = ai.SeedMeta{
						FileName: c.FileName,
						Content:  c.Content,
					}
					seen[c.FileID] = true
				}
			}
		}

		matches, _ := ai.ExtractEnumerationMatches(
			ctx, aiResolver,
			params.SearchQuery, contextText,
			params.KbID, params.Language,
			"", // modelOverride — use KB's chat model
			preSeeded,
			seedMeta,
		)
		enumerationMatches = matches
		logctx.From(ctx).Info("rag.enumeration.decision",
			"enumeration", true,
			"matches_extracted", len(matches),
			"chunks_in_context", len(chunks),
			"pre_seeded_candidates", len(preSeeded),
		)
	}

	// Build system prompt.
	var sb strings.Builder
	if params.KbSystemPrompt != "" {
		sb.WriteString(params.KbSystemPrompt)
		sb.WriteString("\n\n")
	}
	sb.WriteString(prompts.ChatSystemPromptWithDate(params.Language, params.CurrentDateLine))
	switch {
	case abstain:
		sb.WriteString(prompts.ChatAbstainNotice(params.Language))
	case IsLowConfidence(chunks):
		sb.WriteString(prompts.ChatLowConfidenceNotice(params.Language))
	}
	if runEnumeration {
		// Inject the verified-matches addendum even when the list is empty —
		// the prose LLM should then tell the user "no matches" rather than
		// improvising from the raw chunks.
		matchesJSON := ai.FormatEnumerationMatchesJSON(enumerationMatches)
		sb.WriteString(prompts.EnumerationVerifiedMatchesAddendum(params.Language, matchesJSON))
	}
	if recency.fired {
		// Injected even when empty: the model should answer "nothing new
		// was added since <date>" instead of presenting older context
		// content as new.
		sb.WriteString(prompts.RecencyListingAddendum(params.Language, recency.entries, recency.sinceISO, recency.truncated))
	}
	sb.WriteString("\n\nCONTEXT:\n")
	sb.WriteString(contextText)

	return &ChatContext{
		EnhancedQuery: result.EnhancedQuery,
		SystemPrompt:  sb.String(),
		Sources:       sources,
		Context:       contextText,
		FinalChunks:   chunks,
		Abstain:       abstain,
	}, nil
}

// ---------------------------------------------------------------------------
// CondenseFollowUp
// ---------------------------------------------------------------------------

// CondenseFollowUp turns a follow-up question into a standalone search query
// by incorporating the recent conversation history. If the history has fewer
// than 2 messages, or if the AI call fails, the original message is returned
// unchanged (fail-open behaviour).
func CondenseFollowUp(
	ctx context.Context,
	aiResolver *ai.ConfigResolver,
	store Store,
	chatID string,
	parentMessageID *string,
	message, kbID, language string,
) (string, error) {
	var messages []MessageRow
	var err error

	if parentMessageID != nil && *parentMessageID != "" {
		messages, err = store.GetMessageAncestors(ctx, *parentMessageID, chatID)
	} else {
		messages, err = store.GetChatMessages(ctx, chatID)
	}
	if err != nil {
		// Fail open: return original message if we can't load history.
		// Log so a real DB outage is visible — LLM errors are silenced
		// later (expected fallback path), but a store failure here
		// silently degrades follow-up retrieval and should surface in
		// observability.
		logctx.From(ctx).Warn("chat.condense: load history failed",
			"chat_id", chatID, "error", err)
		return message, nil
	}

	if len(messages) < 2 {
		return message, nil
	}

	// Take last 6 messages, truncate content to 500 chars each.
	if len(messages) > 6 {
		messages = messages[len(messages)-6:]
	}

	history := make([]ai.ChatHistoryEntry, len(messages))
	for i, m := range messages {
		content := m.Content
		if len(content) > 500 {
			content = content[:500]
		}
		history[i] = ai.ChatHistoryEntry{
			Role:    m.Role,
			Content: content,
		}
	}

	condensed, err := ai.CondenseQuestion(ctx, aiResolver, history, message, kbID, language)
	if err != nil {
		// Fail open.
		return message, nil
	}
	return condensed, nil
}
