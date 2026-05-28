package ai

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"

	"github.com/justrag/go-backend/internal/safego"
)

// ---------------------------------------------------------------------------
// Qwen3-Reranker chat-template scorer
// ---------------------------------------------------------------------------

// Qwen3-Reranker was fine-tuned with a binary yes/no head on a chat template.
// The Qwen team's released inference code feeds a (query, doc) pair through
// the template and reads back logit(yes) vs logit(no) — that's the trained
// decision boundary. Routing the same model through a generic /rerank
// endpoint works but skips the trained head; the chat-template path is what
// Qwen3's own benchmarks use.
//
// The prompt shape comes from the official Qwen3-Reranker model card:
//
//	system: Judge whether the Document meets the requirements based on the
//	  Query and the Instruct provided. Note that the answer can only be
//	  "yes" or "no".
//	user:   <Instruct>: {instruction}
//	        <Query>: {query}
//	        <Document>: {doc}
//
// We then sample max_tokens=1 with logprobs=true and top_logprobs>=20 and
// read the first-token distribution to compute softmax(yes, no)[0]. That
// scalar becomes the relevance score — already calibrated to [0, 1] with
// the same semantics as a probability, so it's a drop-in replacement for
// the Cohere-style `relevance_score` field.
const (
	qwen3RerankSystemPrompt = `Judge whether the Document meets the requirements based on the Query and the Instruct provided. Note that the answer can only be "yes" or "no".`
	qwen3RerankUserTemplate = "<Instruct>: %s\n<Query>: %s\n<Document>: %s"
)

// defaultQwen3RerankInstruction is used when the admin panel's
// `rerank_instruction` is empty. It mirrors the phrasing from the Qwen3
// model card, which is what the model saw during training for the
// retrieval task.
const defaultQwen3RerankInstruction = "Given a web search query, retrieve relevant passages that answer the query"

// qwen3RerankConcurrency caps the number of parallel chat calls fired at the
// reranker backend for a single Rerank invocation. vLLM and sglang batch
// concurrent requests well on the server side, but we still bound the client
// fan-out to keep the queue depth sane and leave GPU headroom for concurrent
// user-facing chat completions.
const qwen3RerankConcurrency = 8

// BuildQwen3RerankMessages returns the system+user chat messages for a single
// (query, document) pair per the Qwen3-Reranker template. Exported so the
// template is exercisable from tests without going through the HTTP client.
func BuildQwen3RerankMessages(instruction, query, doc string) []ChatMessage {
	if instruction == "" {
		instruction = defaultQwen3RerankInstruction
	}
	return []ChatMessage{
		{Role: "system", Content: qwen3RerankSystemPrompt},
		{Role: "user", Content: fmt.Sprintf(qwen3RerankUserTemplate, instruction, query, doc)},
	}
}

// IsQwen3RerankerModel returns true when the model name looks like a
// Qwen3-Reranker variant. The heuristic is intentionally loose: the caller
// already gates the chat-template path behind an explicit site_config flag,
// so this function only has to distinguish "don't accidentally apply the
// Qwen3 template to a Cohere model" — false positives on an unrelated model
// named "qwen3-rerank-something" are fine because the operator opted in.
func IsQwen3RerankerModel(name string) bool {
	lower := strings.ToLower(name)
	return strings.Contains(lower, "qwen") && strings.Contains(lower, "rerank")
}

// scoreYesNoFromLogprobs returns softmax(yes, no)[0] from a first-token
// top_logprobs list. When both tokens are present the score is the exact
// probability Qwen3 assigned to "yes". When only one is present we fill the
// missing side with a conservative floor (logprob -20, ≈ 2e-9) so the score
// still encodes the present signal instead of collapsing to 0.5. When
// neither is present we return 0.5 — the response is degraded and we don't
// want a missing token to silently rank a doc at either extreme.
//
// Token matching is case-insensitive and tolerates leading whitespace
// because tokenisers sometimes emit " yes" / " no" with a leading space.
func scoreYesNoFromLogprobs(top []ChatLogprobEntry) float64 {
	const missingFloor = -20.0

	yesLogp, noLogp := math.Inf(-1), math.Inf(-1)
	for _, e := range top {
		t := strings.ToLower(strings.TrimSpace(e.Token))
		switch t {
		case "yes":
			if e.Logprob > yesLogp {
				yesLogp = e.Logprob
			}
		case "no":
			if e.Logprob > noLogp {
				noLogp = e.Logprob
			}
		}
	}

	yesPresent := !math.IsInf(yesLogp, -1)
	noPresent := !math.IsInf(noLogp, -1)

	if !yesPresent && !noPresent {
		return 0.5
	}
	if !yesPresent {
		yesLogp = missingFloor
	}
	if !noPresent {
		noLogp = missingFloor
	}

	// Numerically-stable softmax of two logits.
	maxL := math.Max(yesLogp, noLogp)
	eYes := math.Exp(yesLogp - maxL)
	eNo := math.Exp(noLogp - maxL)
	return eYes / (eYes + eNo)
}

// rerankWithQwen3ChatTemplate scores each (query, doc) pair via the
// Qwen3-Reranker chat-template path and returns RerankScores in the
// original document order. Parallelism is bounded by qwen3RerankConcurrency.
//
// On any pair-level error the function returns an error — the caller (Rerank)
// is responsible for deciding whether to fall back to the legacy /rerank
// endpoint. Returning per-pair partial results would silently mix two score
// calibrations, which is worse than taking the fallback whole.
func rerankWithQwen3ChatTemplate(ctx context.Context, cfg *ResolvedConfig, query, instruction string, documents []string) ([]RerankScore, error) {
	scores := make([]RerankScore, len(documents))
	errs := make([]error, len(documents))

	sem := make(chan struct{}, qwen3RerankConcurrency)
	var wg sync.WaitGroup

	for i, doc := range documents {
		// Acquire the semaphore slot from the parent loop so a saturated
		// fan-out can't block past ctx cancellation. A plain `sem <- struct{}{}`
		// here would ignore ctx, hanging this loop indefinitely if the request
		// is cancelled while every slot is in flight. wg.Add follows the
		// successful acquire so the counter exactly matches the number of
		// live goroutines — matches the raptor builder convention and avoids
		// a manual wg.Done in the cancellation branch.
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return nil, ctx.Err()
		}
		wg.Add(1)
		go func() {
			// Defer order matters: RecoverError is registered LAST so it
			// runs FIRST during a panic unwind, completing its write to
			// errs[i] before wg.Done() establishes the happens-before edge
			// with wg.Wait() in the parent goroutine. The previous order
			// (RecoverError last in unwinding) raced: the parent could
			// observe wg.Done() and scan a still-nil errs[i], returning
			// silently zero-valued scores to the caller. A panic in
			// qwen3RerankOne (HTTP/JSON shape surprises in third-party
			// endpoints) now reliably becomes errs[i] = "panic: ..." which
			// the post-Wait error scan surfaces to the caller via the
			// existing whole-batch fallback path.
			defer wg.Done()
			defer func() { <-sem }()
			defer safego.RecoverError(&errs[i])

			score, err := qwen3RerankOne(ctx, cfg, query, instruction, doc)
			if err != nil {
				errs[i] = err
				return
			}
			scores[i] = RerankScore{Index: i, Score: score}
		}()
	}
	wg.Wait()

	for i, e := range errs {
		if e != nil {
			return nil, fmt.Errorf("qwen3 rerank: pair %d: %w", i, e)
		}
	}
	return scores, nil
}

// qwen3RerankOne scores a single (query, doc) pair. Factored out of the
// fan-out loop so it's testable with a mock HTTP server independently of
// goroutine plumbing.
func qwen3RerankOne(ctx context.Context, cfg *ResolvedConfig, query, instruction, doc string) (float64, error) {
	client := CachedClient(cfg.BaseURL, cfg.APIKey)
	// temperature=0 makes the generation greedy, which means the first-token
	// distribution is what the model actually believes — no sampling noise
	// smearing the yes/no probabilities.
	temp := 0.0
	resp, err := client.ChatCompletion(ctx, &ChatRequest{
		Model:       cfg.RerankModel,
		Messages:    BuildQwen3RerankMessages(instruction, query, doc),
		Temperature: &temp,
		MaxTokens:   1,
		Logprobs:    true,
		TopLogprobs: 20,
	})
	if err != nil {
		return 0, err
	}
	if len(resp.Choices) == 0 {
		return 0, fmt.Errorf("empty choices")
	}
	choice := resp.Choices[0]
	if choice.Logprobs == nil || len(choice.Logprobs.Content) == 0 {
		return 0, fmt.Errorf("missing logprobs (backend may not support top_logprobs)")
	}
	return scoreYesNoFromLogprobs(choice.Logprobs.Content[0].TopLogprobs), nil
}
