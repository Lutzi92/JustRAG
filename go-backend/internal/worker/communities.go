package worker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hibiken/asynq"

	"github.com/justrag/go-backend/internal/community"
	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/kg"
	"github.com/justrag/go-backend/internal/logctx"
)

// communityStore is the kg read/clear surface the handler needs (satisfied by *kg.PgStore).
type communityStore interface {
	EdgesForCommunities(ctx context.Context, kbID string) ([]kg.CommunityEdge, error)
	EntitiesForCommunities(ctx context.Context, kbID string, ids []int64) ([]kg.CommunityEntity, error)
	InternalEdgesForCommunities(ctx context.Context, kbID string) ([]kg.InternalEdgeRow, error)
	ClearCommunities(ctx context.Context, kbID string) error
	StoreCommunity(ctx context.Context, kbID string, level int, memberIDs []int64, summaryChunkID string, size int) error
}

// CommunitiesDeps are the injected dependencies. Embed/Summarise/Sink are
// per-KB factories. DeleteSummaries clears the vector community-summary chunks.
type CommunitiesDeps struct {
	Store           communityStore
	Reader          community.SiteConfigReader
	DeleteSummaries func(ctx context.Context, kbID string) error
	SummariseFor    func(kbID string) community.SummariseFunc
	EmbedFor        func(kbID string) community.EmbedFunc
	SinkFor         func(kbID string) community.Sink
	Publish         func(ctx context.Context, kbID string)
}

// NewKGCommunitiesBuildHandler runs the admin-triggered community build.
// No-op when the gate is off. Clear-then-rebuild; best-effort.
func NewKGCommunitiesBuildHandler(deps CommunitiesDeps) asynq.HandlerFunc {
	return func(ctx context.Context, task *asynq.Task) error {
		var p jobs.KGCommunitiesBuildPayload
		if err := json.Unmarshal(task.Payload(), &p); err != nil {
			return fmt.Errorf("unmarshal kg-communities payload: %w", err)
		}
		if p.KbID == "" {
			return fmt.Errorf("kg-communities: empty kbId")
		}
		if !community.Enabled(ctx, deps.Reader) {
			logctx.From(ctx).Info("communities: gate off; skipping", "kb_id", p.KbID)
			return nil
		}
		// Clear-then-rebuild: prior community rows + their summary chunks.
		if err := deps.Store.ClearCommunities(ctx, p.KbID); err != nil {
			return fmt.Errorf("kg-communities: clear: %w", err)
		}
		if deps.DeleteSummaries != nil {
			if err := deps.DeleteSummaries(ctx, p.KbID); err != nil {
				return fmt.Errorf("kg-communities: clear summaries: %w", err)
			}
		}
		kgEdges, err := deps.Store.EdgesForCommunities(ctx, p.KbID)
		if err != nil {
			return fmt.Errorf("kg-communities: load edges: %w", err)
		}
		edges := make([]community.Edge, len(kgEdges))
		idSet := map[int64]bool{}
		for i, e := range kgEdges {
			edges[i] = community.Edge{Src: e.Src, Dst: e.Dst, Weight: e.Weight}
			idSet[e.Src] = true
			idSet[e.Dst] = true
		}
		ids := make([]int64, 0, len(idSet))
		for id := range idSet {
			ids = append(ids, id)
		}
		ents, err := deps.Store.EntitiesForCommunities(ctx, p.KbID, ids)
		if err != nil {
			return fmt.Errorf("kg-communities: load entities: %w", err)
		}
		members := make(map[int64]community.Member, len(ents))
		for _, e := range ents {
			members[e.ID] = community.Member{ID: e.ID, Name: e.Name, Type: e.Type}
		}
		ie, err := deps.Store.InternalEdgesForCommunities(ctx, p.KbID)
		if err != nil {
			return fmt.Errorf("kg-communities: load internal edges: %w", err)
		}
		internalEdges := make([]community.InternalEdge, len(ie))
		for i, e := range ie {
			internalEdges[i] = community.InternalEdge{Src: e.Src, Dst: e.Dst, Rel: e.Rel, Evidence: e.Evidence}
		}
		cfg := community.RunConfig{
			Resolution:      community.Resolution(ctx, deps.Reader),
			MinSize:         community.MinSize(ctx, deps.Reader),
			SummaryInputCap: community.SummaryInputCap(ctx, deps.Reader),
		}
		res, err := community.Run(ctx, edges, members, internalEdges, deps.SummariseFor(p.KbID), deps.EmbedFor(p.KbID), deps.SinkFor(p.KbID), cfg)
		if err != nil {
			return fmt.Errorf("kg-communities: run: %w", err)
		}
		logctx.From(ctx).Info("communities.done", "kb_id", p.KbID, "communities", res.Communities, "summarised", res.Summarised)
		if res.Summarised > 0 && deps.Publish != nil {
			deps.Publish(ctx, p.KbID)
		}
		return nil
	}
}
