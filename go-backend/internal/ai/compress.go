package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/justrag/go-backend/internal/prompts"
)

// EvidentialityScore is one chunk's verdict from the ECoRAG
// classifier. Score is in [0, 1]: 0 = no evidence to answer the
// query, 1 = strong direct evidence. The caller filters by a
// threshold; missing scores (LLM error or out-of-range index)
// are treated as "keep" (1.0) so a faulty classifier can never
// drop a chunk silently.
type EvidentialityScore struct {
	Index int
	Score float64
}

// MaxEvidentialityChunkChars caps how much of each chunk we feed
// the classifier per call. Chunks longer than this are truncated;
// the classifier's job is "does this passage support an answer",
// not "deeply read the whole thing", and a ~600-char preview
// captures enough surface signal to score reliably. Keeps the
// total input bounded at ~15 × 600 ≈ 9 000 chars for a typical
// pre-prompt-assembly chunk pool.
const MaxEvidentialityChunkChars = 600

// ScoreEvidentiality runs the ECoRAG (arXiv:2506.05167) one-shot
// classifier: a single fast-tier LLM call scores ALL retrieved
// chunks for evidentiality (do they actually support an answer
// to the query?), distinct from relevance (which the reranker
// already captured — a chunk can be on-topic without containing
// the answer).
//
// Returns a map keyed by the input slice index. Indices missing
// from the map are unscored — the caller's contract is to keep
// those chunks (fail-open). Empty chunks or chunks below
// min-length short-circuit with no LLM call.
//
// Cost: one fast-tier LLM call per chat turn the compressor
// fires on; the caller (chat/service.go) gates this on a chunk-
// count threshold (typically 15) so small candidate pools where
// every chunk is likely useful skip the call entirely.
func ScoreEvidentiality(ctx context.Context, resolver *ConfigResolver, query string, chunkContents []string, kbID, lang, modelOverride string) (map[int]float64, error) {
	if resolver == nil {
		return nil, fmt.Errorf("evidentiality: nil resolver")
	}
	if strings.TrimSpace(query) == "" || len(chunkContents) == 0 {
		return nil, nil
	}

	// Build the LLM input: numbered passages, each truncated to
	// MaxEvidentialityChunkChars. Numbers are 0-indexed so the
	// returned scores map directly back to the input slice.
	var sb strings.Builder
	sb.Grow(len(chunkContents)*(MaxEvidentialityChunkChars+16) + 256)
	sb.WriteString("Query:\n")
	sb.WriteString(query)
	sb.WriteString("\n\nPassages:\n")
	for i, c := range chunkContents {
		fmt.Fprintf(&sb, "[%d] ", i)
		if len(c) > MaxEvidentialityChunkChars {
			sb.WriteString(c[:MaxEvidentialityChunkChars])
			sb.WriteString("…")
		} else {
			sb.WriteString(c)
		}
		sb.WriteString("\n\n")
	}
	sb.WriteString("Score each passage in 0.0-1.0 on whether it provides DIRECT evidence to answer the query. ")
	sb.WriteString("Return ONLY a JSON object {\"scores\": [float, float, ...]} with one float per passage in the same order. ")
	sb.WriteString("No explanation, no introduction.")

	result, err := GenerateCompletionWithModel(ctx, resolver, sb.String(), prompts.EvidentialitySystemPrompt(lang), kbID, false, modelOverride)
	if err != nil {
		return nil, fmt.Errorf("evidentiality: completion: %w", err)
	}
	if strings.TrimSpace(result.Content) == "" {
		return nil, fmt.Errorf("evidentiality: empty completion")
	}

	parsed, perr := parseEvidentialityScores(result.Content)
	if perr != nil {
		return nil, fmt.Errorf("evidentiality: parse: %w", perr)
	}

	// Build the index→score map. Indices outside [0, N) are
	// dropped — a hallucinated index can't influence a chunk.
	out := make(map[int]float64, len(parsed))
	for i, s := range parsed {
		if i >= len(chunkContents) {
			break
		}
		if s < 0 {
			s = 0
		}
		if s > 1 {
			s = 1
		}
		out[i] = s
	}
	return out, nil
}

// parseEvidentialityScores tolerates prose wrapping and either of
// two response shapes the prompt could legitimately emit: a bare
// JSON array of floats OR an object {"scores": [floats]}. Both
// land on []float64.
func parseEvidentialityScores(text string) ([]float64, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("empty")
	}
	b := []byte(text)

	// Object form first (the prompt asks for it explicitly).
	var obj struct {
		Scores []float64 `json:"scores"`
	}
	if err := json.Unmarshal(b, &obj); err == nil && obj.Scores != nil {
		return obj.Scores, nil
	}
	// Bare-array fallback.
	var arr []float64
	if err := json.Unmarshal(b, &arr); err == nil {
		return arr, nil
	}
	// Prose-wrapped object: find the first '{' and last '}'.
	if start := strings.Index(text, "{"); start >= 0 {
		if end := strings.LastIndex(text, "}"); end > start {
			if err := json.Unmarshal(b[start:end+1], &obj); err == nil && obj.Scores != nil {
				return obj.Scores, nil
			}
		}
	}
	// Prose-wrapped array.
	if start := strings.Index(text, "["); start >= 0 {
		if end := strings.LastIndex(text, "]"); end > start {
			if err := json.Unmarshal(b[start:end+1], &arr); err == nil {
				return arr, nil
			}
		}
	}
	return nil, fmt.Errorf("no parseable JSON in %q", truncForError(text))
}

func truncForError(s string) string {
	const maxLen = 200
	if len(s) > maxLen {
		return s[:maxLen] + "…"
	}
	return s
}
