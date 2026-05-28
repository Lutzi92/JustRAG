package processor

import (
	"context"
	"fmt"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/processor/raptor"
	"github.com/justrag/go-backend/internal/vector"
)

// raptorStoreAdapter bridges raptor.Store to vector.ChunkService. The
// raptor package defines its own row shape (raptor.LeafRow,
// raptor.SummaryRow) to keep an internal/vector dependency out — the
// adapter is the one place where the two shapes meet.
//
// InsertSummary is the only non-trivial method:
//
//  1. Build a vector.ChunkInput tagged node_kind="summary",
//     tree_level=row.TreeLevel.
//  2. Call AddDocumentChunks (one row). This is the canonical insert
//     path with embedding_low derivation, tsvector building, and
//     content_hash; duplicating it into an INSERT-RETURNING just to
//     get the id isn't worth the maintenance cost.
//  3. Recover the id via LookupSummaryID (kb_id + file_id + content +
//     node_kind='summary' is unique by construction within one
//     cluster-batch — clusters are content-disjoint within a level).
//  4. Back-patch raptor_parent_id on the child rows via
//     SetRaptorParent.
type raptorStoreAdapter struct {
	svc *vector.ChunkService
}

func (a *raptorStoreAdapter) ListLeaves(ctx context.Context, kbID, fileID string, dim int) ([]raptor.LeafRow, error) {
	rows, err := a.svc.ListLeafEmbeddingsForRaptor(ctx, kbID, fileID, dim, 500)
	if err != nil {
		return nil, err
	}
	out := make([]raptor.LeafRow, len(rows))
	for i, r := range rows {
		out[i] = raptor.LeafRow{ID: r.ID, Content: r.Content, Embedding: r.Embedding}
	}
	return out, nil
}

func (a *raptorStoreAdapter) InsertSummary(ctx context.Context, kbID, fileID, pgConfig string, dim int, row raptor.SummaryRow) (string, error) {
	input := vector.ChunkInput{
		Content:   row.Content,
		Embedding: row.Embedding,
		KbID:      kbID,
		FileID:    fileID,
		NodeKind:  "summary",
		TreeLevel: row.TreeLevel,
		Metadata: map[string]any{
			"chunkKind":  "raptor_summary",
			"tree_level": row.TreeLevel,
		},
	}
	if err := a.svc.AddDocumentChunks(ctx, fileID, []vector.ChunkInput{input}, dim, pgConfig); err != nil {
		return "", fmt.Errorf("raptor adapter: insert summary: %w", err)
	}
	id, err := a.svc.LookupSummaryID(ctx, kbID, fileID, row.Content, dim)
	if err != nil {
		return "", fmt.Errorf("raptor adapter: lookup id: %w", err)
	}
	if err := a.svc.SetRaptorParent(ctx, row.ChildIDs, id, dim); err != nil {
		return "", fmt.Errorf("raptor adapter: setparent: %w", err)
	}
	return id, nil
}

// raptorEmbedderAdapter calls ai.GenerateEmbedding with the
// processor's existing embedding cache so re-summaries (e.g. across
// re-ingests with deterministic seeds) hit the cache. The summary
// text is generated prose, not a query — symmetric embedding (no
// instruction prefix) is correct here, same as for leaf chunks.
type raptorEmbedderAdapter struct {
	resolver *ai.ConfigResolver
	cache    *ai.EmbeddingCache
}

func (a *raptorEmbedderAdapter) EmbedOne(ctx context.Context, kbID, text string) ([]float64, error) {
	return ai.GenerateEmbedding(ctx, a.resolver, text, kbID, a.cache)
}

// raptorSummariserAdapter unpacks the two-message slice from
// BuildSummaryMessages and calls the deterministic completion
// helper. Deterministic = temperature 0 / top_p 1; we want
// re-ingest of the same file to produce the same summaries given
// the same model.
type raptorSummariserAdapter struct {
	resolver *ai.ConfigResolver
}

func (a *raptorSummariserAdapter) Summarise(ctx context.Context, kbID, model, fileName string, level int, concat string) (string, error) {
	msgs := raptor.BuildSummaryMessages(fileName, level, concat)
	// BuildSummaryMessages always returns exactly [system, user];
	// pulling them out positionally is safe.
	system, user := msgs[0].Content, msgs[1].Content
	res, err := ai.GenerateCompletionWithModelDeterministic(ctx, a.resolver, user, system, kbID, false, model)
	if err != nil {
		return "", err
	}
	return res.Content, nil
}
