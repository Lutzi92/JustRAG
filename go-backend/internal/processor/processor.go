// Package processor orchestrates the file processing pipeline: parsing,
// splitting, embedding, and storing chunks in the vector store.
package processor

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/chat"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/parser"
	"github.com/justrag/go-backend/internal/pgxutil"
	"github.com/justrag/go-backend/internal/processor/raptor"
	"github.com/justrag/go-backend/internal/safego"
	"github.com/justrag/go-backend/internal/splitter"
	"github.com/justrag/go-backend/internal/tabular"
	"github.com/justrag/go-backend/internal/vector"
)

// HashLookup is the minimum interface dedupBatch needs from a chunk store.
type HashLookup interface {
	GetExistingChunkHashes(ctx context.Context, kbID string, dimensions int, hashes []string) (map[string]struct{}, error)
}

// reingestCleaner removes a file's existing chunks before (re-)ingestion so
// that an Asynq retry of a partially-failed ProcessFile attempt replaces
// rather than duplicates rows. *vector.ChunkService satisfies it.
type reingestCleaner interface {
	DeleteChunksByFileIDAllDims(ctx context.Context, fileID string) error
	DeleteParentChunksByFileID(ctx context.Context, fileID string) error
}

// dedupResult holds the indices of survivor chunks in the original batch
// (those that should be embedded + stored), the parallel hashes, and a count
// of how many were dropped.
//
// allHashes is set ONLY on the cross-file lookup error path, and contains
// the texts-aligned hash slice (one entry per input text, in input order)
// so callers that want to fall back to "embed everything" can do so without
// recomputing SHA-256 over every chunk again.
type dedupResult struct {
	survivorIdx  []int
	hashes       []string
	allHashes    []string
	droppedCount int
}

// dedupBatch performs in-batch + cross-file deduplication for one embedding
// batch. Returns the indices of chunks that should actually be embedded.
//
// chunkSvc may be nil — the function then performs only in-batch dedup.
// dimensions is passed to chunkSvc.GetExistingChunkHashes (the dedup column
// currently exists only on the default 1536-dim chunk table; non-default
// dimensions silently skip cross-file dedup until operators apply migration
// 0007 to their dimension-specific tables).
//
// On a cross-file lookup error, the returned dedupResult still contains the
// in-batch-dedup survivors and their hashes alongside the error. Callers
// that want to fall back to "embed everything" on lookup failure can use
// the parallel `texts`-aligned hash slice via dedupAllHashes, but if the
// in-batch-dedup result is acceptable (it always is — it's a strict subset
// of "embed everything") they can use the partial result and avoid a
// duplicate SHA-256 pass.
func dedupBatch(ctx context.Context, chunkSvc HashLookup, kbID string, dimensions int, texts []string) (dedupResult, error) {
	res := dedupResult{}
	if len(texts) == 0 {
		return res, nil
	}

	hashes := make([]string, len(texts))
	for i, t := range texts {
		hashes[i] = vector.HashContent(t)
	}

	// In-batch dedup: keep first occurrence per non-empty hash.
	seenInBatch := make(map[string]struct{})
	survivors := make([]int, 0, len(texts))
	for i, h := range hashes {
		if h == "" {
			survivors = append(survivors, i)
			continue
		}
		if _, dup := seenInBatch[h]; dup {
			res.droppedCount++
			continue
		}
		seenInBatch[h] = struct{}{}
		survivors = append(survivors, i)
	}

	// Cross-file dedup: query DB for hashes already present.
	if chunkSvc != nil {
		lookup := make([]string, 0, len(survivors))
		for _, idx := range survivors {
			if hashes[idx] != "" {
				lookup = append(lookup, hashes[idx])
			}
		}
		existing, err := chunkSvc.GetExistingChunkHashes(ctx, kbID, dimensions, lookup)
		if err != nil {
			// Surface the partial in-batch-dedup result so the caller can
			// fall back without recomputing every hash. We hand back the
			// raw (texts-aligned) hash slice via a separate field so the
			// caller can reconstruct an "embed everything" result without
			// looping vector.HashContent again.
			res.survivorIdx = survivors
			res.hashes = make([]string, len(survivors))
			for i, idx := range survivors {
				res.hashes[i] = hashes[idx]
			}
			res.allHashes = hashes
			return res, fmt.Errorf("dedup lookup: %w", err)
		}
		filtered := survivors[:0]
		for _, idx := range survivors {
			if h := hashes[idx]; h != "" {
				if _, dup := existing[h]; dup {
					res.droppedCount++
					continue
				}
			}
			filtered = append(filtered, idx)
		}
		survivors = filtered
	}

	res.survivorIdx = survivors
	res.hashes = make([]string, len(survivors))
	for i, idx := range survivors {
		res.hashes[i] = hashes[idx]
	}
	return res, nil
}

// ProcessorStore defines the persistence operations required by Processor.
type ProcessorStore interface {
	UpdateFileStatus(ctx context.Context, fileID, status string) error
	UpdateFileProgress(ctx context.Context, fileID string, progress int) error
}

// SiteConfigReader reads individual site config values.
type SiteConfigReader interface {
	GetSiteConfigValue(ctx context.Context, key string) (*string, error)
}

// Processor orchestrates parsing → splitting → embedding → storing.
type Processor struct {
	factory          *parser.Factory
	aiResolver       *ai.ConfigResolver
	chunkSvc         *vector.ChunkService
	store            ProcessorStore
	embeddingCache   *ai.EmbeddingCache
	siteConfigReader SiteConfigReader
	// cleaner is the injection seam for idempotent re-ingestion. It is set
	// from chunkSvc in NewProcessor (nil-safe: only assigned when chunkSvc
	// is non-nil to avoid the nil-pointer-in-interface trap). Tests may
	// assign a fake to verify cleanup fires before the ingest stages.
	cleaner reingestCleaner
	// mainDB is the application Postgres pool, used to look up the KB's
	// language so the BM25 tsvector can be built with a matching
	// regconfig (e.g. "german"). Optional — when nil, inserts fall back
	// to the "simple" text-search config.
	mainDB *pgxpool.Pool
	// raptorBuilder is the Phase F injection seam. nil → ProcessFile
	// constructs a real builder via NewRaptorBuilder when raptor_enabled
	// is on. Tests assign a fake to this field to verify the hook
	// fires under the right conditions without touching the LLM /
	// vector store.
	raptorBuilder raptor.BuilderInterface
	// materializer, when set AND chat_tabular_query_enabled is on, diverts
	// spreadsheet files into the structured tabular store and replaces the
	// embedded body with a per-sheet summary card. nil disables the path.
	materializer *tabular.Materializer
	// hype is the HyPE question store, backed by the vector pool. nil when
	// the vector pool was not injected (e.g. server-side processor, tests).
	// runHyPEGenerationStage guards on non-nil.
	hype *vector.HyPEStore
}

// indexedChunk pairs a chunk's text with its source page number.
type indexedChunk struct {
	Text string
	Page int // 0 = no page info
}

const terminalStatusTimeout = 5 * time.Second

// NewProcessor creates a Processor with all required dependencies. An optional
// EmbeddingCache avoids redundant embedding API calls for identical chunks.
func NewProcessor(factory *parser.Factory, aiResolver *ai.ConfigResolver, chunkSvc *vector.ChunkService, store ProcessorStore, cache ...*ai.EmbeddingCache) *Processor {
	var ec *ai.EmbeddingCache
	if len(cache) > 0 {
		ec = cache[0]
	}
	p := &Processor{
		factory:        factory,
		aiResolver:     aiResolver,
		chunkSvc:       chunkSvc,
		store:          store,
		embeddingCache: ec,
	}
	// Assign cleaner only when chunkSvc is non-nil. Assigning a nil
	// *vector.ChunkService to the reingestCleaner interface would create a
	// non-nil interface holding a nil pointer, which would panic on any
	// method call; the if-guard prevents that.
	if chunkSvc != nil {
		p.cleaner = chunkSvc
	}
	return p
}

// SetSiteConfigReader attaches a SiteConfigReader so the processor can check
// whether contextual chunk enrichment is enabled.
func (p *Processor) SetSiteConfigReader(r SiteConfigReader) {
	p.siteConfigReader = r
}

// SetMainDB attaches the application Postgres pool used for KB-language
// lookup during ingestion. Without it the processor cannot tailor the BM25
// regconfig to the KB and falls back to "simple".
func (p *Processor) SetMainDB(pool *pgxpool.Pool) {
	p.mainDB = pool
}

// SetMaterializer attaches the tabular materializer (worker-side wiring).
func (p *Processor) SetMaterializer(m *tabular.Materializer) { p.materializer = m }

// SetVectorPool attaches the vector Postgres pool used by the HyPE generation
// stage. Without it, HyPE ingest is silently skipped even when the feature
// flag is on. Call this on the worker side alongside SetMainDB.
func (p *Processor) SetVectorPool(pool *pgxpool.Pool) {
	p.hype = vector.NewHyPEStore(pool)
}

// resolveKBLanguages does a single SELECT and returns both forms of the KB
// language that the ingest pipeline needs: the raw two-letter code
// ("de"/"en"/...) used by the KG-extraction prompt branch, and the
// Postgres text-search regconfig ("german"/"english"/"simple") used by the
// BM25 tsvector at INSERT time. Combined to avoid two round-trips per file
// — the old code path called resolveKBLanguage and a separate raw-form
// helper during ProcessFile + runKGExtractionStage. Falls back to
// ("en", "simple") on missing pool / lookup failure / empty row, matching
// the per-form helpers' historical defaults.
func (p *Processor) resolveKBLanguages(ctx context.Context, kbID string) (rawLang, pgConfig string) {
	const rawFallback, pgFallback = "en", "simple"
	if p.mainDB == nil {
		return rawFallback, pgFallback
	}
	type langRow struct {
		Language string `db:"language"`
	}
	row, err := pgxutil.QueryOne[langRow](ctx, p.mainDB, `SELECT language FROM knowledge_bases WHERE id = $1`, kbID)
	if err != nil {
		logctx.From(ctx).Warn("processor: kb language lookup failed; falling back to defaults",
			"kbId", kbID,
			"error", err)
		return rawFallback, pgFallback
	}
	if row == nil || row.Language == "" {
		return rawFallback, pgFallback
	}
	return row.Language, vector.PgTextSearchConfig(row.Language)
}

// resolveKBLanguage returns the Postgres text-search regconfig for the KB
// (e.g. "german", "english"). Falls back to "simple" if the language can't
// be resolved or no main DB pool is wired up — keeps ingestion working even
// in test setups that don't exercise the language-aware path.
func (p *Processor) resolveKBLanguage(ctx context.Context, kbID string) string {
	_, pgConfig := p.resolveKBLanguages(ctx, kbID)
	return pgConfig
}

// resolveEnrichmentEnabled reports whether contextual enrichment should run
// for ingestion. Defaults to true (enabled); only an explicit "false" or "0"
// in site config disables it.
func resolveEnrichmentEnabled(ctx context.Context, reader SiteConfigReader) bool {
	if reader == nil {
		return true
	}
	val, err := reader.GetSiteConfigValue(ctx, "contextual_enrichment")
	if err != nil {
		logctx.From(ctx).Warn("processor: failed to read site config",
			"key", "contextual_enrichment",
			"error", err)
		return true
	}
	if val == nil {
		return true
	}
	switch *val {
	case "false", "0":
		return false
	default:
		return true
	}
}

// resolveTabularEnabled reports whether the spreadsheet materializer should
// run. Default false (opt-in), mirroring chat_tabular_query_enabled.
func resolveTabularEnabled(ctx context.Context, reader SiteConfigReader) bool {
	if reader == nil {
		return false
	}
	val, err := reader.GetSiteConfigValue(ctx, "chat_tabular_query_enabled")
	if err != nil || val == nil {
		return false
	}
	return *val == "true" || *val == "1"
}

// resolveTabularSemanticOptions reads the Phase-2 free-text-embedding config.
// Enabled defaults false; thresholds fall back to 32 chars / 0.6 distinct ratio.
func resolveTabularSemanticOptions(ctx context.Context, reader SiteConfigReader) tabular.SemanticOptions {
	opts := tabular.SemanticOptions{MinAvgLen: 32, MinDistinctRatio: 0.6}
	if reader == nil {
		return opts
	}
	if v, err := reader.GetSiteConfigValue(ctx, "chat_tabular_semantic_columns_enabled"); err == nil && v != nil {
		opts.Enabled = *v == "true" || *v == "1"
	}
	if v, err := reader.GetSiteConfigValue(ctx, "tabular_semantic_min_avg_len"); err == nil && v != nil {
		if n, perr := strconv.Atoi(strings.TrimSpace(*v)); perr == nil {
			opts.MinAvgLen = n
		}
	}
	if v, err := reader.GetSiteConfigValue(ctx, "tabular_semantic_min_distinct_ratio"); err == nil && v != nil {
		if f, perr := strconv.ParseFloat(strings.TrimSpace(*v), 64); perr == nil {
			opts.MinDistinctRatio = f
		}
	}
	return opts
}

// runTabularMaterializer materializes every sheet and returns the combined
// per-sheet summary card (the only text embedded for this file).
func (p *Processor) runTabularMaterializer(ctx context.Context, filePath, fileName, fileID, kbID, pgConfig string) (string, error) {
	opts := resolveTabularSemanticOptions(ctx, p.siteConfigReader)
	res, err := p.materializer.Materialize(ctx, filePath, fileName, fileID, kbID, opts)
	if err != nil {
		return "", err
	}
	if opts.Enabled {
		if err := p.embedTabularRowChunks(ctx, fileID, kbID, res.Sheets, pgConfig); err != nil {
			// Best-effort: structured store + summary card already committed; the
			// fuzzy index is the only thing missing. Log and continue.
			logctx.From(ctx).Warn("processor: tabular row-chunk embedding failed; fuzzy search unavailable for this file",
				"fileId", fileID, "error", err)
		}
	}
	var cards []string
	for _, s := range res.Sheets {
		cards = append(cards, tabular.BuildSummaryCard(fileName, s.SheetName, s.TableName, s.Columns, s.RowCount))
	}
	return strings.Join(cards, "\n\n"), nil
}

// embedTabularRowChunks embeds the Phase-2 free-text row-chunks produced by the
// materializer and stores them in the dim-keyed chunk table. Each chunk's
// Content carries the `[tabular.<table> row <id>]` source header (built by
// tabular.BuildRowChunkContent) so the agent can pivot to table_query; Metadata
// records the table + rowid for a future cleaner-surfacing path. Reuses the
// standard embedding batch size + cache. file_id ties the chunks to the file so
// cascade-delete / re-ingest clean them up with no extra code.
func (p *Processor) embedTabularRowChunks(ctx context.Context, fileID, kbID string, sheets []tabular.SheetResult, pgConfig string) error {
	const embeddingBatchSize = 20
	type pending struct {
		content string
		table   string
		rowID   int64
	}
	var all []pending
	for _, s := range sheets {
		for _, rc := range s.RowChunks {
			all = append(all, pending{content: rc.Text, table: s.TableName, rowID: rc.RowID})
		}
	}
	if len(all) == 0 {
		return nil
	}
	dimensions := 0
	for _, batch := range batches(all, embeddingBatchSize) {
		if ctx.Err() != nil {
			return fmt.Errorf("processor: ctx cancelled during tabular row-chunk embed: %w", ctx.Err())
		}
		texts := make([]string, len(batch))
		for i, pc := range batch {
			texts[i] = pc.content
		}
		embeddings, err := ai.GenerateEmbeddings(ctx, p.aiResolver, texts, kbID, p.embeddingCache)
		if err != nil {
			return fmt.Errorf("processor: embed tabular row-chunks: %w", err)
		}
		if dimensions == 0 && len(embeddings) > 0 {
			dimensions = len(embeddings[0])
		}
		inputs := make([]vector.ChunkInput, len(batch))
		for i, pc := range batch {
			inputs[i] = vector.ChunkInput{
				KbID:        kbID,
				FileID:      fileID,
				Content:     pc.content,
				ContentHash: vector.HashContent(pc.content),
				Embedding:   embeddings[i],
				Metadata: map[string]any{
					"tabular_table": pc.table,
					"tabular_rowid": pc.rowID,
				},
			}
		}
		if err := p.chunkSvc.AddDocumentChunks(ctx, fileID, inputs, dimensions, pgConfig); err != nil {
			return fmt.Errorf("processor: insert tabular row-chunks: %w", err)
		}
	}
	logctx.From(ctx).Info("processor: tabular row-chunks embedded", "fileId", fileID, "chunks", len(all))
	return nil
}

// resolveKGExtractionEnabled gates the AP-C1 knowledge-graph
// extraction stage. Default off — KG extraction adds one LLM call
// per chunk, which the plan documents as a 3-5x ingestion cost
// multiplier. Provider auto-caching of the document prefix takes
// most of that bite back, but operators should still flip this
// deliberately and watch the kb_id-scoped ingestion latency.
func resolveKGExtractionEnabled(ctx context.Context, reader SiteConfigReader) bool {
	if reader == nil {
		return false
	}
	val, err := reader.GetSiteConfigValue(ctx, "kg_extraction_enabled")
	if err != nil || val == nil {
		return false
	}
	switch *val {
	case "true", "1":
		return true
	default:
		return false
	}
}

// resolveKGExtractionModel returns the model override used by the
// KG extractor. Empty falls back to the KB's default chat model.
// Resolves through the P7 fast-tier chain: per-task
// `kg_extraction_model` → `model_tier_fast` → empty. Operators
// usually point this at a small fast model — extraction is
// structured-output, not reasoning, so the small models do well.
func resolveKGExtractionModel(ctx context.Context, reader SiteConfigReader) string {
	return chat.ResolveFastTierModel(ctx, reader, "kg_extraction_model")
}

// resolveEnrichmentModel returns the model override used for
// chunk-context generation during ingestion. Empty string means
// "use the KB's default chat model" (handled inside
// ai.GenerateChunkContext). Resolves through the P7 fast-tier
// chain: per-task `contextual_enrichment_model` →
// `model_tier_fast` → empty.
func resolveEnrichmentModel(ctx context.Context, reader SiteConfigReader) string {
	return chat.ResolveFastTierModel(ctx, reader, "contextual_enrichment_model")
}

// resolveHyPEEnabled reports whether the HyPE ingest stage is active.
// Reads the "hype_enabled" site_config key via the chat package helper.
func resolveHyPEEnabled(ctx context.Context, reader SiteConfigReader) bool {
	return chat.HyPEEnabled(ctx, reader)
}

// resolveHyPEModel returns the model override used by the HyPE question
// generator. Empty falls back to the KB's default chat model. Resolves
// through the P7 fast-tier chain: per-task `hype_model` →
// `model_tier_fast` → empty.
func resolveHyPEModel(ctx context.Context, reader SiteConfigReader) string {
	return chat.ResolveFastTierModel(ctx, reader, "hype_model")
}

// resolveLateChunkingEnabled gates Jina-style late chunking at ingest:
// the whole document's chunks are embedded in one call with
// `late_chunking: true` so each chunk vector carries cross-chunk
// document context. Default off — requires a provider that understands
// the flag (Jina-compatible). Other providers silently ignore the
// field and return standard embeddings.
func resolveLateChunkingEnabled(ctx context.Context, reader SiteConfigReader) bool {
	if reader == nil {
		return false
	}
	val, err := reader.GetSiteConfigValue(ctx, "late_chunking_enabled")
	if err != nil || val == nil {
		return false
	}
	switch *val {
	case "true", "1":
		return true
	default:
		return false
	}
}

// resolveLateChunkingMaxInputTokens caps the per-call token budget for
// the late-chunking embedding endpoint. Documents whose chunks exceed
// this cap are split into successive windows; no chunk is split
// across windows. Default 8192.
func resolveLateChunkingMaxInputTokens(ctx context.Context, reader SiteConfigReader) int {
	const def = 8192
	if reader == nil {
		return def
	}
	val, err := reader.GetSiteConfigValue(ctx, "late_chunking_max_input_tokens")
	if err != nil || val == nil || *val == "" {
		return def
	}
	n, err := strconv.Atoi(*val)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// ProcessFileInput bundles ProcessFile's positional arguments. The five string
// fields all carried positionally before — silently swapping fileID with
// filePath, fileName, mimeType, or kbID would compile and break ingestion
// halfway through. Naming them at call sites is the cheap fix.
type ProcessFileInput struct {
	FileID       string
	FilePath     string
	FileName     string
	MimeType     string
	KBID         string
	ChunkSize    int // 0 → splitter default (512)
	ChunkOverlap int // 0 → splitter default (100)
}

// ProcessFile runs the full pipeline for a single file:
// parse → split → embed (batches of 20) → store chunks → update progress/status.
// ChunkSize and ChunkOverlap of 0 use the splitter defaults (512 and 100).
func (p *Processor) ProcessFile(ctx context.Context, in ProcessFileInput) error {
	fileID := in.FileID
	filePath := in.FilePath
	fileName := in.FileName
	mimeType := in.MimeType
	kbID := in.KBID
	chunkSize := in.ChunkSize
	chunkOverlap := in.ChunkOverlap
	logctx.From(ctx).Info("processor: starting",
		"fileId", fileID,
		"fileName", fileName,
		"mimeType", mimeType,
		"kbId", kbID,
	)

	// Step 1: mark as processing with a small initial progress so the
	// frontend progress bar is immediately visible (0% is rendered as an
	// invisible zero-width bar).
	if err := p.store.UpdateFileStatus(ctx, fileID, "processing"); err != nil {
		return fmt.Errorf("processor: update status processing: %w", err)
	}
	_ = p.store.UpdateFileProgress(ctx, fileID, 5)

	// Idempotency guard: an Asynq retry re-runs this handler from the top.
	// Delete any chunks a previous (failed/partial) attempt wrote, else the
	// gen_random_uuid() ids mean the retry appends duplicates instead of
	// replacing. Mirrors the user-triggered re-ingest path. nil cleaner
	// (e.g. tests without a vector store) → skipped.
	if p.cleaner != nil {
		if err := p.cleaner.DeleteChunksByFileIDAllDims(ctx, fileID); err != nil {
			logctx.From(ctx).Warn("processor: pre-ingest chunk cleanup failed; continuing", "fileId", fileID, "error", err)
		}
		if err := p.cleaner.DeleteParentChunksByFileID(ctx, fileID); err != nil {
			logctx.From(ctx).Warn("processor: pre-ingest parent cleanup failed; continuing", "fileId", fileID, "error", err)
		}
	}

	// Step 2: select parser.
	par := p.factory.GetParser(mimeType, fileName)
	if par == nil {
		_ = p.store.UpdateFileStatus(ctx, fileID, "error")
		return fmt.Errorf("processor: no parser for mimeType=%s fileName=%s", mimeType, fileName)
	}

	// Step 3: parse file. For audio files this calls the STT transcriber
	// and can take minutes; progress stays at 5% during this phase so the
	// frontend still shows activity.
	result, err := par.Parse(ctx, parser.ParseContext{
		FilePath:  filePath,
		FileName:  fileName,
		MimeType:  mimeType,
		KbID:      kbID,
		ChunkSize: chunkSize,
	})
	if err != nil {
		_ = p.store.UpdateFileStatus(ctx, fileID, "error")
		return fmt.Errorf("processor: parse file: %w", err)
	}

	// Parsing done — bump progress so the bar visibly advances before
	// embedding begins (important for audio where parse is the long step).
	_ = p.store.UpdateFileProgress(ctx, fileID, 10)

	// Resolve the KB language once so the BM25 tsvector built at INSERT
	// time uses the correct regconfig (e.g. "german" stemming). The raw
	// form is also threaded down to runKGExtractionStage so a second DB
	// round-trip for the same row is avoided; the tabular divert below
	// reuses pgConfig too.
	rawLang, pgConfig := p.resolveKBLanguages(ctx, kbID)

	// Tabular divert: for spreadsheets when the materializer is wired and
	// chat_tabular_query_enabled is on, load sheets into native-typed Postgres
	// tables and replace the embedded body with a per-sheet summary card. On
	// failure, fall through to normal text ingestion (best-effort).
	if p.materializer != nil &&
		tabular.IsSpreadsheet(mimeType, fileName) &&
		resolveTabularEnabled(ctx, p.siteConfigReader) {
		if card, err := p.runTabularMaterializer(ctx, filePath, fileName, fileID, kbID, pgConfig); err != nil {
			logctx.From(ctx).Warn("processor: tabular materializer failed; falling back to text ingestion",
				"fileId", fileID, "error", err)
		} else if card != "" {
			// Divert: replace the spreadsheet body with the summary card so
			// only the card is embedded (raw rows live in the tabular store).
			result.Text = card
			result.Pages = nil
			result.IsMarkdown = true
		}
	}

	// Step 4: choose splitter config.
	var cfg splitter.Config
	lowerName := strings.ToLower(fileName)
	isMarkdown := strings.HasSuffix(lowerName, ".md") ||
		strings.HasSuffix(lowerName, ".markdown") ||
		strings.HasSuffix(lowerName, ".mdx") ||
		result.IsMarkdown
	if isMarkdown {
		cfg = splitter.MarkdownConfig()
	} else {
		cfg = splitter.DefaultConfig()
	}
	if chunkSize > 0 {
		cfg.ChunkSize = chunkSize
	}
	if chunkOverlap > 0 {
		cfg.ChunkOverlap = chunkOverlap
	}

	// Step 5: split text into chunks.
	// When the parser provides per-page text (e.g. PDF), split each page
	// independently so every chunk reliably knows which page it belongs
	// to. Otherwise split the full text as a single document.
	var ichunks []indexedChunk

	if len(result.Pages) > 0 {
		for _, pg := range result.Pages {
			pageChunks := splitter.Split(pg.Text, cfg)
			for _, c := range pageChunks {
				ichunks = append(ichunks, indexedChunk{Text: c, Page: pg.PageNumber})
			}
		}
	} else {
		for _, c := range splitter.Split(result.Text, cfg) {
			ichunks = append(ichunks, indexedChunk{Text: c, Page: 0})
		}
	}

	if len(ichunks) == 0 {
		_ = p.store.UpdateFileStatus(ctx, fileID, "completed")
		logctx.From(ctx).Info("processor: no chunks produced", "fileId", fileID)
		return nil
	}

	// Build a section index for markdown files so each chunk can carry
	// heading breadcrumbs (e.g. ["Chapter", "Section", "Subsection"]).
	var sectionIndex *SectionIndex
	sectionSearchOffset := 0
	if isMarkdown && result.Text != "" {
		sectionIndex = ExtractMarkdownSections(result.Text)
	}

	// Flatten for the batch loop.
	chunks := make([]string, len(ichunks))
	for i, ic := range ichunks {
		chunks[i] = ic.Text
	}

	// Check if contextual enrichment is enabled (once per file, not per batch).
	enrichmentEnabled := resolveEnrichmentEnabled(ctx, p.siteConfigReader)
	enrichmentModel := resolveEnrichmentModel(ctx, p.siteConfigReader)
	if enrichmentEnabled {
		// Probe the resolver once. On a fresh install with no AI provider
		// configured yet, every per-chunk enrichment call would otherwise
		// emit a WARN — degrade to a single INFO per file and skip the
		// enrichment phase. Other errors (transient DB, etc.) fall through
		// and let the per-chunk path surface them normally.
		if _, err := p.aiResolver.Resolve(ctx, kbID); errors.Is(err, ai.ErrNoActiveProvider) {
			logctx.From(ctx).Info("processor: contextual enrichment skipped — no active AI provider configured",
				"fileId", fileID)
			enrichmentEnabled = false
		} else {
			logctx.From(ctx).Info("processor: contextual enrichment enabled",
				"fileId", fileID,
				"modelOverride", enrichmentModel)
		}
	}

	// Phase 3 §D parent-child path. When the toggle is on, run the
	// alternate ingestion that produces parents + children with FK
	// linkage. Page-level metadata fidelity is a v1 limitation —
	// parent-child collapses per-page text into a single source string,
	// trading page-precision on parents for the embedding-precision
	// gain on children. Otherwise fall through to the existing flat
	// path below (bit-for-bit unchanged).
	if chat.ParentChildEnabled(ctx, p.siteConfigReader) {
		parentSize := chat.ParentChildParentChunkSize(ctx, p.siteConfigReader)
		childSize := chat.ParentChildChildChunkSize(ctx, p.siteConfigReader)

		parentCfg := cfg
		parentCfg.ChunkSize = parentSize
		childCfg := cfg
		childCfg.ChunkSize = childSize

		// Use the same source the flat path used: result.Text when the
		// parser didn't provide pages, otherwise the concatenated page
		// texts. v1 limitation: per-page links are lost on parents — see
		// docs/runbooks/parent-child-reingestion.md for context.
		var sourceText string
		if len(result.Pages) > 0 {
			var sb strings.Builder
			for _, pg := range result.Pages {
				sb.WriteString(pg.Text)
				sb.WriteString("\n\n")
			}
			sourceText = sb.String()
		} else {
			sourceText = result.Text
		}

		groups := splitter.ParentChildSplit(sourceText, parentCfg, childCfg)
		if len(groups) == 0 {
			_ = p.store.UpdateFileStatus(ctx, fileID, "completed")
			logctx.From(ctx).Info("processor: parent-child split produced 0 groups", "fileId", fileID)
			return nil
		}
		if err := p.runParentChildIngest(ctx, fileID, kbID, fileName, groups,
			enrichmentEnabled, enrichmentModel, pgConfig); err != nil {
			p.updateTerminalStatus(ctx, fileID, "error")
			return err
		}
		_ = p.store.UpdateFileStatus(ctx, fileID, "completed")
		_ = p.store.UpdateFileProgress(ctx, fileID, 100)
		return nil
	}

	// Phase L: late-chunking ingest path. When `late_chunking_enabled` is
	// on (and parent-child didn't take over), the document's chunks are
	// embedded as one contiguous run via Jina-style late chunking. The
	// flag is orthogonal to contextual_enrichment: enrichment still runs
	// when enabled and its prefix is stored on the row (so BM25 + chat-
	// time prompts still benefit), but the embedding input is the
	// natural chunk text, never `prefix + content`.
	if resolveLateChunkingEnabled(ctx, p.siteConfigReader) {
		maxInputTokens := resolveLateChunkingMaxInputTokens(ctx, p.siteConfigReader)
		logctx.From(ctx).Info("processor: late chunking enabled",
			"fileId", fileID,
			"chunks", len(chunks),
			"maxInputTokens", maxInputTokens,
			"enrichment", enrichmentEnabled,
		)
		if err := p.runLateChunkedIngest(ctx, fileID, kbID, fileName, chunks,
			ichunks, result.Text, sectionIndex, &sectionSearchOffset,
			enrichmentEnabled, enrichmentModel, maxInputTokens, pgConfig); err != nil {
			p.updateTerminalStatus(ctx, fileID, "error")
			return err
		}
		_ = p.store.UpdateFileStatus(ctx, fileID, "completed")
		return nil
	}

	// Step 6: process chunks — enrichment and embedding run as a pipeline.
	//
	// When enrichment is disabled, chunks are batched (20 at a time) and
	// embedded sequentially as before. When enabled, enrichment goroutines
	// feed enriched chunks into an embedding stage via a channel so the two
	// stages overlap: embedding can start as soon as a batch-worth of
	// enriched chunks is ready, while remaining chunks are still being
	// enriched.
	const embeddingBatchSize = 20
	const maxEnrichConcurrency = 10
	totalChunks := len(chunks)
	processed := 0
	failedBatches := 0
	totalBatches := 0

	// lastDimensions is set on every successful batch insert; the Phase
	// F RAPTOR hook below the chunk loop reads it to know which
	// dim-keyed chunk table to read leaves from. 0 means "nothing was
	// inserted" — the hook short-circuits.
	var lastDimensions int

	if !enrichmentEnabled {
		// Fast path — no enrichment, embed in simple batches.
		for _, batch := range batches(chunks, embeddingBatchSize) {
			if ctx.Err() != nil {
				p.updateTerminalStatus(ctx, fileID, "error")
				return fmt.Errorf("processor: context cancelled: %w", ctx.Err())
			}
			totalBatches++
			dim, failed := p.embedAndStore(ctx, batch, nil, ichunks, result.Text, sectionIndex, &sectionSearchOffset, processed, fileID, kbID, pgConfig)
			if failed {
				failedBatches++
			}
			if dim > 0 {
				lastDimensions = dim
			}
			processed += len(batch)
			progress := 10 + int(float64(processed)/float64(totalChunks)*90)
			_ = p.store.UpdateFileProgress(ctx, fileID, progress)
		}
	} else {
		// Pipelined path — enrichment feeds into embedding.
		type enrichedChunk struct {
			globalIdx int
			original  string
			prefix    string // LLM-generated context prefix; empty on enrichment failure
		}

		enrichedCh := make(chan enrichedChunk, embeddingBatchSize)

		// Producer: enrich all chunks with bounded concurrency.
		var enrichWg sync.WaitGroup
		sem := make(chan struct{}, maxEnrichConcurrency)

		// Anthropic-style Contextual Retrieval: every per-chunk LLM call
		// receives the FULL document as the cacheable prefix (truncated
		// inside ai.GenerateChunkContext to ~8k cl100k_base tokens). With
		// OpenAI-compatible automatic prompt caching the document prefix
		// is paid for once per file; subsequent chunks read it from the
		// cache. Hence no per-chunk surrounding-context construction here.
		document := result.Text

		// producerErr surfaces a panic in the spawn loop as a normal error
		// instead of crashing the worker. The defer order below is critical
		// (defers run LIFO):
		//   1. RecoverError runs first — captures the panic into producerErr.
		//   2. enrichWg.Wait() runs — drains any sub-goroutines that were
		//      already launched before the panic.
		//   3. close(enrichedCh) runs last — only after Wait() returns, so
		//      no live sub-goroutine can send on a closed channel.
		// The consumer's `for range enrichedCh` then exits cleanly and we
		// inspect producerErr below.
		//
		// producerErrCh carries panics that occur inside the per-chunk
		// enrichment sub-goroutines. Buffered size 1 so the first panic
		// records non-blockingly; subsequent panics drop on the floor (one
		// is enough to fail the file). After the consumer exits we drain it
		// into producerErr if no spawn-loop panic was already captured.
		var producerErr error
		producerErrCh := make(chan error, 1)
		// Intentional raw `go` (not safego.Go) — this goroutine's panic
		// path is "capture into producerErr so the outer pipeline fails
		// the file", not safego.Go's log-and-continue. The inner
		// safego.RecoverError defer does the capture. Wrapping with
		// safego.Go would still work (the inner defer consumes the panic
		// before safego.Go's outer recover sees it), but matching the
		// goroutine launcher to the failure contract makes the intent
		// clear at the call site. Do NOT drop the inner RecoverError —
		// without it a producer panic would crash the worker.
		go func() {
			defer close(enrichedCh)
			defer enrichWg.Wait()
			defer safego.RecoverError(&producerErr)

		spawn:
			for i, text := range chunks {
				if ctx.Err() != nil {
					// Stop spawning new work; defers still run Wait() then close.
					break
				}

				enrichWg.Add(1)
				// Acquire the concurrency slot with a ctx escape so a cancelled
				// task isn't held hostage by in-flight LLM calls (each occupied
				// slot can take a full enrichment timeout to free).
				select {
				case sem <- struct{}{}:
				case <-ctx.Done():
					// Undo the Add — no goroutine will be launched, so the
					// deferred Wait() must not be left waiting on it.
					enrichWg.Done()
					break spawn
				}
				go func(idx int, chunkText string) {
					defer enrichWg.Done()
					defer func() { <-sem }()
					// Capture any panic inside ai.GenerateChunkContext (or
					// the surrounding logic) and forward it via
					// producerErrCh so the outer pipeline fails the file
					// cleanly instead of crashing the worker process.
					var subErr error
					defer func() {
						if subErr != nil {
							select {
							case producerErrCh <- subErr:
							default:
							}
						}
					}()
					defer safego.RecoverError(&subErr)

					prefix := ""
					contextStr, err := ai.GenerateChunkContext(ctx, p.aiResolver, fileName, document, chunkText, kbID, enrichmentModel)
					if err != nil {
						logctx.From(ctx).Warn("processor: chunk enrichment failed, indexing original text only",
							"fileId", fileID,
							"chunkIndex", idx,
							"error", err,
						)
					} else {
						prefix = contextStr
					}
					// If the consumer has already exited (ctx cancelled), the
					// send would block forever. Bail out on ctx.Done() so
					// Wait() can complete and the channel can be closed.
					select {
					case enrichedCh <- enrichedChunk{globalIdx: idx, original: chunkText, prefix: prefix}:
					case <-ctx.Done():
					}
				}(i, text)
			}
		}()

		// Consumer: collect enriched chunks and embed in batches.
		// Because enrichment goroutines finish out of order, we collect
		// into a map and flush in-order batches.
		pending := make(map[int]enrichedChunk)
		nextFlush := 0 // next globalIdx to flush

		for ec := range enrichedCh {
			// Mirror the non-enrichment path: honour ctx cancellation before
			// spending any more embedding budget on a torn-down job.
			if ctx.Err() != nil {
				p.updateTerminalStatus(ctx, fileID, "error")
				return fmt.Errorf("processor: context cancelled: %w", ctx.Err())
			}
			pending[ec.globalIdx] = ec

			// Flush complete batches in order.
			for len(pending) >= embeddingBatchSize || (nextFlush+len(pending) >= totalChunks && len(pending) > 0) {
				// Check if we have a contiguous run starting at nextFlush.
				batchEnd := nextFlush + embeddingBatchSize
				if batchEnd > totalChunks {
					batchEnd = totalChunks
				}
				ready := true
				for j := nextFlush; j < batchEnd; j++ {
					if _, ok := pending[j]; !ok {
						ready = false
						break
					}
				}
				if !ready {
					break
				}

				// We have a full contiguous batch — flush it.
				batchLen := batchEnd - nextFlush
				batchOriginals := make([]string, batchLen)
				batchPrefixes := make([]string, batchLen)
				for j := 0; j < batchLen; j++ {
					ec := pending[nextFlush+j]
					batchOriginals[j] = ec.original
					batchPrefixes[j] = ec.prefix
					delete(pending, nextFlush+j)
				}

				totalBatches++
				dim, failed := p.embedAndStore(ctx, batchOriginals, batchPrefixes, ichunks, result.Text, sectionIndex, &sectionSearchOffset, nextFlush, fileID, kbID, pgConfig)
				if failed {
					failedBatches++
				}
				if dim > 0 {
					lastDimensions = dim
				}
				processed += batchLen
				progress := 10 + int(float64(processed)/float64(totalChunks)*90)
				_ = p.store.UpdateFileProgress(ctx, fileID, progress)
				nextFlush = batchEnd
			}
		}

		// The producer's RecoverError defer runs before Wait()+close, so by
		// the time we exit the for-range, producerErr is fully written. If
		// the spawn loop didn't panic but a per-chunk sub-goroutine did,
		// pull the first such panic out of producerErrCh.
		if producerErr == nil {
			select {
			case producerErr = <-producerErrCh:
			default:
			}
		}
		if producerErr != nil {
			p.updateTerminalStatus(ctx, fileID, "error")
			return fmt.Errorf("processor: enrichment producer panicked: %w", producerErr)
		}
	}

	// Step 7: set final status.
	var finalStatus string
	switch {
	case failedBatches == 0:
		finalStatus = "completed"
	case failedBatches == totalBatches:
		finalStatus = "error"
	default:
		finalStatus = "partial"
	}

	if err := p.store.UpdateFileStatus(ctx, fileID, finalStatus); err != nil {
		return fmt.Errorf("processor: update final status: %w", err)
	}

	logctx.From(ctx).Info("processor: finished",
		"fileId", fileID,
		"status", finalStatus,
		"totalChunks", totalChunks,
		"failedBatches", failedBatches,
	)

	if finalStatus == "error" {
		return fmt.Errorf("processor: all embedding batches failed for file %s", fileID)
	}

	// AP-C1: knowledge-graph extraction. Best-effort; runs only when
	// the gate is on AND ingestion at least partially succeeded. Errors
	// log and drop — KG is a side-channel, never reverts the file's
	// completed/partial status.
	if resolveKGExtractionEnabled(ctx, p.siteConfigReader) {
		kgErr := p.runKGExtractionStage(ctx, fileID, kbID, fileName, result.Text, rawLang)
		if kgErr != nil {
			logctx.From(ctx).Warn("processor: kg extraction stage failed",
				"fileId", fileID, "error", kgErr)
		}
	}

	// HyPE: generate + embed hypothetical questions per chunk. Best-effort,
	// post-ingest, gated independently from KG. Re-ingest is the only backfill.
	if resolveHyPEEnabled(ctx, p.siteConfigReader) {
		if hErr := p.runHyPEGenerationStage(ctx, fileID, kbID, fileName, result.Text, rawLang); hErr != nil {
			logctx.From(ctx).Warn("processor: hype generation stage failed",
				"fileId", fileID, "error", hErr)
		}
	}

	// Phase F: RAPTOR summary-tree build. Best-effort, post-ingest,
	// gated independently from KG. Mutually exclusive with parent-child
	// (the two stripe document_chunks differently and re-running RAPTOR
	// over parent-child children would feed structural rows back as
	// "leaves").
	if chat.RaptorEnabled(ctx, p.siteConfigReader) {
		if chat.ParentChildEnabled(ctx, p.siteConfigReader) {
			observability.RecordRaptorBuild("skipped_parent_child")
			logctx.From(ctx).Info("raptor.build.skipped",
				"fileId", fileID, "reason", "parent_child_enabled")
		} else if lastDimensions > 0 {
			p.runRaptorBuildStage(ctx, fileID, kbID, fileName, lastDimensions, pgConfig)
		}
	}

	return nil
}

// runRaptorBuildStage is a thin wrapper around the raptor builder.
// Factored out of ProcessFile so the hook stays a single condition.
// Errors are logged + counted but never propagated — RAPTOR is a
// recall-boost side-channel; an LLM outage during summary generation
// must not flip a successfully-ingested file into an error state.
func (p *Processor) runRaptorBuildStage(ctx context.Context, fileID, kbID, fileName string, dimensions int, pgConfig string) {
	cfg := raptor.Config{
		MinChunks:           chat.RaptorMinChunks(ctx, p.siteConfigReader),
		MaxLevels:           chat.RaptorMaxLevels(ctx, p.siteConfigReader),
		BranchingFactor:     chat.RaptorBranchingFactor(ctx, p.siteConfigReader),
		SummaryModel:        chat.RaptorSummaryModel(ctx, p.siteConfigReader),
		ClusteringAlgorithm: chat.RaptorClusteringAlgorithm(ctx, p.siteConfigReader),
		LeidenResolution:    chat.RaptorLeidenResolution(ctx, p.siteConfigReader),
	}
	var b raptor.BuilderInterface = p.raptorBuilder
	if b == nil {
		b = raptor.NewBuilder(
			&raptorStoreAdapter{svc: p.chunkSvc},
			&raptorEmbedderAdapter{resolver: p.aiResolver, cache: p.embeddingCache},
			&raptorSummariserAdapter{resolver: p.aiResolver},
			cfg,
		)
	}
	if _, err := b.Build(ctx, raptor.BuildParams{
		KbID:       kbID,
		FileID:     fileID,
		FileName:   fileName,
		Dimensions: dimensions,
		PgConfig:   pgConfig,
	}); err != nil {
		logctx.From(ctx).Warn("processor: raptor build failed",
			"fileId", fileID, "error", err)
		observability.RecordRaptorBuild("failed")
	}
}

// runKGExtractionStage is the AP-C1 post-ingestion pass: read the
// chunks we just inserted, run the LLM extractor per chunk with the
// full document body as a stable prefix (provider auto-cache reuses
// it), persist entities + edges into kg_entities + kg_edges.
//
// Best-effort: per-chunk LLM failures log and skip; partial state is
// fine because the storage layer is idempotent (entities dedupe on
// (canonical_name, type), edges append). The whole stage runs
// sequentially per chunk — parallelizing here would be a Phase-D
// optimization once the eval gate has measured the baseline.
//
// Dimensions resolution: probe each known dim table for any row of
// this fileID. The first hit wins. Cheap because ListChunkTableDimensions
// returns 1-2 entries in practice.
func (p *Processor) runKGExtractionStage(ctx context.Context, fileID, kbID, fileName, document, lang string) error {
	if p.mainDB == nil || p.chunkSvc == nil {
		return fmt.Errorf("kg extraction: missing dependencies (mainDB / chunkSvc)")
	}
	dims, err := p.chunkSvc.ListChunkTableDimensions(ctx)
	if err != nil {
		return fmt.Errorf("kg extraction: list dims: %w", err)
	}
	var chunks []vector.FileChunkRow
	for _, d := range dims {
		rows, err := p.chunkSvc.GetChunksByFileID(ctx, kbID, fileID, d)
		if err != nil {
			// Non-existent table for this dim is logged but not
			// fatal — we keep probing.
			continue
		}
		if len(rows) > 0 {
			chunks = rows
			break
		}
	}
	if len(chunks) == 0 {
		// Either no chunks were inserted (failed batches), or the
		// file's chunks live in a dim table this loop didn't see.
		// Either way, KG extraction has nothing to do.
		return nil
	}
	model := resolveKGExtractionModel(ctx, p.siteConfigReader)
	// lang is the KB's raw two-letter code ("de"/"en") passed in by
	// ProcessFile — the KG prompt branches on those, and the regconfig
	// form ("german") would silently miss the de branch.
	store := newKGStore(p.mainDB)

	totalCreated, totalDeduped, totalEdges := 0, 0, 0
	for _, c := range chunks {
		if ctx.Err() != nil {
			break
		}
		ext, err := ai.ExtractKG(ctx, p.aiResolver, fileName, document, c.Content, kbID, lang, model)
		if err != nil {
			logctx.From(ctx).Warn("kg extraction: chunk extract failed; skipping chunk",
				"fileId", fileID, "chunkId", c.ID, "error", err)
			continue
		}
		created, deduped, edges, err := store.persistKGExtraction(ctx, kbID, c.ID, ext)
		if err != nil {
			logctx.From(ctx).Warn("kg extraction: persist failed; skipping chunk",
				"fileId", fileID, "chunkId", c.ID, "error", err)
			continue
		}
		totalCreated += created
		totalDeduped += deduped
		totalEdges += edges
	}
	logctx.From(ctx).Info("processor: kg extraction stage finished",
		"fileId", fileID,
		"chunks", len(chunks),
		"entities_created", totalCreated,
		"entities_deduped", totalDeduped,
		"edges", totalEdges,
	)
	return nil
}

// runHyPEGenerationStage generates + embeds hypothetical questions for
// each of the file's chunks and stores them in chunk_hype_questions_<dim>.
// Best-effort: per-chunk failures log and skip; never blocks ingestion.
// Re-ingest is the only backfill path. document is the full file text
// (cacheable prefix); lang is the KB's raw two-letter code.
func (p *Processor) runHyPEGenerationStage(ctx context.Context, fileID, kbID, fileName, document, lang string) error {
	if p.chunkSvc == nil || p.hype == nil {
		return fmt.Errorf("hype: missing dependencies (chunkSvc / hype)")
	}
	dims, err := p.chunkSvc.ListChunkTableDimensions(ctx)
	if err != nil {
		return fmt.Errorf("hype: list dims: %w", err)
	}
	var chunks []vector.FileChunkRow
	var chunkDim int
	for _, d := range dims {
		rows, err := p.chunkSvc.GetChunksByFileID(ctx, kbID, fileID, d)
		if err != nil {
			continue
		}
		if len(rows) > 0 {
			chunks = rows
			chunkDim = d
			break
		}
	}
	if len(chunks) == 0 {
		return nil
	}
	model := resolveHyPEModel(ctx, p.siteConfigReader)
	maxQ := chat.HyPEQuestionsPerChunk(ctx, p.siteConfigReader)

	totalQuestions := 0
	for _, c := range chunks {
		if ctx.Err() != nil {
			break
		}
		questions, err := ai.GenerateHypotheticalQuestions(ctx, p.aiResolver, fileName, document, c.Content, kbID, maxQ, lang, model)
		if err != nil {
			logctx.From(ctx).Warn("hype: question generation failed; skipping chunk",
				"fileId", fileID, "chunkId", c.ID, "error", err)
			continue
		}
		if len(questions) == 0 {
			continue
		}
		embs, err := ai.GenerateEmbeddings(ctx, p.aiResolver, questions, kbID, p.embeddingCache)
		if err != nil || len(embs) != len(questions) {
			logctx.From(ctx).Warn("hype: question embedding failed; skipping chunk",
				"fileId", fileID, "chunkId", c.ID, "error", err)
			continue
		}
		if err := p.hype.Insert(ctx, kbID, fileID, c.ID, questions, embs, chunkDim); err != nil {
			logctx.From(ctx).Warn("hype: insert failed; skipping chunk",
				"fileId", fileID, "chunkId", c.ID, "error", err)
			continue
		}
		totalQuestions += len(questions)
	}
	logctx.From(ctx).Info("processor: hype generation stage finished",
		"fileId", fileID, "chunks", len(chunks), "questions", totalQuestions)
	return nil
}

// runParentChildIngest is the Phase 3 §D ingestion path. Splits the
// document into parent groups, inserts parents into document_chunk_parents
// (so we have parent IDs), runs enrichment on PARENT-level content (one
// LLM call per parent — typically ~75% fewer calls than per-child), then
// embeds and inserts children with parent_chunk_id set to the
// corresponding parent row's id.
//
// On failure the partial parent + child state may leave orphans. The
// caller marks the file as error; orphan rows are removed by the next
// re-ingestion's DeleteParentChunksByFileID + DeleteChunksByFileID pair.
func (p *Processor) runParentChildIngest(
	ctx context.Context,
	fileID, kbID, fileName string,
	groups []splitter.ParentChildGroup,
	enrichmentEnabled bool,
	enrichmentModel string,
	pgConfig string,
) error {
	// Step A: insert parents WITHOUT contextual_prefix; collect IDs.
	parentInputs := make([]vector.ParentChunkInput, len(groups))
	for i, g := range groups {
		parentInputs[i] = vector.ParentChunkInput{
			KbID:    kbID,
			FileID:  fileID,
			Content: g.ParentText,
			Metadata: map[string]any{
				"parentIndex":  i,
				"totalParents": len(groups),
				"chunkKind":    "parent",
			},
		}
	}
	parentIDs, err := p.chunkSvc.AddParentChunks(ctx, parentInputs)
	if err != nil {
		return fmt.Errorf("processor: insert parents: %w", err)
	}
	if len(parentIDs) != len(groups) {
		return fmt.Errorf("processor: parent insert returned %d ids, expected %d", len(parentIDs), len(groups))
	}

	// Step B: enrichment runs on PARENT content (cheaper + more meaningful
	// prefix at parent granularity). Sequential — parent count is typically
	// << chunk count and the existing enrichment concurrency mechanism is
	// overkill here. The full document is sent each time so OpenAI-style
	// automatic prompt caching folds the document prefix across parents.
	//
	// Capture each parent's prefix into parentPrefixes so Step C can fold
	// the SAME prefix into every child's embedding input + ChunkInput.
	// ContextualPrefix. Without this the children embed bare 128-token
	// text — losing the disambiguating signal flat-512 chunks got from
	// Anthropic-style contextual retrieval. The 2026-05-06 eval gap
	// (overall recall -8.9pp, MRR -16pp vs baseline) was traced to this
	// exact missing-prefix-on-children pattern.
	parentPrefixes := make([]string, len(groups))
	if enrichmentEnabled {
		// Build the full document text once (concat of parent texts is the
		// best v1 approximation — the parser-original is no longer in scope
		// here without threading another argument).
		var docB strings.Builder
		for i, g := range groups {
			if i > 0 {
				docB.WriteString("\n\n")
			}
			docB.WriteString(g.ParentText)
		}
		document := docB.String()
		for i, g := range groups {
			if ctx.Err() != nil {
				return fmt.Errorf("processor: ctx cancelled during parent enrichment: %w", ctx.Err())
			}
			prefix, err := ai.GenerateChunkContext(ctx, p.aiResolver, fileName, document, g.ParentText, kbID, enrichmentModel)
			if err != nil {
				logctx.From(ctx).Warn("processor: parent enrichment failed, leaving prefix empty",
					"fileId", fileID, "parentIndex", i, "error", err)
				continue
			}
			parentPrefixes[i] = prefix
			if err := p.chunkSvc.UpdateParentContextualPrefix(ctx, parentIDs[i], prefix); err != nil {
				logctx.From(ctx).Warn("processor: parent prefix UPDATE failed",
					"fileId", fileID, "parentIndex", i, "error", err)
			}
		}
	}

	// Step C: embed children in batches. Each child carries its parent's
	// ID via ChunkInput.ParentChunkID, AND inherits the parent's
	// contextual_prefix (folded into both the embedding input and the
	// stored row so the BM25 tsvector picks it up — see the long Step B
	// comment for why this is critical).
	const embeddingBatchSize = 20
	type pendingChild struct {
		text     string
		parentID string
		prefix   string
		metadata map[string]any
	}
	totalChildren := 0
	for _, g := range groups {
		totalChildren += len(g.ChildTexts)
	}
	pending := make([]pendingChild, 0, totalChildren)
	globalChildIdx := 0
	for parentIdx, g := range groups {
		for childIdx, t := range g.ChildTexts {
			pending = append(pending, pendingChild{
				text:     t,
				parentID: parentIDs[parentIdx],
				prefix:   parentPrefixes[parentIdx],
				metadata: map[string]any{
					"chunkKind":    "child",
					"chunkIndex":   globalChildIdx,
					"parentIndex":  parentIdx,
					"childIndex":   childIdx,
					"totalParents": len(groups),
				},
			})
			globalChildIdx++
		}
	}

	processed := 0
	dimensions := 0
	for _, batch := range batches(pending, embeddingBatchSize) {
		if ctx.Err() != nil {
			return fmt.Errorf("processor: ctx cancelled during child embed: %w", ctx.Err())
		}
		// Embedding input is `prefix + "\n\n" + content` when a prefix is
		// present (matches the flat-path recipe in embedAndStore). Bare
		// child text otherwise.
		texts := make([]string, len(batch))
		for i, pc := range batch {
			if pc.prefix != "" {
				texts[i] = pc.prefix + "\n\n" + pc.text
			} else {
				texts[i] = pc.text
			}
		}
		embeddings, err := ai.GenerateEmbeddings(ctx, p.aiResolver, texts, kbID, p.embeddingCache)
		if err != nil {
			return fmt.Errorf("processor: embed children batch: %w", err)
		}
		if dimensions == 0 && len(embeddings) > 0 {
			dimensions = len(embeddings[0])
		}
		inputs := make([]vector.ChunkInput, len(batch))
		for i, pc := range batch {
			parentIDCopy := pc.parentID // address-stable copy for *string
			inputs[i] = vector.ChunkInput{
				KbID:             kbID,
				FileID:           fileID,
				Content:          pc.text,
				ContextualPrefix: pc.prefix, // BM25 tsvector picks this up via insertBatch
				ContentHash:      vector.HashContent(pc.text),
				Embedding:        embeddings[i],
				Metadata:         pc.metadata,
				ParentChunkID:    &parentIDCopy,
			}
		}
		if err := p.chunkSvc.AddDocumentChunks(ctx, fileID, inputs, dimensions, pgConfig); err != nil {
			return fmt.Errorf("processor: insert children batch: %w", err)
		}
		processed += len(batch)
		progress := 10 + int(float64(processed)/float64(totalChildren)*90)
		_ = p.store.UpdateFileProgress(ctx, fileID, progress)
	}

	logctx.From(ctx).Info("processor: parent-child ingest complete",
		"fileId", fileID, "parents", len(groups), "children", totalChildren)
	return nil
}

// embedAndStore generates embeddings for a batch of chunks, builds metadata,
// and stores them. It returns the embedding dimension of the batch (0 if
// no chunks were stored, e.g. fully dedup'd or failed) and a `failed`
// flag set to true on insertion / embedding failure.
//
// originals are the original chunk texts (stored verbatim as
// ChunkInput.Content). prefixes is parallel to originals and carries the
// optional Anthropic-style context prefix for each chunk; pass nil (or
// empty strings) when contextual enrichment is disabled / failed.
//
// pgConfig is the Postgres regconfig used to build the per-row tsvector
// (see ChunkService.AddDocumentChunks).
//
// The dimensions return value is consumed by the Phase F RAPTOR hook
// in ProcessFile so it can target the correct dim-keyed chunk table
// without re-querying the resolver.
func (p *Processor) embedAndStore(
	ctx context.Context,
	originals []string,
	prefixes []string,
	ichunks []indexedChunk,
	fullText string,
	sectionIndex *SectionIndex,
	sectionSearchOffset *int,
	startIdx int,
	fileID, kbID string,
	pgConfig string,
) (dimensions int, failed bool) {
	// Pre-embed deduplication. Hash on `originals` (the stored content), not on
	// the prefix-augmented embedding input (which varies per ingestion run
	// because the LLM-generated prefix is non-deterministic).
	//
	// Use 1536 as the dedup-table dimension regardless of the actual embedding
	// dimension. The migration only created the content_hash column on the
	// default document_chunks table; non-default-dim setups silently skip
	// cross-file dedup until operators apply the migration to their tables.
	const dedupTableDim = 1536
	var hashLookup HashLookup
	if p.chunkSvc != nil {
		hashLookup = p.chunkSvc
	}
	dedup, dedupErr := dedupBatch(ctx, hashLookup, kbID, dedupTableDim, originals)
	if dedupErr != nil {
		logctx.From(ctx).Warn("processor: dedup query failed; embedding entire batch",
			"fileId", fileID,
			"batchStart", startIdx,
			"error", dedupErr,
		)
		// Fall through with no dedup — embed everything as before. The
		// hashes are already computed inside dedupBatch and surfaced via
		// dedup.allHashes on the error path, so we reuse them rather than
		// looping vector.HashContent again per chunk.
		survivors := make([]int, len(originals))
		for i := range originals {
			survivors[i] = i
		}
		survivorHashes := dedup.allHashes
		if survivorHashes == nil {
			// Defensive: dedupBatch should always populate allHashes on the
			// error path, but if a future caller route lands here without
			// it, recompute rather than panic on a nil-indexed insert.
			survivorHashes = make([]string, len(originals))
			for i, t := range originals {
				survivorHashes[i] = vector.HashContent(t)
			}
		}
		dedup = dedupResult{survivorIdx: survivors, hashes: survivorHashes}
	} else if dedup.droppedCount > 0 {
		logctx.From(ctx).Info("processor.dedup",
			"fileId", fileID,
			"kept", len(dedup.survivorIdx),
			"dropped", dedup.droppedCount,
			"total", len(originals),
		)
	}

	if len(dedup.survivorIdx) == 0 {
		// Every chunk in this batch was already known. Nothing to embed or store.
		return 0, false
	}

	// Build survivor-only slices. The text fed to the embedding model is
	// `prefix + "\n\n" + content` when a prefix is present (matches
	// Anthropic's Contextual Retrieval recipe); otherwise just the content.
	survivorOriginals := make([]string, len(dedup.survivorIdx))
	survivorPrefixes := make([]string, len(dedup.survivorIdx))
	survivorTextsForEmbedding := make([]string, len(dedup.survivorIdx))
	for i, idx := range dedup.survivorIdx {
		survivorOriginals[i] = originals[idx]
		var prefix string
		if idx < len(prefixes) {
			prefix = prefixes[idx]
		}
		survivorPrefixes[i] = prefix
		if prefix != "" {
			survivorTextsForEmbedding[i] = prefix + "\n\n" + originals[idx]
		} else {
			survivorTextsForEmbedding[i] = originals[idx]
		}
	}
	survivorHashes := dedup.hashes // parallel to survivorOriginals

	embeddings, embErr := ai.GenerateEmbeddings(ctx, p.aiResolver, survivorTextsForEmbedding, kbID, p.embeddingCache)
	if embErr != nil {
		logctx.From(ctx).Error("processor: embedding batch failed",
			"fileId", fileID,
			"batchStart", startIdx,
			"error", embErr,
		)
		return 0, true
	}

	chunkInputs := make([]vector.ChunkInput, len(survivorOriginals))
	for i, text := range survivorOriginals {
		origIdx := dedup.survivorIdx[i]
		meta := map[string]any{"chunkIndex": startIdx + origIdx}
		if pg := ichunks[startIdx+origIdx].Page; pg > 0 {
			meta["pages"] = []int{pg}
		}
		if sectionIndex != nil {
			sections, nextOff := SectionsForChunk(sectionIndex, fullText, text, *sectionSearchOffset)
			*sectionSearchOffset = nextOff
			if len(sections) > 0 {
				meta["sections"] = sections
			}
		}
		chunkInputs[i] = vector.ChunkInput{
			Content:          text,
			ContextualPrefix: survivorPrefixes[i],
			ContentHash:      survivorHashes[i],
			Embedding:        embeddings[i],
			KbID:             kbID,
			FileID:           fileID,
			Metadata:         meta,
		}
	}

	dim := len(embeddings[0])
	if storeErr := p.chunkSvc.AddDocumentChunks(ctx, fileID, chunkInputs, dim, pgConfig); storeErr != nil {
		logctx.From(ctx).Error("processor: store chunks failed",
			"fileId", fileID,
			"batchStart", startIdx,
			"error", storeErr,
		)
		return dim, true
	}
	return dim, false
}

// runLateChunkedIngest runs the alternate ingestion path used when
// `late_chunking_enabled` is on: the document's chunks are embedded
// in document order via the late-chunking endpoint (Jina-style:
// the provider concatenates the input list, encodes once with a
// long-context model, returns one mean-pooled vector per chunk that
// carries cross-chunk context).
//
// Differences from the flat path:
//   - The text fed to the embedder is the NATURAL chunk text, never
//     `prefix + "\n\n" + content`. Prepending an LLM-generated
//     prefix here would corrupt the document's attention flow.
//     Contextual enrichment (when on) still runs and is stored on
//     the chunk row, so the BM25 tsvector (which COALESCEs prefix
//     into the to_tsvector input) and chat-time prompts still
//     benefit — only the dense vector input changes.
//   - All chunks are embedded as one contiguous run (split into
//     token-budget-sized windows internally), so dedup-dropped
//     chunks must still be embedded (we just don't insert them).
//     Skipping them would punch holes in the document flow and
//     defeat the point of late chunking. Dedup hit-rate on a single
//     fresh ingest is typically low, so the extra embedding cost
//     is bounded.
//   - Failure semantics are file-level: one window failing fails
//     the whole file (no partial inserts). The flat path's
//     per-batch partial-success model would conflict with the
//     contiguous-context requirement.
func (p *Processor) runLateChunkedIngest(
	ctx context.Context,
	fileID, kbID, fileName string,
	chunks []string,
	ichunks []indexedChunk,
	fullText string,
	sectionIndex *SectionIndex,
	sectionSearchOffset *int,
	enrichmentEnabled bool,
	enrichmentModel string,
	maxInputTokens int,
	pgConfig string,
) error {
	totalChunks := len(chunks)
	if totalChunks == 0 {
		return nil
	}

	// Stage 1: contextual enrichment for all chunks (parallel, bounded
	// concurrency). When disabled, prefixes stay empty strings.
	const maxEnrichConcurrency = 10
	prefixes := make([]string, totalChunks)
	if enrichmentEnabled {
		document := fullText
		var wg sync.WaitGroup
		sem := make(chan struct{}, maxEnrichConcurrency)
		for i, text := range chunks {
			if ctx.Err() != nil {
				break
			}
			wg.Add(1)
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				wg.Done()
				continue
			}
			go func(idx int, chunkText string) {
				defer wg.Done()
				defer func() { <-sem }()
				// Recover panics into a warn-and-continue: a panicking
				// enrichment call should leave prefixes[idx] empty (same
				// outcome as the LLM returning an error), not crash the
				// worker.
				defer func() {
					if r := recover(); r != nil {
						logctx.From(ctx).Warn("processor: chunk enrichment panicked (late-chunking path)",
							"fileId", fileID,
							"chunkIndex", idx,
							"panic", r)
					}
				}()
				ctxStr, err := ai.GenerateChunkContext(ctx, p.aiResolver, fileName, document, chunkText, kbID, enrichmentModel)
				if err != nil {
					logctx.From(ctx).Warn("processor: chunk enrichment failed (late-chunking path)",
						"fileId", fileID,
						"chunkIndex", idx,
						"error", err,
					)
					return
				}
				prefixes[idx] = ctxStr
			}(i, text)
		}
		wg.Wait()
		if ctx.Err() != nil {
			return fmt.Errorf("processor: context cancelled during late-chunking enrichment: %w", ctx.Err())
		}
	}
	_ = p.store.UpdateFileProgress(ctx, fileID, 50)

	// Stage 2: dedup against existing chunks (cross-file). Survivors keep
	// the original document order; non-survivors are still embedded so the
	// late-chunking window sees a contiguous document, but their rows are
	// discarded before insert.
	const dedupTableDim = 1536
	var hashLookup HashLookup
	if p.chunkSvc != nil {
		hashLookup = p.chunkSvc
	}
	dedup, dedupErr := dedupBatch(ctx, hashLookup, kbID, dedupTableDim, chunks)
	if dedupErr != nil {
		logctx.From(ctx).Warn("processor: dedup query failed; embedding entire document",
			"fileId", fileID,
			"error", dedupErr,
		)
		// Fall through with no dedup — all chunks are survivors.
		allSurvivors := make([]int, totalChunks)
		for i := range allSurvivors {
			allSurvivors[i] = i
		}
		allHashes := dedup.allHashes
		if allHashes == nil {
			allHashes = make([]string, totalChunks)
			for i, t := range chunks {
				allHashes[i] = vector.HashContent(t)
			}
		}
		dedup = dedupResult{survivorIdx: allSurvivors, hashes: allHashes}
	}
	if dedup.droppedCount > 0 {
		logctx.From(ctx).Info("processor.dedup",
			"fileId", fileID,
			"kept", len(dedup.survivorIdx),
			"dropped", dedup.droppedCount,
			"total", totalChunks,
			"path", "late_chunking",
		)
	}

	// Stage 3: late-chunked embedding of the FULL chunk list (in document
	// order, including dedup'd chunks). Bypasses the embedding cache
	// because late-chunked vectors depend on document context.
	embeddings, err := ai.GenerateEmbeddingsLateChunked(ctx, p.aiResolver, chunks, kbID, maxInputTokens)
	if err != nil {
		logctx.From(ctx).Error("processor: late-chunked embedding failed",
			"fileId", fileID,
			"error", err,
		)
		return fmt.Errorf("processor: late-chunked embedding: %w", err)
	}
	if len(embeddings) != totalChunks || len(embeddings[0]) == 0 {
		return fmt.Errorf("processor: late-chunked embedding returned %d vectors for %d chunks", len(embeddings), totalChunks)
	}
	_ = p.store.UpdateFileProgress(ctx, fileID, 80)

	// Stage 4: assemble ChunkInput rows for survivors only.
	if len(dedup.survivorIdx) == 0 {
		_ = p.store.UpdateFileProgress(ctx, fileID, 100)
		return nil
	}
	chunkInputs := make([]vector.ChunkInput, len(dedup.survivorIdx))
	for i, origIdx := range dedup.survivorIdx {
		text := chunks[origIdx]
		meta := map[string]any{"chunkIndex": origIdx}
		if pg := ichunks[origIdx].Page; pg > 0 {
			meta["pages"] = []int{pg}
		}
		if sectionIndex != nil {
			sections, nextOff := SectionsForChunk(sectionIndex, fullText, text, *sectionSearchOffset)
			*sectionSearchOffset = nextOff
			if len(sections) > 0 {
				meta["sections"] = sections
			}
		}
		chunkInputs[i] = vector.ChunkInput{
			Content:          text,
			ContextualPrefix: prefixes[origIdx],
			ContentHash:      dedup.hashes[i],
			Embedding:        embeddings[origIdx],
			KbID:             kbID,
			FileID:           fileID,
			Metadata:         meta,
		}
	}

	dim := len(embeddings[0])
	if storeErr := p.chunkSvc.AddDocumentChunks(ctx, fileID, chunkInputs, dim, pgConfig); storeErr != nil {
		logctx.From(ctx).Error("processor: store chunks failed (late-chunking path)",
			"fileId", fileID,
			"error", storeErr,
		)
		return fmt.Errorf("processor: store late-chunked chunks: %w", storeErr)
	}
	_ = p.store.UpdateFileProgress(ctx, fileID, 100)
	return nil
}

func (p *Processor) updateTerminalStatus(ctx context.Context, fileID, status string) {
	// Detach cancellation only — status update must run to completion even
	// when the parent task is being torn down — but keep tracing/request-id
	// values by using context.WithoutCancel(ctx).
	statusCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), terminalStatusTimeout)
	defer cancel()
	if err := p.store.UpdateFileStatus(statusCtx, fileID, status); err != nil {
		logctx.From(statusCtx).Warn("processor: failed to update terminal status",
			"fileId", fileID,
			"status", status,
			"error", err,
		)
	}
}

// batches splits items into successive slices of at most size elements.
func batches[T any](items []T, size int) [][]T {
	if size <= 0 || len(items) == 0 {
		return nil
	}
	result := make([][]T, 0, (len(items)+size-1)/size)
	for start := 0; start < len(items); start += size {
		end := start + size
		if end > len(items) {
			end = len(items)
		}
		result = append(result, items[start:end])
	}
	return result
}
