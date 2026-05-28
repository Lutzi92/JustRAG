package chat

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/safego"
	"github.com/justrag/go-backend/internal/vector"
)

// multipassConcurrency caps the in-flight per-chunk extraction calls.
// Higher values cut wall-clock time but burst the LLM endpoint; 5 was
// picked to stay well under typical per-KB connection pool sizes
// (often 10-20 against vLLM/LiteLLM) while still cutting a 15-chunk
// pre-pass to 3 sequential batches instead of 15.
const multipassConcurrency = 5

// multipassNoneSentinel is the exact string the extraction prompt
// instructs the LLM to emit when a passage has no facts relevant to
// the query. Comparison is case-insensitive after Trim so " None " /
// "none." also count — Gemma-class models sometimes punctuate.
const multipassNoneSentinel = "NONE"

// multipassSystemPrompt narrows the LLM's job to verbatim copy so the
// answer model receives source text it can directly cite. Any
// paraphrase or summary breaks the n-gram citation validator
// downstream (the validator matches against the chunk's original
// content via the sources slice; an LLM-summarised excerpt that no
// longer matches would invalidate every otherwise-correct citation).
const multipassSystemPrompt = "You are a precise extraction helper. " +
	"You only copy sentences verbatim from the supplied passage. " +
	"You never invent, infer, summarise, or rewrite content."

// multipassUserPrompt is intentionally short — extraction is a
// constrained-output task and a longer prompt does not improve
// adherence on Gemma-class fast-tier models.
func multipassUserPrompt(query, passage string) string {
	var sb strings.Builder
	sb.Grow(len(query) + len(passage) + 256)
	sb.WriteString("User query:\n")
	sb.WriteString(query)
	sb.WriteString("\n\nPassage:\n")
	sb.WriteString(passage)
	sb.WriteString("\n\nList the sentences from the passage that directly help answer the query. ")
	sb.WriteString("Copy them verbatim, one per line. Do not paraphrase, summarise, or add information not present in the passage. ")
	sb.WriteString("If no sentence in the passage is relevant to the query, output exactly: NONE")
	return sb.String()
}

// multipassResult is one chunk's outcome from the per-chunk LLM call.
// keep=false + err==nil means the LLM emitted NONE; keep=true + err!=nil
// means the call failed and the caller should preserve the original
// chunk content (best-effort semantics).
type multipassResult struct {
	keep      bool
	extracted string
	err       error
}

// RunMultipassExtraction fans out one fast-tier LLM call per chunk to
// extract only the sentences relevant to query. Returns a new slice
// (chunks parameter is never mutated) with each kept chunk's Content
// replaced by the extracted text; chunks where extraction yielded no
// relevant facts are dropped.
//
// Best-effort semantics: an extraction error on a single chunk does
// NOT abort the pre-pass. The original chunk passes through with its
// content intact — better to show the answer model a raw chunk than to
// drop it on a transient LLM hiccup.
//
// On ctx cancellation the function returns whatever the completed
// goroutines extracted, in original order, omitting cancelled chunks
// (which never produced a result — the caller's deadline drove the
// decision so dropping them is the right call). The one exception is a
// full cancellation that drops *every* chunk: rather than hand the
// answer model no retrieval context at all, the original chunks pass
// through unextracted (same "raw chunk beats no chunk" rule as the
// per-chunk error path).
func RunMultipassExtraction(
	ctx context.Context,
	aiResolver *ai.ConfigResolver,
	kbID, query string,
	chunks []vector.SearchChunk,
	modelOverride string,
) []vector.SearchChunk {
	if len(chunks) == 0 || aiResolver == nil || strings.TrimSpace(query) == "" {
		return chunks
	}

	start := time.Now()
	defer func() {
		observability.ObserveMultipassTurnSeconds(time.Since(start).Seconds())
	}()

	results := make([]multipassResult, len(chunks))
	sem := make(chan struct{}, multipassConcurrency)
	var wg sync.WaitGroup
	// Each iteration's `i` is a per-iteration variable (Go 1.22+ loop-scope
	// semantics, confirmed by the module's `go 1.26` directive), so each
	// goroutine writes only to its own results[i] slot — no concurrent
	// slice-element write, no need for an inner shadow var.
	for i := range chunks {
		wg.Add(1)
		safego.GoCtx(ctx, func() {
			defer wg.Done()
			// Fast cancellation check before contending for a slot: a free
			// semaphore makes the select below a coin-flip, so an already-
			// expired deadline could otherwise still proceed. Drop straight
			// to keep=false; the full-cancellation fallback after wg.Wait
			// turns an all-dropped result back into the original chunks.
			if ctx.Err() != nil {
				return
			}
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				// Treat cancellation as "no result" — leave keep=false so
				// the chunk is dropped during compaction below.
				return
			}
			defer func() { <-sem }()

			passage := strings.TrimSpace(chunks[i].Content)
			if passage == "" {
				return
			}

			res, err := ai.GenerateCompletionWithModel(
				ctx, aiResolver,
				multipassUserPrompt(query, passage),
				multipassSystemPrompt,
				kbID,
				false, // wantReasoning — extraction is structured, no CoT needed
				modelOverride,
			)
			if err != nil {
				observability.RecordMultipassCall("error")
				results[i] = multipassResult{keep: true, extracted: chunks[i].Content, err: err}
				return
			}
			if res == nil {
				observability.RecordMultipassCall("error")
				results[i] = multipassResult{keep: true, extracted: chunks[i].Content}
				return
			}

			extracted := strings.TrimSpace(res.Content)
			// Normalise the NONE sentinel — accept "NONE", "none.", "  none  ",
			// "**NONE**" (some models bold). We require the entire payload to
			// match (after stripping markdown bold), not just contain "none",
			// to avoid dropping a chunk where "None" appears as a substring of
			// a legitimately extracted sentence ("None of the proposals…").
			normalised := strings.TrimSpace(strings.Trim(extracted, "*."))
			if strings.EqualFold(normalised, multipassNoneSentinel) || extracted == "" {
				observability.RecordMultipassCall("dropped")
				results[i] = multipassResult{keep: false}
				return
			}
			observability.RecordMultipassCall("kept")
			results[i] = multipassResult{keep: true, extracted: extracted}
		})
	}
	wg.Wait()

	out := make([]vector.SearchChunk, 0, len(chunks))
	for i, r := range results {
		if r.err != nil {
			out = append(out, chunks[i])
			continue
		}
		if !r.keep {
			continue
		}
		c := chunks[i]
		c.Content = r.extracted
		out = append(out, c)
	}

	// Full-cancellation fallback: if the deadline fired before any goroutine
	// produced a result, every chunk was dropped (keep=false) and out is
	// empty. Returning nothing would leave the answer model with no context;
	// pass the original chunks through instead, unextracted.
	if len(out) == 0 && ctx.Err() != nil {
		return chunks
	}

	dropped := len(chunks) - len(out)
	dropRate := float64(dropped) / float64(len(chunks))
	observability.ObserveMultipassDropRate(dropRate)
	logctx.From(ctx).Info("rag.multipass.completed",
		"chunks_in", len(chunks),
		"chunks_out", len(out),
		"dropped", dropped,
		"drop_rate", dropRate,
		"duration_ms", time.Since(start).Milliseconds(),
	)
	return out
}
