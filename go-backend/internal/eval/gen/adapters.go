package gen

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/prompts"
	"github.com/justrag/go-backend/internal/vector"
)

// --- corpusReader over Postgres + ChunkService ---

// DBCorpus implements corpusReader using the main Postgres pool (for file
// listing) and the vector ChunkService (for chunk fetching).
type DBCorpus struct {
	mainDB    *pgxpool.Pool
	chunkSvc  *vector.ChunkService
	dimsCache []int
}

// NewDBCorpus constructs a DBCorpus. mainDB is used for file listing queries;
// chunkSvc is used to fetch chunks from the correct dimension table.
func NewDBCorpus(mainDB *pgxpool.Pool, chunkSvc *vector.ChunkService) *DBCorpus {
	return &DBCorpus{mainDB: mainDB, chunkSvc: chunkSvc}
}

// ListFiles returns all files in the given KB ordered by creation time.
func (c *DBCorpus) ListFiles(ctx context.Context, kbID string) ([]FileRef, error) {
	rows, err := c.mainDB.Query(ctx,
		`SELECT id::text, name FROM files WHERE kb_id = $1::uuid ORDER BY created_at ASC`, kbID)
	if err != nil {
		return nil, fmt.Errorf("list files: %w", err)
	}
	defer rows.Close()
	var out []FileRef
	for rows.Next() {
		var f FileRef
		if err := rows.Scan(&f.ID, &f.Name); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// GetChunks returns all chunks for the given file. It tries every known
// dimension table (lazily cached) and returns the first non-empty result.
func (c *DBCorpus) GetChunks(ctx context.Context, kbID, fileID string) ([]vector.FileChunkRow, error) {
	if c.dimsCache == nil {
		dims, err := c.chunkSvc.ListChunkTableDimensions(ctx)
		if err != nil {
			return nil, err
		}
		c.dimsCache = dims
	}
	for _, d := range c.dimsCache {
		rows, err := c.chunkSvc.GetChunksByFileID(ctx, kbID, fileID, d)
		if err != nil {
			continue
		}
		if len(rows) > 0 {
			return rows, nil
		}
	}
	return nil, nil
}

// --- questionGen over ai.* ---

// AIQGen implements questionGen by delegating to the ai package's LLM helpers.
type AIQGen struct {
	resolver *ai.ConfigResolver
	kbID     string
	lang     string
	model    string
}

// NewAIQGen constructs an AIQGen. lang is the ISO language tag (e.g. "en",
// "de") passed to system prompts; model is an optional model override
// (empty = use the KB default fast-tier model).
func NewAIQGen(resolver *ai.ConfigResolver, kbID, lang, model string) *AIQGen {
	return &AIQGen{resolver: resolver, kbID: kbID, lang: lang, model: model}
}

// LookupQuestions generates up to n factual lookup questions for a single chunk.
func (a *AIQGen) LookupQuestions(ctx context.Context, fileName, document, chunk string, n int) ([]string, error) {
	return ai.GenerateHypotheticalQuestions(ctx, a.resolver, fileName, document, chunk, a.kbID, n, a.lang, a.model)
}

// ComplexQuestion generates one complex-reasoning question for a single chunk.
func (a *AIQGen) ComplexQuestion(ctx context.Context, fileName, document, chunk string) (string, error) {
	res, err := ai.GenerateCompletionWithModelDeterministic(ctx, a.resolver,
		prompts.GenComplexUserPrompt(fileName, chunk), prompts.GenComplexSystemPrompt(a.lang),
		a.kbID, false, a.model)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(res.Content), nil
}

// EnumerationQuestion generates one enumeration question spanning multiple chunks.
func (a *AIQGen) EnumerationQuestion(ctx context.Context, fileName string, chunks []string) (string, error) {
	joined := strings.Join(chunks, "\n\n")
	res, err := ai.GenerateCompletionWithModelDeterministic(ctx, a.resolver,
		prompts.GenEnumerationUserPrompt(fileName, joined), prompts.GenEnumerationSystemPrompt(a.lang),
		a.kbID, false, a.model)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(res.Content), nil
}

// BridgeQuestion generates one multi-hop question spanning two files.
func (a *AIQGen) BridgeQuestion(ctx context.Context, aName, aText, bName, bText string) (string, error) {
	res, err := ai.GenerateCompletionWithModelDeterministic(ctx, a.resolver,
		prompts.GenBridgeUserPrompt(aName, aText, bName, bText), prompts.GenBridgeSystemPrompt(a.lang),
		a.kbID, false, a.model)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(res.Content), nil
}

// --- neighborFinder over the search service ---

// Searcher is the subset of *vector.SearchService the neighbor finder needs.
// The method signature matches *vector.SearchService.Search exactly so that
// the concrete type satisfies this interface without a wrapper.
type Searcher interface {
	Search(ctx context.Context, kbID, query string, limit int, opts vector.SearchOptions) (*vector.SearchResult, error)
}

// compile-time proof that *vector.SearchService satisfies Searcher.
var _ Searcher = (*vector.SearchService)(nil)

// SearchNeighbors implements neighborFinder by running a vector search and
// returning the highest-scoring chunk from a different file.
type SearchNeighbors struct {
	searcher Searcher
	k        int
}

// NewSearchNeighbors constructs a SearchNeighbors. k is the number of
// candidate chunks to retrieve; values ≤ 0 are clamped to 5.
func NewSearchNeighbors(searcher Searcher, k int) *SearchNeighbors {
	if k <= 0 {
		k = 5
	}
	return &SearchNeighbors{searcher: searcher, k: k}
}

// NearestOtherFile returns the top chunk that comes from a file other than
// seedFileID. It returns (Neighbor{}, false, nil) when no qualifying chunk
// is found.
func (s *SearchNeighbors) NearestOtherFile(ctx context.Context, kbID, seedFileID, seedText string) (Neighbor, bool, error) {
	res, err := s.searcher.Search(ctx, kbID, seedText, s.k,
		vector.SearchOptions{QueryType: vector.QueryTypeComplexReasoning})
	if err != nil {
		return Neighbor{}, false, err
	}
	for _, ch := range res.Chunks {
		if ch.FileID != seedFileID {
			return Neighbor{
				FileID:    ch.FileID,
				FileName:  ch.FileName,
				ChunkText: ch.Content,
				Score:     ch.Score,
			}, true, nil
		}
	}
	return Neighbor{}, false, nil
}
