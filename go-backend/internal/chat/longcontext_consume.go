package chat

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/prompts"
	"github.com/justrag/go-backend/internal/promptsafety"
	"github.com/justrag/go-backend/internal/safego"
	"github.com/justrag/go-backend/internal/splitter"
	"github.com/justrag/go-backend/internal/vector"
)

// Long-context consumer modes (site_config `chat_longcontext_mode`).
const (
	// LongContextModeFlat hands the whole (token-budgeted) chunk pool to the
	// answer LLM raw — byte-identical to the pre-Wave-3 behaviour.
	LongContextModeFlat = "flat"
	// LongContextModeMapReduce extracts per-group findings first and hands the
	// answer LLM the findings instead of the raw bodies (W3-R6).
	LongContextModeMapReduce = "map_reduce"
)

// longContextMapTimeout bounds one map-stage extraction call. A group that
// exceeds it degrades to its raw-chunk fallback findings rather than stalling
// the turn — the whole route already runs at ~30x normal cost, so an unbounded
// tail latency here is the difference between "slow" and "abandoned".
const longContextMapTimeout = 45 * time.Second

// longContextMapStageTimeout bounds the WHOLE map stage. The per-group
// timeout above is not an overall bound: a 200-chunk pool at the default
// group size is ~25 groups, and at concurrency 6 that is five sequential
// waves, so the worst case is ~5 × 45 s ≈ 225 s of a synchronous chat turn
// before a single token streams. 180 s is the ceiling on that: groups the
// stage deadline cuts off degrade to their raw-chunk fallback findings like
// any other per-group failure, so a slow backend costs evidence quality, not
// the answer. Deliberately a constant, not a site_config key — it is a
// latency guardrail on an already-opt-in route, and the two knobs that
// actually shape the cost (group size, concurrency) are operator-tunable.
//
// A package var rather than a const solely so the test that exercises the
// stage deadline can shorten it; nothing at runtime writes it.
var longContextMapStageTimeout = 180 * time.Second

// longContextFallbackRunes is how much of a chunk's raw body becomes its
// fallback "finding" when its group's extraction failed (W3-R7).
const longContextFallbackRunes = 600

// LongContextParams is the resolved input to the long-context orchestrator and
// its consumer. Everything is pre-resolved so consumeLongContextWith needs no
// site_config reader (and the tests need no fake one).
type LongContextParams struct {
	KbID            string
	Query           string
	Language        string
	KbSystemPrompt  string
	CurrentDateLine string
	FileIDs         []string
	// RawQuery is the verbatim user utterance forwarded to the retrieval
	// layer's extra RRF lane (chat_condense_keep_raw_enabled); empty when the
	// turn was not condensed.
	RawQuery string
	// GraphChunkIDs, BridgeChunks and HyPESearch are the same retrieval
	// signals every other orchestrator threads into SearchOptions (see
	// DriftChatParams): the AP-C4 graph router's resolved chunk ids, the
	// bridge-evidence tally, and the HyPE question-embedding lane. Omitting
	// them would silently disable graph routing and HyPE for exactly the
	// turns this route serves.
	GraphChunkIDs []string
	BridgeChunks  map[string]int
	HyPESearch    bool

	// Mode is LongContextModeFlat or LongContextModeMapReduce. Anything else
	// normalises to flat.
	Mode string
	// MaxTokens is the pool's token budget (chat_longcontext_max_tokens).
	MaxTokens int
	// TopK is the wide-retrieval pool size (chat_longcontext_top_k).
	TopK int
	// GroupSize / Concurrency configure the map stage only.
	GroupSize   int
	Concurrency int
	// MapModel is the resolved fast-tier model for the map stage.
	MapModel string

	Emit func(map[string]any)
}

// flatAddenda carries the system-prompt blocks that only PrepareChatContext
// can compute (they depend on retrieval state the orchestrator path never
// builds). The orchestrator passes the zero value; service.go passes the
// strings its own pre-passes produced, so both paths share ONE prompt-tail
// implementation and cannot drift.
type flatAddenda struct {
	// Abstain selects the abstain notice over the low-confidence notice.
	Abstain bool
	// Enumeration is the verified-matches addendum (already prompt-formatted,
	// leading newlines included), empty when the pre-pass did not run.
	Enumeration string
	// Recency is the recency-listing addendum, empty when it did not fire.
	Recency string
	// Tabular is the executed-rows addendum. Unlike the two above it carries
	// no leading blank line of its own, so the assembler adds one.
	Tabular string
	// Conflicts is the W5-R7 conflict / supersession addendum, empty when
	// the pass is off, found nothing or failed. Carries its own leading
	// blank line (like Enumeration and Recency).
	Conflicts string
}

// extractFindingsFn is the injectable seam for ai.ExtractLongContextFindings.
type extractFindingsFn func(ctx context.Context, resolver *ai.ConfigResolver, question, groupText, kbID, lang, model string) ([]ai.LongContextFinding, error)

// consumeLongContext turns a wide chunk pool into a *ChatContext.
//
// cfg fills in any LongContextParams field the caller left at its zero value,
// so a call site that only knows the KB and the query still gets the operator's
// configured mode / group size / concurrency / model.
func consumeLongContext(
	ctx context.Context,
	resolver *ai.ConfigResolver,
	cfg SiteConfigReader,
	p LongContextParams,
	chunks []vector.SearchChunk,
) (*ChatContext, error) {
	return consumeLongContextWith(ctx, resolver, ai.ExtractLongContextFindings, resolveLongContextParams(ctx, cfg, p), chunks)
}

// resolveLongContextParams fills the zero-valued knobs from the site_config
// reader. A nil reader leaves the compiled-in defaults (readBool/readInt do
// their own nil-reader handling, so this is safe on the eval path too).
func resolveLongContextParams(ctx context.Context, cfg SiteConfigReader, p LongContextParams) LongContextParams {
	if p.Mode == "" {
		p.Mode = ChatLongContextMode(ctx, cfg)
	}
	if p.MaxTokens <= 0 {
		p.MaxTokens = ChatLongContextMaxTokens(ctx, cfg)
	}
	if p.TopK <= 0 {
		p.TopK = ChatLongContextTopK(ctx, cfg)
	}
	if p.GroupSize <= 0 {
		p.GroupSize = ChatLongContextMapGroupSize(ctx, cfg)
	}
	if p.Concurrency <= 0 {
		p.Concurrency = ChatLongContextMapConcurrency(ctx, cfg)
	}
	if p.MapModel == "" {
		p.MapModel = ChatLongContextMapModel(ctx, cfg)
	}
	return p
}

// consumeLongContextWith is the testable core. extract may be nil in flat mode
// (it is never called there).
func consumeLongContextWith(
	ctx context.Context,
	resolver *ai.ConfigResolver,
	extract extractFindingsFn,
	p LongContextParams,
	chunks []vector.SearchChunk,
) (*ChatContext, error) {
	if len(chunks) == 0 {
		// Mirrors RunDriftChat's empty-pool contract: return an error so the
		// dispatcher falls through to the standard path instead of streaming
		// an answer with no evidence at all.
		return nil, fmt.Errorf("longcontext: no results")
	}

	if p.Mode != LongContextModeMapReduce {
		pool := TruncateChunksToFit(chunks, p.MaxTokens)
		pool = SandwichOrder(pool)
		return assembleFlatLongContext(pool, p, flatAddenda{}), nil
	}

	// Map-reduce. Score order is preserved through grouping (no sandwich
	// reorder before the map stage) so group i covers pool[i*size : …] and the
	// `[N]` numbering the extractor sees is the pool numbering.
	pool := TruncateChunksToFit(chunks, p.MaxTokens)
	sources, _ := buildChatSourcesAndContext(pool)
	groups := groupChunks(pool, p.GroupSize)

	type groupResult struct {
		findings []ai.LongContextFinding
		err      error
	}
	results := make([]groupResult, len(groups))

	concurrency := p.Concurrency
	if concurrency < 1 {
		concurrency = 1
	}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	// One deadline for the entire fan-out; each group's own 45 s budget is
	// derived from it, so the stage can never outlive this even across
	// several sequential waves of groups.
	stageCtx, stageCancel := context.WithTimeout(ctx, longContextMapStageTimeout)
	defer stageCancel()

	for i := range groups {
		wg.Add(1)
		// Plain goroutine + safego.RecoverError, NOT safego.GoCtx — the
		// multipass.go convention. A panic inside the extraction call (an
		// HTTP/JSON shape surprise from a third-party endpoint) must become
		// results[i].err so the fallback below turns the group into raw-chunk
		// findings; GoCtx would only log it and leave the slot zero-valued,
		// silently dropping that group's evidence. RecoverError is registered
		// after wg.Done() so it runs FIRST on unwind and its write to
		// results[i].err happens-before wg.Wait() observes the counter.
		go func() {
			defer wg.Done()
			defer safego.RecoverError(&results[i].err)

			// Every wait keys on stageCtx, not ctx: a group still queued when
			// the stage deadline passes must abandon immediately and take the
			// fallback path rather than start a fresh 45 s call.
			if stageCtx.Err() != nil {
				results[i].err = stageCtx.Err()
				return
			}
			select {
			case sem <- struct{}{}:
			case <-stageCtx.Done():
				results[i].err = stageCtx.Err()
				return
			}
			defer func() { <-sem }()

			gctx, cancel := context.WithTimeout(stageCtx, longContextMapTimeout)
			defer cancel()

			offset := i * groupSizeOrDefault(p.GroupSize)
			f, err := extract(gctx, resolver, p.Query, renderChunkGroup(groups[i], offset), p.KbID, p.Language, p.MapModel)
			results[i].findings, results[i].err = f, err
		}()
	}
	wg.Wait()

	var all []ai.LongContextFinding
	failedGroups := 0
	for i, res := range results {
		offset := i * groupSizeOrDefault(p.GroupSize)
		var kept []ai.LongContextFinding
		if res.err != nil {
			failedGroups++
			logctx.From(ctx).Warn("longcontext.map_group_failed",
				"group", i+1, "chunks", len(groups[i]), "error", res.err)
			kept = fallbackFindings(groups[i], offset)
		} else {
			for _, f := range res.findings {
				// Drop hallucinated source numbers: the answer must not carry
				// a `[N]` the Sources list cannot resolve.
				if f.SourceIdx < 1 || f.SourceIdx > len(pool) {
					continue
				}
				kept = append(kept, f)
			}
		}
		all = append(all, kept...)
		emitTrajectory(p.Emit, TrajectoryEvent{
			Stage:    "longcontext_map",
			Step:     i + 1,
			Findings: len(kept),
			Chunks:   chunkRefs(groups[i]),
		}, nil)
	}

	if len(all) == 0 {
		// Every group came back empty (and none errored, or the fallback
		// produced nothing). Handing the answer LLM an empty FINDINGS block
		// would be strictly worse than the raw pool, so degrade to flat.
		logctx.From(ctx).Warn("longcontext.map_reduce_empty_findings", "groups", len(groups))
		observability.RecordLongContextRoute("map_empty", LongContextModeMapReduce)
		pool = SandwichOrder(pool)
		return assembleFlatLongContext(pool, p, flatAddenda{}), nil
	}

	// The findings COUNT is unbounded by construction (a 500-chunk pool at
	// group size 2 is 250 groups, each free to return several findings), so
	// the reduce prompt gets the same token budget the chunk pool was
	// truncated against. Dropping happens from the tail, in source order.
	all, droppedFindings := truncateFindingsToFit(all, pool, p.Language, p.MaxTokens)
	if droppedFindings > 0 {
		logctx.From(ctx).Info("longcontext.reduce_findings_truncated",
			"kept", len(all), "dropped", droppedFindings, "max_tokens", p.MaxTokens)
	}

	emitTrajectory(p.Emit, TrajectoryEvent{
		Stage:    "longcontext_reduce",
		Findings: len(all),
		Dropped:  droppedFindings,
	}, nil)
	logctx.From(ctx).Info("rag.longcontext.map_reduce",
		"groups", len(groups), "failed_groups", failedGroups,
		"findings", len(all), "dropped_findings", droppedFindings, "pool", len(pool))

	findingsText := renderFindingsContext(all, pool, p.Language)

	var sb strings.Builder
	if p.KbSystemPrompt != "" {
		sb.WriteString(p.KbSystemPrompt)
		sb.WriteString("\n\n")
	}
	sb.WriteString(prompts.ChatSystemPromptWithDate(p.Language, p.CurrentDateLine))
	sb.WriteString(prompts.LongContextSynthesisSystem(p.Language))
	sb.WriteString("\n\nCONTEXT:\n")
	sb.WriteString(findingsText)

	return &ChatContext{
		SystemPrompt: sb.String(),
		Sources:      sources,
		Context:      findingsText,
		FinalChunks:  pool,
	}, nil
}

// groupSizeOrDefault mirrors groupChunks' normalisation so the pool offset a
// group maps to is computed from the SAME size groupChunks actually used.
func groupSizeOrDefault(size int) int {
	if size < 1 {
		return defaultLongContextGroupSize
	}
	return size
}

// defaultLongContextGroupSize is the fallback when a caller passes a
// non-positive group size. Matches chat_longcontext_map_group_size's default.
const defaultLongContextGroupSize = 8

// groupChunks slices chunks into consecutive groups of at most size, keeping
// order so group i covers chunks[i*size : (i+1)*size] and a chunk's pool index
// (hence its `[N]`) is recoverable from its group position. The last group is
// short when the count is not a multiple of size. Returns nil for no chunks.
func groupChunks(chunks []vector.SearchChunk, size int) [][]vector.SearchChunk {
	if len(chunks) == 0 {
		return nil
	}
	size = groupSizeOrDefault(size)
	out := make([][]vector.SearchChunk, 0, (len(chunks)+size-1)/size)
	for start := 0; start < len(chunks); start += size {
		end := min(start+size, len(chunks))
		out = append(out, chunks[start:end])
	}
	return out
}

// renderChunkGroup renders one group with its ORIGINAL pool numbering, so the
// extractor's source_idx values are pool indices and need no remapping.
// offset is the group's first chunk's 0-based pool index.
func renderChunkGroup(group []vector.SearchChunk, offset int) string {
	parts := make([]string, 0, len(group))
	for i, c := range group {
		annotation, _ := renderChunkAnnotation(offset+i+1, c)
		parts = append(parts, annotation+"\n"+c.Content)
	}
	return strings.Join(parts, chunkBlockSeparator)
}

// fallbackFindings turns a failed group's chunks into raw-body findings so its
// evidence still reaches the answer LLM (W3-R7).
func fallbackFindings(group []vector.SearchChunk, offset int) []ai.LongContextFinding {
	out := make([]ai.LongContextFinding, 0, len(group))
	for i, c := range group {
		body := strings.TrimSpace(c.Content)
		if body == "" {
			continue
		}
		out = append(out, ai.LongContextFinding{
			SourceIdx: offset + i + 1,
			Claim:     firstRunes(body, longContextFallbackRunes),
		})
	}
	return out
}

func firstRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// findingsFenceOpen / findingsFenceClose delimit the findings list in the
// prompt. Both the claims and the quotes are model output derived from
// retrieved document text — an injection carrier, exactly like a spreadsheet
// cell in the tabular addendum — so they are fenced as data rather than left
// loose in the system prompt, and the synthesis instruction names the fence.
const (
	findingsFenceOpen  = "```FINDINGS"
	findingsFenceClose = "```"
)

// fenceRunRe matches a run of three or more backticks, which a claim or quote
// could otherwise use to close the fence early and continue as prose the model
// reads as instructions. Mirrors internal/prompts' fenceSafe.
var fenceRunRe = regexp.MustCompile("`{3,}")

// findingTextFilteredDE / EN replace a claim or quote that trips the
// instruction heuristic. The finding is dropped rather than rendered, but its
// `[N]` line stays so the numbering and the source attribution survive.
const (
	findingTextFilteredDE = "[gefiltert]"
	findingTextFilteredEN = "[filtered]"
)

// sanitizeFindingText makes one model-authored string safe to place inside the
// fenced findings block: newlines collapse (a finding is one line, and an
// embedded newline could fake a fresh line outside the data), backtick runs are
// neutralised so the fence cannot be closed early, and text matching the
// instruction heuristic is replaced wholesale.
func sanitizeFindingText(s, lang string) string {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return ""
	}
	if promptsafety.LooksLikeInstruction(s) {
		if lang == "de" {
			return findingTextFilteredDE
		}
		return findingTextFilteredEN
	}
	return fenceRunRe.ReplaceAllString(s, "‵‵‵")
}

// findingLineOverheadTokens is the per-finding cost of the rendering itself:
// the `[N] ` prefix, the „…“ quote wrapper and the trailing newline. Small and
// deliberately generous — the budget check is a guardrail, not an accountant.
const findingLineOverheadTokens = 6

// truncateFindingsToFit bounds the reduce-stage CONTEXT against maxTokens (the
// same chat_longcontext_max_tokens the chunk pool was truncated against), using
// the same estimator TruncateChunksToFit uses. Findings stay in source order
// and are dropped from the TAIL: earlier groups cover the highest-scoring
// chunks, so a tail drop sheds the weakest evidence first.
//
// The SOURCES block (one header line per pool chunk) is fixed cost and is
// reserved up front. At least one finding is always kept — an empty findings
// block would be strictly worse than a short one, and the "no findings at all"
// case is already handled earlier by the flat degrade path.
//
// Returns the kept findings and how many were dropped.
func truncateFindingsToFit(findings []ai.LongContextFinding, pool []vector.SearchChunk, lang string, maxTokens int) ([]ai.LongContextFinding, int) {
	if len(findings) == 0 || maxTokens <= 0 {
		return findings, 0
	}
	budget := maxTokens - splitter.CountTokens(renderFindingsContext(nil, pool, lang))
	used := 0
	kept := 0
	for _, f := range findings {
		cost := splitter.CountTokens(f.Claim) + splitter.CountTokens(f.Quote) + findingLineOverheadTokens
		if kept > 0 && used+cost > budget {
			break
		}
		used += cost
		kept++
	}
	return findings[:kept], len(findings) - kept
}

// renderFindingsContext builds the reduce-stage CONTEXT block: the findings
// grouped by source in ascending `[N]` order inside a data fence, then the
// original source headers (one line each, no chunk bodies, W3-R6) so the
// answer LLM can attribute every citation.
func renderFindingsContext(findings []ai.LongContextFinding, pool []vector.SearchChunk, lang string) string {
	byIdx := map[int][]ai.LongContextFinding{}
	for _, f := range findings {
		byIdx[f.SourceIdx] = append(byIdx[f.SourceIdx], f)
	}
	idxs := make([]int, 0, len(byIdx))
	for idx := range byIdx {
		idxs = append(idxs, idx)
	}
	sort.Ints(idxs)

	var sb strings.Builder
	sb.WriteString("FINDINGS (grouped by source):\n")
	sb.WriteString(findingsFenceOpen)
	sb.WriteString("\n")
	for _, idx := range idxs {
		for _, f := range byIdx[idx] {
			sb.WriteString(fmt.Sprintf("[%d] %s", idx, sanitizeFindingText(f.Claim, lang)))
			if q := sanitizeFindingText(f.Quote, lang); q != "" {
				sb.WriteString(fmt.Sprintf(" — „%s“", q))
			}
			sb.WriteString("\n")
		}
	}
	sb.WriteString(findingsFenceClose)
	sb.WriteString("\n\nSOURCES:\n")
	for i, c := range pool {
		header, _ := renderChunkHeaderLine(i+1, c)
		sb.WriteString(header)
		sb.WriteString("\n")
	}
	return sb.String()
}

// assembleFlatLongContext is the ONE implementation of the answer-prompt tail
// that PrepareChatContext has produced since T2-1. The orchestrator path calls
// it with a zero flatAddenda; service.go calls assembleFlatFromParts with the
// addenda its own pre-passes produced (and the sources/context text it already
// built) so neither path can drift from the other.
//
// chunks must already be truncated and sandwich-ordered.
func assembleFlatLongContext(chunks []vector.SearchChunk, p LongContextParams, add flatAddenda) *ChatContext {
	sources, contextText := buildChatSourcesAndContext(chunks)
	return assembleFlatFromParts(chunks, sources, contextText, p, add)
}

// assembleFlatFromParts is assembleFlatLongContext for callers that already
// built the sources / context text (service.go needs them for its
// sufficient-context gate and enumeration pre-pass before the prompt exists).
func assembleFlatFromParts(
	chunks []vector.SearchChunk,
	sources []ChatSource,
	contextText string,
	p LongContextParams,
	add flatAddenda,
) *ChatContext {
	var sb strings.Builder
	if p.KbSystemPrompt != "" {
		sb.WriteString(p.KbSystemPrompt)
		sb.WriteString("\n\n")
	}
	sb.WriteString(prompts.ChatSystemPromptWithDate(p.Language, p.CurrentDateLine))
	switch {
	case add.Abstain:
		sb.WriteString(prompts.ChatAbstainNotice(p.Language))
	case IsLowConfidence(chunks):
		sb.WriteString(prompts.ChatLowConfidenceNotice(p.Language))
	}
	if add.Enumeration != "" {
		sb.WriteString(add.Enumeration)
	}
	if add.Recency != "" {
		sb.WriteString(add.Recency)
	}
	if add.Tabular != "" {
		// Must precede the CONTEXT block: the executed rows are ground truth
		// the answer LLM should prefer over the retrieved prose.
		sb.WriteString("\n\n")
		sb.WriteString(add.Tabular)
	}
	if add.Conflicts != "" {
		// After Tabular, still before CONTEXT: the conflict list refers to
		// the [N] markers of the context blocks below, so it reads as a
		// commentary on them rather than as evidence of its own.
		sb.WriteString(add.Conflicts)
	}
	sb.WriteString("\n\nCONTEXT:\n")
	sb.WriteString(contextText)

	return &ChatContext{
		SystemPrompt: sb.String(),
		Sources:      sources,
		Context:      contextText,
		FinalChunks:  chunks,
		Abstain:      add.Abstain,
	}
}
