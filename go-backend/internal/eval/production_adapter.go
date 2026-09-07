package eval

import (
	"cmp"
	"context"
	"slices"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/chat"
	"github.com/justrag/go-backend/internal/vector"
)

// ProductionContextAdapter invokes the full chat.PrepareChatContext pipeline
// (CRAG, enumeration pre-pass, contextual prefix, sandwich order,
// abstain/low-confidence notices) so eval measures the same code path that
// serves users. Used by cmd/eval (--production-context) and the in-app eval
// runner's worker.
type ProductionContextAdapter struct {
	aiResolver       *ai.ConfigResolver
	searchService    vector.Searcher
	siteConfigReader chat.SiteConfigReader
	flags            EvalFlags

	// tabularRouter is the deterministic spreadsheet path. Optional —
	// nil (the default) reproduces the pre-router pipeline exactly, which
	// is what a retrieval-only ablation wants.
	tabularRouter *chat.TabularRouter

	// recencyLister backs the deterministic recency-listing path (Wave 2
	// Task 8: dated CERT fixture). Optional — nil (the default) reproduces
	// the pre-Task-8 pipeline exactly: no window-scoped retrieval, no
	// listing addendum. (CurrentDateLine itself is set unconditionally by
	// buildParams regardless of this option — see WithRecencyLister.) This
	// is what a retrieval-only ablation wants.
	recencyLister chat.RecencyLister

	// fileDates resolves the cited files' published_at/created_at for the
	// W5-R7 conflict pass's date lines. Optional — nil (the default) makes
	// every date line render "unknown" and the detector is told not to
	// guess a direction, so `newer` degenerates to "unknown" and a
	// supersession measurement is impossible. cmd/eval wires the real
	// lookup (files.PGStore) exactly as internal/app does for production.
	fileDates chat.FileDateLookup

	// cache stores the final ChatContext per question so judge-mode can
	// reuse the assembled system prompt and context text without a second
	// retrieval pass.
	cache map[string]*chat.ChatContext
}

// ProductionAdapterOption configures optional dependencies that only some
// eval entrypoints can supply (cmd/eval has the pools; the in-app runner
// does not necessarily).
type ProductionAdapterOption func(*ProductionContextAdapter)

// WithTabularRouter attaches the deterministic tabular router so a
// --production-context run exercises the same spreadsheet path production
// serves (quoted id phrases, forced simple BM25 arm, SQL-result addendum).
func WithTabularRouter(r *chat.TabularRouter) ProductionAdapterOption {
	return func(a *ProductionContextAdapter) { a.tabularRouter = r }
}

// WithRecencyLister attaches the deterministic recency-listing lookup
// (recencylister.New in production wiring / cmd/eval) so a
// --production-context run exercises the same "what is new / recently
// added" path production serves: window-scoped retrieval plus a complete
// file-listing system-prompt addendum for recency-listing queries. Without
// this option, a --production-context run reproduces the pre-Task-8
// pipeline exactly (no window scoping, no listing addendum) — the
// recency-listing mechanism stays entirely opt-in for eval. CurrentDateLine
// is unaffected by this option: buildParams sets it unconditionally (via
// chat.SystemPromptDateLine) on every --production-context run, with or
// without a recencyLister, mirroring chat_date_awareness_enabled's
// default-on behaviour in production.
func WithRecencyLister(l chat.RecencyLister) ProductionAdapterOption {
	return func(a *ProductionContextAdapter) { a.recencyLister = l }
}

// WithFileDates attaches the per-turn file-date lookup that production wires
// through chat.WithFileDates. It is what lets the conflict / supersession
// pass (W5-R7, chat_conflict_surfacing_enabled) decide DIRECTION: without it
// every source's date line renders "unknown" and the detector is instructed
// not to guess, so a supersession pair can never be reported with `newer`
// set. Only meaningful under --production-context with the flag on;
// otherwise it is inert (PrepareChatContext ignores FileDates when the
// conflict pass is gated off).
func WithFileDates(l chat.FileDateLookup) ProductionAdapterOption {
	return func(a *ProductionContextAdapter) { a.fileDates = l }
}

// EvalFlags carries the flag values through the adapter without a global.
type EvalFlags struct {
	Enhance    string
	HyDE       bool
	MultiQuery bool
	// StepBack force-enables step-back retrieval for this eval run regardless
	// of the live `step_back_enabled` site_config — the production adapter
	// passes it into ChatContextParams.StepBack which is OR-combined with the
	// site_config inside PrepareChatContext.
	StepBack                bool
	ForceEnumerationPrepass *bool
	// GoldenQueryType forwards the golden row's curated `query_type` label
	// into chat.ChatContextParams.QueryType instead of leaving the
	// classification to the pipeline (cmd/eval --golden-query-type).
	// Default false: every pre-existing --production-context run keeps
	// classifying the query itself, so report shapes stay comparable.
	// Only the retrieval-layer routing keys on this — the eval
	// orchestrator ladder still classifies independently
	// (ClassifyQueryTypeForEval), which is deliberate: a golden set whose
	// questions do not trip the production classifier is a question-authoring
	// problem, not something a label should paper over.
	GoldenQueryType bool
}

// NewProductionContextAdapter constructs a ProductionContextAdapter with the
// given dependencies. All fields are required (siteConfigReader may be wrapped
// to override CRAG config).
func NewProductionContextAdapter(
	aiResolver *ai.ConfigResolver,
	searchService vector.Searcher,
	siteConfigReader chat.SiteConfigReader,
	flags EvalFlags,
	opts ...ProductionAdapterOption,
) *ProductionContextAdapter {
	a := &ProductionContextAdapter{
		aiResolver:       aiResolver,
		searchService:    searchService,
		siteConfigReader: siteConfigReader,
		flags:            flags,
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

// Search runs chat.PrepareChatContext for the question and caches the
// resulting ChatContext so judge-mode can reuse it via ChatContextForQuestion.
// Equivalent to SearchWithQuery(ctx, q, k, q.Question, "") — no query
// condensation, no rewrite⊕raw lane.
func (a *ProductionContextAdapter) Search(ctx context.Context, q Question, k int) ([]RetrievedChunk, error) {
	return a.SearchWithQuery(ctx, q, k, q.Question, "")
}

// buildParams assembles the chat.ChatContextParams for one question. Split
// out from SearchWithQuery as a seam so a unit test can assert the
// RecencyLister / CurrentDateLine wiring without running retrieval (Wave 2
// Task 8): dropping either assignment here is a silent eval/production
// divergence — the recency-listing fixture's headline mechanism (window
// scoping + the file-listing addendum) never fires under cmd/eval, but the
// question doesn't error, so nothing else in the pipeline notices.
func (a *ProductionContextAdapter) buildParams(ctx context.Context, q Question, searchQuery, rawQuery string) chat.ChatContextParams {
	// Wave 3 Task 4: the golden label reaches the pipeline only behind
	// EvalFlags.GoldenQueryType (cmd/eval --golden-query-type). An
	// unlabelled row stays empty either way — the flag forwards a curator's
	// label, it never invents one.
	queryType := ""
	if a.flags.GoldenQueryType {
		queryType = q.QueryType
	}
	return chat.ChatContextParams{
		QueryType:               queryType,
		KbID:                    q.KbID,
		SearchQuery:             searchQuery,
		RawQuery:                rawQuery,
		Language:                q.Language,
		Enhance:                 a.flags.Enhance,
		HyDE:                    a.flags.HyDE,
		MultiQuery:              a.flags.MultiQuery,
		StepBack:                a.flags.StepBack,
		ForceEnumerationPrepass: a.flags.ForceEnumerationPrepass,
		TabularRouter:           a.tabularRouter,
		RecencyLister:           a.recencyLister,
		FileDates:               a.fileDates,
		CurrentDateLine:         chat.SystemPromptDateLine(ctx, a.siteConfigReader, q.Language),
	}
}

// SearchWithQuery is Search with the retrieval query and the optional raw
// (rewrite⊕raw lane) query supplied by the caller instead of derived from
// q.Question. MultiTurnAdapter (Wave 2 Task 3) is the caller that needs
// this: a follow-up turn's SEARCH query is the condensed standalone
// question, not the verbatim golden-set turn text, and RawQuery carries the
// verbatim utterance alongside it when chat_condense_keep_raw_enabled is on.
func (a *ProductionContextAdapter) SearchWithQuery(ctx context.Context, q Question, k int, searchQuery, rawQuery string) ([]RetrievedChunk, error) {
	params := a.buildParams(ctx, q, searchQuery, rawQuery)
	chatCtx, err := chat.PrepareChatContext(ctx, a.aiResolver, a.searchService, a.siteConfigReader, params)
	if err != nil {
		return nil, err
	}
	if a.cache == nil {
		a.cache = make(map[string]*chat.ChatContext)
	}
	a.cache[q.ID] = chatCtx

	// Project FinalChunks to the evaluator's view. FinalChunks is in
	// SANDWICH order — highest-scored chunks at positions 0 and N-1, next at
	// 1 and N-2, etc. — so the LLM's lost-in-the-middle attention pattern is
	// mitigated. That layout is wrong for retrieval-quality metrics (recall,
	// precision, MRR, nDCG) which all need top-k by score: a chunks[:k] cut
	// against sandwich order silently drops every other top-ranked chunk
	// (rank-2/4/6/8/10 land at sandwich positions 14/13/12/11/10, all of
	// which fall outside k=10 when len=15). Re-sort by score descending
	// here, so the eval measures retrieval quality, not LLM-context layout.
	// The cached ChatContext (used by ContentsForQuestion → judge mode)
	// remains in sandwich order — that's the right layout for grading the
	// LLM's actual answer.
	chunks := append([]vector.SearchChunk(nil), chatCtx.FinalChunks...)
	slices.SortStableFunc(chunks, func(a, b vector.SearchChunk) int {
		return cmp.Compare(b.Score, a.Score)
	})
	if k > 0 && k < len(chunks) {
		chunks = chunks[:k]
	}
	out := make([]RetrievedChunk, 0, len(chunks))
	for _, c := range chunks {
		idx, total := ChunkPositionFromMetadata(c.Metadata)
		out = append(out, RetrievedChunk{
			FileID:      c.FileID,
			FileName:    c.FileName,
			Score:       c.Score,
			ChunkIndex:  idx,
			TotalChunks: total,
		})
	}
	return out, nil
}

// AgentTraceForQuestion satisfies the agentTracer interface RunEval
// detects via type assertion. ProductionContextAdapter doesn't dispatch
// through an orchestrator, so every field except Tabular stays zero — this
// exists solely so a --production-context run without
// --orchestrator-dispatch still surfaces the tabular router's decision
// (chatCtx.TabularTrace, set by PrepareChatContext when a.tabularRouter is
// non-nil) in the report. Returns nil when no ChatContext was cached for
// questionID (search errored) or the cached ChatContext carries no tabular
// trace (no router wired, or the router didn't fire) — either way
// QuestionReport.Agent stays nil, keeping legacy report shapes byte-stable
// for runs without the router attached.
func (a *ProductionContextAdapter) AgentTraceForQuestion(questionID string) *AgentTrace {
	if a.cache == nil {
		return nil
	}
	c, ok := a.cache[questionID]
	if !ok || c == nil {
		return nil
	}
	tab := TabularEvalTraceFrom(c.TabularTrace)
	if tab == nil {
		return nil
	}
	return &AgentTrace{Tabular: tab}
}

// ConflictsForQuestion satisfies the conflictTracer interface RunEval detects
// by type assertion. Returns the W5-R7 conflict report the chat pipeline
// attached to this question's ChatContext, in the same bare-array shape a
// chat turn persists and streams (chat.ConflictsForWire), or nil when the
// pass did not run or found nothing.
func (a *ProductionContextAdapter) ConflictsForQuestion(questionID string) []chat.MessageConflict {
	c, ok := a.ChatContextForQuestion(questionID)
	if !ok || c == nil {
		return nil
	}
	return chat.ConflictsForWire(c.Conflicts)
}

// ChatContextForQuestion returns the cached ChatContext for judge-mode answer
// generation. Returns (nil, false) if no retrieval was run for questionID
// (e.g. it errored).
func (a *ProductionContextAdapter) ChatContextForQuestion(questionID string) (*chat.ChatContext, bool) {
	if a.cache == nil {
		return nil, false
	}
	c, ok := a.cache[questionID]
	return c, ok
}

// ContentsForQuestion mirrors the legacy adapter's method so judge-mode code
// can call it uniformly. Returns the chunk content and file-name slices from
// the cached ChatContext, trimmed to at most k entries.
func (a *ProductionContextAdapter) ContentsForQuestion(questionID string, k int) (contents []string, fileNames []string, ok bool) {
	c, found := a.ChatContextForQuestion(questionID)
	if !found {
		return nil, nil, false
	}
	chunks := c.FinalChunks
	if k > 0 && k < len(chunks) {
		chunks = chunks[:k]
	}
	contents = make([]string, len(chunks))
	fileNames = make([]string, len(chunks))
	for i, ch := range chunks {
		contents[i] = ch.Content
		fileNames[i] = ch.FileName
	}
	return contents, fileNames, true
}
