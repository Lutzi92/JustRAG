// Package contentgen provides HTTP handlers for AI-powered content generation
// endpoints (flashcards, presentation, podcast, analysis, abstract, chart).
package contentgen

import (
	"context"
	"fmt"
	"strings"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/vector"
)

// getContext searches the KB for relevant chunks and builds a numbered context string.
// Each chunk is formatted as "[N] [Source: filename]\ncontent\n\n---\n\n".
func getContext(ctx context.Context, searchSvc Searcher, kbID, topic string, limit int) (string, error) {
	result, err := searchSvc.Search(ctx, kbID, topic, limit, vector.SearchOptions{})
	if err != nil {
		return "", fmt.Errorf("getContext search: %w", err)
	}

	if len(result.Chunks) == 0 {
		return "", nil
	}

	var sb strings.Builder
	for i, chunk := range result.Chunks {
		fmt.Fprintf(&sb, "[%d] [Source: %s]\n%s\n\n---\n\n", i+1, chunk.FileName, chunk.Content)
	}
	return sb.String(), nil
}

// getContextForFile searches the KB restricted to a specific file ID.
func getContextForFile(ctx context.Context, searchSvc Searcher, kbID, fileID, topic string, limit int) (string, error) {
	result, err := searchSvc.Search(ctx, kbID, topic, limit, vector.SearchOptions{
		FileIDs: []string{fileID},
	})
	if err != nil {
		return "", fmt.Errorf("getContextForFile search: %w", err)
	}

	if len(result.Chunks) == 0 {
		return "", nil
	}

	var sb strings.Builder
	for i, chunk := range result.Chunks {
		fmt.Fprintf(&sb, "[%d] [Source: %s]\n%s\n\n---\n\n", i+1, chunk.FileName, chunk.Content)
	}
	return sb.String(), nil
}

// generateWithLLM calls the AI completion API with the given prompt and system prompt.
func generateWithLLM(ctx context.Context, resolver *ai.ConfigResolver, prompt, systemPrompt, kbID string) (string, error) {
	result, err := ai.GenerateCompletion(ctx, resolver, prompt, systemPrompt, kbID, false)
	if err != nil {
		return "", fmt.Errorf("generateWithLLM: %w", err)
	}
	return result.Content, nil
}
