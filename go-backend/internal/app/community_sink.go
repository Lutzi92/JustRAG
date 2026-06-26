package app

import (
	"context"

	"github.com/justrag/go-backend/internal/vector"
)

// communitySink implements community.Sink: it writes the summary as a dim-keyed
// community_summary chunk and records the kg_communities row. It is constructed
// by the SinkFor factory wired in worker.go, so it lives in the app package
// alongside the infra it adapts.
type communitySink struct {
	svc   *vector.ChunkService
	store interface {
		StoreCommunity(ctx context.Context, kbID string, level int, memberIDs []int64, summaryChunkID string, size int) error
	}
	kbID     string
	fileID   string
	pgConfig string
}

func (s *communitySink) InsertSummaryChunk(ctx context.Context, content string, embedding []float64) (string, error) {
	return s.svc.InsertCommunitySummary(ctx, s.kbID, s.fileID, content, embedding, s.pgConfig)
}

func (s *communitySink) StoreCommunity(ctx context.Context, level int, memberIDs []int64, summaryChunkID string, size int) error {
	return s.store.StoreCommunity(ctx, s.kbID, level, memberIDs, summaryChunkID, size)
}
