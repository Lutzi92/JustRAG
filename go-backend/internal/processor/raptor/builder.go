package raptor

import (
	"context"
	"hash/fnv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/safego"
)

// Config carries the resolved site_config knobs the builder needs.
// Resolved once per Build() at the call site (so the builder is
// site_config-agnostic and unit-testable).
type Config struct {
	MinChunks            int
	MaxLevels            int
	BranchingFactor      int
	SummaryInputTokenCap int
	Concurrency          int
	SummaryModel         string
	// ClusteringAlgorithm selects the per-level clustering pass.
	// "" or "kmeans" (default): the v1 fixed-k Lloyd's algorithm
	// (ClusterByKMeans) with k = ceil(N / BranchingFactor).
	// "leiden" (T2-2): modularity-based community detection over a
	// k-NN cosine-similarity graph (ClusterByLeiden) — k is
	// determined by graph topology, not pre-specified, so the
	// branching factor is ignored on this path. Cited Frontiers
	// 2025 paper: +20 % QuALITY accuracy vs fixed-k K-means on
	// heterogeneous documents.
	ClusteringAlgorithm string
	// LeidenResolution is γ for the modularity formula. > 1
	// produces more, smaller clusters; < 1 produces fewer, larger.
	// Only consulted when ClusteringAlgorithm == "leiden". Default
	// 1.0.
	LeidenResolution float64
}

// BuildParams identify the file the tree is being built for and the
// vector dimension so the store can pick the right per-dim table.
type BuildParams struct {
	KbID       string
	FileID     string
	FileName   string
	Dimensions int
	PgConfig   string
}

// Stats is the return value of Build for telemetry callers. Reported
// as one trajectory-like log line at the end of every build so an
// operator can grep "raptor.build.end" and reconstruct what
// happened per file.
type Stats struct {
	LevelsBuilt    int
	SummariesAdded int
	LLMCalls       int
	DurationMs     int64
}

// BuilderInterface is the seam the processor uses to inject a
// Builder. Real path uses *Builder; tests inject a recording fake.
type BuilderInterface interface {
	Build(ctx context.Context, p BuildParams) (Stats, error)
}

// Builder grows a RAPTOR summary tree over the leaves of one file.
type Builder struct {
	store      Store
	embedder   Embedder
	summariser Summariser
	cfg        Config
}

// NewBuilder is the constructor. Caller (the processor package) is
// responsible for materialising adapters around vector.ChunkService
// and ai.* and resolving Config from site_config.
func NewBuilder(store Store, embedder Embedder, summariser Summariser, cfg Config) *Builder {
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 8
	}
	if cfg.SummaryInputTokenCap <= 0 {
		cfg.SummaryInputTokenCap = 12_000
	}
	return &Builder{store: store, embedder: embedder, summariser: summariser, cfg: cfg}
}

// Build constructs the tree. Returns Stats{LevelsBuilt:0} when the
// file is below MinChunks (a skip, not a failure). Per-cluster
// summary / embed / insert failures are logged + dropped (they
// reduce the level's branching but do not fail the whole build).
//
// Determinism: levelSeed hashes kbID + fileID + level, so re-ingest
// of the same file produces the same tree shape — useful for
// debugging eval regressions where you want to bisect "is this the
// builder or the embedder?".
func (b *Builder) Build(ctx context.Context, p BuildParams) (Stats, error) {
	start := time.Now()
	leaves, err := b.store.ListLeaves(ctx, p.KbID, p.FileID, p.Dimensions)
	if err != nil {
		observability.RecordRaptorBuild("failed")
		return Stats{}, err
	}
	if len(leaves) < b.cfg.MinChunks {
		observability.RecordRaptorBuild("skipped_min_chunks")
		logctx.From(ctx).Info("raptor.build.skipped",
			"fileId", p.FileID,
			"reason", "min_chunks",
			"leaf_count", len(leaves),
			"min_chunks", b.cfg.MinChunks,
		)
		return Stats{}, nil
	}

	current := make([]ClusterInput, len(leaves))
	contentByID := make(map[string]string, len(leaves))
	for i, l := range leaves {
		current[i] = ClusterInput{ID: l.ID, Embedding: l.Embedding}
		contentByID[l.ID] = l.Content
	}

	var stats Stats
	logctx.From(ctx).Info("raptor.build.start",
		"fileId", p.FileID,
		"leaf_count", len(leaves),
		"dimensions", p.Dimensions,
	)

	for level := 1; level <= b.cfg.MaxLevels; level++ {
		// k = ceil(len / branching). Used for K-means; Leiden
		// ignores k and lets graph topology determine cluster
		// count. We still compute it because the > 0 guard below
		// is the only sensible "we have something to cluster"
		// check shared between the two algorithms.
		k := (len(current) + b.cfg.BranchingFactor - 1) / b.cfg.BranchingFactor
		if k <= 0 {
			break
		}
		seed := levelSeed(p.KbID, p.FileID, level)
		var (
			clusters []Cluster
			cErr     error
		)
		switch b.cfg.ClusteringAlgorithm {
		case "leiden":
			clusters, cErr = ClusterByLeiden(current, LeidenConfig{
				Resolution: b.cfg.LeidenResolution,
				Seed:       seed,
			})
		default:
			clusters, cErr = ClusterByKMeans(current, k, seed)
		}
		if cErr != nil || len(clusters) == 0 {
			logctx.From(ctx).Warn("raptor.build.cluster_failed",
				"fileId", p.FileID, "level", level, "err", cErr,
				"algorithm", b.cfg.ClusteringAlgorithm)
			break
		}

		levelSummaries, levelCalls := b.summariseLevel(ctx, p, level, clusters, contentByID)
		stats.LLMCalls += levelCalls
		stats.SummariesAdded += len(levelSummaries)
		stats.LevelsBuilt = level

		logctx.From(ctx).Info("raptor.build.level",
			"fileId", p.FileID,
			"level", level,
			"cluster_count", len(clusters),
			"summary_count", len(levelSummaries),
			"llm_calls", levelCalls,
		)

		if len(levelSummaries) == 0 {
			// Every cluster failed at this level — the rest of the
			// tree has nothing to ascend onto.
			break
		}

		// The next level operates on summaries.
		next := make([]ClusterInput, 0, len(levelSummaries))
		for _, s := range levelSummaries {
			next = append(next, ClusterInput{ID: s.id, Embedding: s.embedding})
			contentByID[s.id] = s.content
		}
		if len(next) <= 1 {
			break
		}
		current = next
	}

	stats.DurationMs = time.Since(start).Milliseconds()
	observability.RecordRaptorBuild("built")
	observability.ObserveRaptorBuildSeconds(time.Since(start).Seconds())
	observability.ObserveRaptorTreeDepth(stats.LevelsBuilt)
	logctx.From(ctx).Info("raptor.build.end",
		"fileId", p.FileID,
		"levels_built", stats.LevelsBuilt,
		"summaries_total", stats.SummariesAdded,
		"llm_calls", stats.LLMCalls,
		"duration_ms", stats.DurationMs,
	)
	return stats, nil
}

// insertedSummary captures the per-cluster output of summariseLevel
// so the caller can build the next level's ClusterInput slice.
type insertedSummary struct {
	id        string
	content   string
	embedding []float64
}

// summariseLevel runs summary LLM + embed + insert for each cluster
// in one level with bounded fan-out. Per-cluster errors are logged
// + dropped — they reduce the level's branching but never fail the
// whole build. Returns the successful summaries plus the LLM-call
// count (for stats), in cluster order with failures omitted.
func (b *Builder) summariseLevel(
	ctx context.Context, p BuildParams, level int,
	clusters []Cluster, contentByID map[string]string,
) ([]insertedSummary, int) {
	results := make([]insertedSummary, len(clusters))
	errs := make([]error, len(clusters))
	var calls atomic.Int64

	sem := make(chan struct{}, b.cfg.Concurrency)
	var wg sync.WaitGroup

summarise:
	for i, c := range clusters {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			// Labeled break required: an unlabeled `break` would only exit
			// the select, leaving the for loop to wg.Add(1) and spawn a
			// goroutine whose `defer <-sem` would steal a slot it never
			// acquired — corrupting the semaphore counter.
			break summarise
		}
		wg.Add(1)
		go func(idx int, cluster Cluster) {
			defer wg.Done()
			defer func() { <-sem }()
			defer safego.RecoverError(&errs[idx])

			concat := joinAndMaybeTruncate(cluster.Members, contentByID, b.cfg.SummaryInputTokenCap)
			summary, sErr := b.summariser.Summarise(ctx, p.KbID, b.cfg.SummaryModel, p.FileName, level, concat)

			calls.Add(1)

			if sErr != nil {
				observability.RecordRaptorLLMCall("error")
				errs[idx] = sErr
				return
			}
			observability.RecordRaptorLLMCall("ok")

			emb, eErr := b.embedder.EmbedOne(ctx, p.KbID, summary)
			if eErr != nil {
				errs[idx] = eErr
				return
			}

			childIDs := make([]string, len(cluster.Members))
			for j, m := range cluster.Members {
				childIDs[j] = m.ID
			}
			id, iErr := b.store.InsertSummary(ctx, p.KbID, p.FileID, p.PgConfig, p.Dimensions, SummaryRow{
				Content:   summary,
				Embedding: emb,
				TreeLevel: level,
				ChildIDs:  childIDs,
			})
			if iErr != nil {
				errs[idx] = iErr
				return
			}
			results[idx] = insertedSummary{id: id, content: summary, embedding: emb}
		}(i, c)
	}
	wg.Wait()

	out := make([]insertedSummary, 0, len(results))
	for i, r := range results {
		if errs[i] != nil {
			logctx.From(ctx).Warn("raptor.build.cluster_dropped",
				"fileId", p.FileID, "level", level, "cluster_index", i, "err", errs[i])
			continue
		}
		if r.id == "" {
			continue
		}
		out = append(out, r)
	}
	return out, int(calls.Load())
}

// joinAndMaybeTruncate concatenates the cluster's member content
// with "---" separators. If the joined length exceeds tokenCap*4
// bytes (a rough 4-chars-per-token bound — calling tiktoken per
// cluster would be too heavy and we only need a safety bound, not
// quality control), keep the head + a truncation marker + the tail.
// The middle is the easiest to lose without hurting summary
// faithfulness — the LLM uses the framing of the cluster to derive
// the topic.
func joinAndMaybeTruncate(members []ClusterInput, contentByID map[string]string, tokenCap int) string {
	var b strings.Builder
	for i, m := range members {
		if i > 0 {
			b.WriteString("\n\n---\n\n")
		}
		b.WriteString(contentByID[m.ID])
	}
	s := b.String()
	maxBytes := tokenCap * 4
	if len(s) <= maxBytes {
		return s
	}
	half := maxBytes / 2
	return s[:half] + "\n\n…[middle truncated]…\n\n" + s[len(s)-half:]
}

// levelSeed hashes kb_id + file_id + level into a 64-bit seed so
// re-ingesting the same file produces the same tree shape. Each
// level varies so adjacent levels don't degenerate into the same
// partition.
func levelSeed(kbID, fileID string, level int) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(kbID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(fileID))
	_, _ = h.Write([]byte{0})
	var lvl [8]byte
	for i := 0; i < 8; i++ {
		lvl[i] = byte(level >> (8 * i))
	}
	_, _ = h.Write(lvl[:])
	return int64(h.Sum64())
}
