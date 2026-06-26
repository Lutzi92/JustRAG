package community

import (
	"context"
	"fmt"
	"strings"

	"github.com/justrag/go-backend/internal/logctx"
)

// SummariseFunc produces a community summary from the rendered input (LLM seam).
type SummariseFunc func(ctx context.Context, input string) (string, error)

// EmbedFunc embeds the summary with the KB embedder.
type EmbedFunc func(ctx context.Context, text string) ([]float64, error)

// Sink persists a summarised community.
type Sink interface {
	InsertSummaryChunk(ctx context.Context, content string, embedding []float64) (chunkID string, err error)
	StoreCommunity(ctx context.Context, level int, memberIDs []int64, summaryChunkID string, size int) error
}

// RunConfig holds the tunables resolved from site config.
type RunConfig struct {
	Resolution      float64
	MinSize         int
	SummaryInputCap int
}

// Result summarises a run.
type Result struct {
	Communities int
	Summarised  int
}

// Run detects communities over edges, summarises each (members + internal
// edges), embeds, and persists. members maps every entity id → Member;
// internalEdges is the full KB edge list (Run slices each community's subset).
// Per-community summarise/embed/insert errors log + skip — Run NEVER returns a
// non-nil error (the error return exists for forward-compat / signature parity).
func Run(ctx context.Context, edges []Edge, members map[int64]Member, internalEdges []InternalEdge, summarise SummariseFunc, embed EmbedFunc, sink Sink, cfg RunConfig) (Result, error) {
	var res Result
	comms := Detect(edges, Config{Resolution: cfg.Resolution, MinSize: cfg.MinSize})
	res.Communities = len(comms)
	for _, c := range comms {
		set := make(map[int64]bool, len(c.Members))
		for _, id := range c.Members {
			set[id] = true
		}
		ms := make([]Member, 0, len(c.Members))
		for _, id := range c.Members {
			if m, ok := members[id]; ok {
				ms = append(ms, m)
			}
		}
		var ies []InternalEdge
		for _, e := range internalEdges {
			if set[e.Src] && set[e.Dst] {
				ies = append(ies, e)
			}
		}
		input := BuildSummaryInput(ms, ies, cfg.SummaryInputCap)
		summary, err := summarise(ctx, input)
		if err != nil || strings.TrimSpace(summary) == "" {
			logctx.From(ctx).Warn("community: summarise failed; skipping", "size", len(c.Members), "err", err)
			continue
		}
		emb, err := embed(ctx, summary)
		if err != nil || len(emb) == 0 {
			logctx.From(ctx).Warn("community: embed failed; skipping", "err", err)
			continue
		}
		chunkID, err := sink.InsertSummaryChunk(ctx, summary, emb)
		if err != nil {
			logctx.From(ctx).Warn("community: insert summary chunk failed; skipping", "err", err)
			continue
		}
		if err := sink.StoreCommunity(ctx, 0, c.Members, chunkID, len(c.Members)); err != nil {
			logctx.From(ctx).Warn("community: store failed", "err", err)
			continue
		}
		res.Summarised++
	}
	return res, nil
}

// SiteConfigReader is the minimal site-config surface.
type SiteConfigReader interface {
	GetSiteConfigValue(ctx context.Context, key string) (*string, error)
}

func readStr(ctx context.Context, r SiteConfigReader, key string) string {
	if r == nil {
		return ""
	}
	v, err := r.GetSiteConfigValue(ctx, key)
	if err != nil || v == nil {
		return ""
	}
	return strings.TrimSpace(*v)
}

// Enabled gates the feature. Default off.
func Enabled(ctx context.Context, r SiteConfigReader) bool {
	return strings.EqualFold(readStr(ctx, r, "kg_communities_enabled"), "true")
}

// Resolution defaults 1.0, clamped (0,5].
func Resolution(ctx context.Context, r SiteConfigReader) float64 {
	if v := readStr(ctx, r, "kg_community_resolution"); v != "" {
		var f float64
		if _, err := fmt.Sscanf(v, "%g", &f); err == nil && f > 0 && f <= 5 {
			return f
		}
	}
	return 1.0
}

// MinSize defaults 3, clamped [2,100].
func MinSize(ctx context.Context, r SiteConfigReader) int {
	if v := readStr(ctx, r, "kg_community_min_size"); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n >= 2 && n <= 100 {
			return n
		}
	}
	return 3
}

// SummaryInputCap defaults 8000 runes, clamped [500,40000].
func SummaryInputCap(ctx context.Context, r SiteConfigReader) int {
	if v := readStr(ctx, r, "kg_community_summary_input_cap"); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n >= 500 && n <= 40000 {
			return n
		}
	}
	return 8000
}
