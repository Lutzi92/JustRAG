package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hibiken/asynq"
	"github.com/justrag/go-backend/internal/admineval"
	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/canonicalize"
	"github.com/justrag/go-backend/internal/chat"
	"github.com/justrag/go-backend/internal/config"
	"github.com/justrag/go-backend/internal/confluence"
	"github.com/justrag/go-backend/internal/database"
	"github.com/justrag/go-backend/internal/eval"
	"github.com/justrag/go-backend/internal/eval/gen"
	"github.com/justrag/go-backend/internal/fetcher"
	"github.com/justrag/go-backend/internal/files"
	"github.com/justrag/go-backend/internal/gencontent"
	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/kb"
	"github.com/justrag/go-backend/internal/kbconfig"
	"github.com/justrag/go-backend/internal/kg"
	"github.com/justrag/go-backend/internal/kgevents"
	"github.com/justrag/go-backend/internal/longmem"
	"github.com/justrag/go-backend/internal/migrate"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/parser"
	"github.com/justrag/go-backend/internal/parser/docling"
	"github.com/justrag/go-backend/internal/processor"
	"github.com/justrag/go-backend/internal/redisclient"
	"github.com/justrag/go-backend/internal/rss"
	"github.com/justrag/go-backend/internal/safego"
	"github.com/justrag/go-backend/internal/storage"
	"github.com/justrag/go-backend/internal/tabular"
	"github.com/justrag/go-backend/internal/vector"
	"github.com/justrag/go-backend/internal/worker"
)

// sttTranscriber adapts ai.ConfigResolver to the parser.Transcriber interface
// so the audio parser can transcribe files via the configured STT model
// without parser/ importing internal/ai (which would create a cycle).
//
// The httpClient is constructed once per worker process via
// ai.NewSTTHTTPClient — its long timeout is tuned independently of the
// completion client's connection pool.
type sttTranscriber struct {
	resolver   *ai.ConfigResolver
	httpClient *http.Client
}

func (s sttTranscriber) Transcribe(ctx context.Context, filePath, kbID, fileName, mimeType string) (string, error) {
	return ai.TranscribeAudioFromPath(ctx, s.resolver, s.httpClient, filePath, kbID, fileName, mimeType)
}

// RunWorker starts the asynq worker with all task handlers registered. It
// blocks until the worker shuts down and returns any fatal error.
func RunWorker(cfg *config.Config) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tracingShutdown, err := observability.InitTracing(ctx, "justrag-worker", "")
	if err != nil {
		return fmt.Errorf("init tracing: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := tracingShutdown(shutdownCtx); err != nil {
			slog.Warn("tracing shutdown error", "error", err)
		}
	}()

	// Worker tasks (batch embedding inserts, tsvector updates, vector index
	// ops) routinely exceed the server's 120s request-path StatementTimeout.
	// Override with WORKER_DB_STATEMENT_TIMEOUT_MS (default 0 = no limit) so
	// long-running ingestion isn't killed mid-task by the HTTP-side wall.
	workerDBCfg := cfg.DB
	workerDBCfg.StatementTimeout = cfg.WorkerDBStatementTimeout
	workerVectorDBCfg := cfg.VectorDB
	workerVectorDBCfg.StatementTimeout = cfg.WorkerDBStatementTimeout

	db, err := database.Connect(ctx, workerDBCfg, workerVectorDBCfg)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer db.Close()

	// Ensure all known vector tables exist with the current schema (adds
	// missing columns like content_hash via ALTER ... ADD COLUMN IF NOT EXISTS).
	// Idempotent and cheap; runs on every worker startup so schema drift
	// between cmd/migrate and dimension-versioned chunk tables self-heals.
	if err := migrate.EnsureVectorTables(ctx, workerDBCfg, workerVectorDBCfg); err != nil {
		slog.Warn("vector table setup failed at worker startup; continuing", "error", err)
	}

	kbStore := kb.NewStore(db.Main)
	filesStore := files.NewStore(db.Main)
	aiResolver := ai.NewConfigResolver(ai.NewStore(db.Main))
	chatStore := chat.NewStore(db.Main)
	chunkService := vector.NewChunkService(db.Vector)
	var doclingFront []parser.Parser
	if dc := buildDoclingClient(ctx, chatStore, aiResolver); dc != nil {
		doclingFront = append(doclingFront,
			&docling.FallbackParser{
				Primary:  &docling.DoclingPDFParser{Client: dc},
				Fallback: &parser.PDFParser{},
			},
			&docling.FallbackParser{
				Primary:  &docling.DoclingDocxParser{Client: dc},
				Fallback: &parser.DocxParser{},
			},
			&docling.FallbackParser{
				Primary:  &docling.DoclingPptxParser{Client: dc},
				Fallback: &parser.PptxParser{},
			},
		)
		// Only intercept standalone image uploads when captioning is on: with
		// it off, Docling would add nothing over the cheaper Tesseract-only
		// ImageParser, so we leave that path unchanged.
		if dc.Options.PictureDescription {
			doclingFront = append(doclingFront, &docling.FallbackParser{
				Primary:  &docling.DoclingImageParser{Client: dc},
				Fallback: &parser.ImageParser{},
			})
			slog.Info("docling image captioning enabled for standalone image uploads")
		}
		slog.Info("docling parser enabled for pdf + docx + pptx", "base_url", dc.BaseURL())
	}
	sttHTTPClient := ai.NewSTTHTTPClient()
	parserFactory := parser.DefaultFactoryWith(sttTranscriber{resolver: aiResolver, httpClient: sttHTTPClient}, doclingFront...)
	rdb := redisclient.New(cfg.Redis)
	defer rdb.Close()

	embeddingCache := ai.NewEmbeddingCache(rdb.Client, cfg.EmbeddingCacheTTL)
	proc := processor.NewProcessor(parserFactory, aiResolver, chunkService, filesStore, embeddingCache)

	// Worker-side QueryCache: shares the vector DB pool with go-server, so
	// invalidations issued here clear the same query_cache rows go-server
	// reads from. No StartWriter — the worker only invalidates, never writes.
	queryCache := vector.NewQueryCache(db.Vector)
	searchService := vector.NewSearchService(db.Vector, db.Main, aiResolver,
		vector.WithEmbeddingCache(embeddingCache),
		vector.WithSiteConfigReader(chatStore),
		vector.WithChunkService(chunkService),
		vector.WithQueryCache(queryCache),
	)

	// Create storage (S3 if configured, local filesystem otherwise)
	stor, err := storage.New(storage.Config{
		S3Endpoint:  cfg.S3.Endpoint,
		S3AccessKey: cfg.S3.AccessKey,
		S3SecretKey: cfg.S3.SecretKey,
		S3Bucket:    cfg.S3.Bucket,
		S3Region:    cfg.S3.Region,
	})
	if err != nil {
		return fmt.Errorf("initialize storage: %w", err)
	}

	// Shared Fetcher used by academic research worker (JustFind Tier 3).
	// JustFindUserDataDir is mounted as a volume in Docker for persistent Anubis cookies.
	//
	// AllowNoSandbox is gated on FETCHER_ALLOW_NO_SANDBOX. The Dockerfile
	// sets it to true in the image entrypoint because /app/worker runs as
	// root inside the container; a bare-metal `go run ./cmd/worker` keeps
	// Chromium's OS sandbox enabled.
	fetcherCfg := fetcher.DefaultConfig()
	fetcherCfg.JustFindUserDataDir = cfg.JustFindUserDataDir
	fetcherCfg.AllowNoSandbox = cfg.FetcherAllowNoSandbox
	sharedFetcher := fetcher.New(ctx, fetcherCfg)
	defer func() {
		if err := sharedFetcher.Close(); err != nil {
			slog.Warn("fetcher close", "err", err)
		}
	}()

	// Create Asynq server
	redisAddr := fmt.Sprintf("%s:%d", cfg.Redis.Host, cfg.Redis.Port)
	srv := worker.NewServer(worker.ServerConfig{
		RedisAddr:     redisAddr,
		RedisPassword: cfg.Redis.Password,
		RedisDB:       cfg.Redis.DB,
		RedisPoolSize: cfg.Redis.PoolSize,
		Queues:        cfg.WorkerQueues,
		Concurrency:   cfg.WorkerConcurrency,
	})

	// Start health endpoint for K8s probes.
	healthSrv := worker.NewHealthServer(cfg.WorkerHealthPort)
	healthSrv.Start()

	// Create Asynq client for enqueuing jobs from handlers and schedulers.
	asynqPoolSize := cfg.Redis.PoolSize
	if asynqPoolSize <= 0 {
		asynqPoolSize = 32
	}
	rssClient := asynq.NewClient(asynq.RedisClientOpt{
		Addr:     redisAddr,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
		PoolSize: asynqPoolSize,
	})
	defer rssClient.Close()

	// Register handlers
	mux := asynq.NewServeMux()
	confStore := confluence.NewStore(db.Main)
	fileHandler := worker.NewFileProcessingHandler(proc, kbStore, searchService, stor)
	fileHandler = worker.MarkErrorOnExhaustion(fileHandler, filesStore)
	mux.HandleFunc(jobs.TypeFileProcessing, worker.Instrument(func(ctx context.Context, task *asynq.Task) error {
		err := fileHandler(ctx, task)
		// After processing, update confluence source progress if applicable.
		confluence.UpdateSourceProgressAfterFile(ctx, confStore, task.Payload())
		return err
	}))
	textHandler := worker.MarkErrorOnExhaustion(worker.NewTextProcessingHandler(proc, kbStore, searchService), filesStore)
	mux.HandleFunc(jobs.TypeTextProcessing, worker.Instrument(textHandler))
	urlHandler := worker.MarkErrorOnExhaustion(worker.NewURLProcessingHandler(proc, stor, kbStore, searchService), filesStore)
	mux.HandleFunc(jobs.TypeURLProcessing, worker.Instrument(urlHandler))
	mux.HandleFunc(jobs.TypeRSSPoll, worker.Instrument(worker.NewRSSPollHandler(worker.RSSPollDeps{
		RSSStore:    rss.NewStore(db.Main),
		FileStore:   filesStore,
		Storage:     stor,
		AsynqClient: rssClient,
		Fetcher:     sharedFetcher,
	})))
	proc.SetSiteConfigReader(chatStore)
	proc.SetKBOverrideLister(kbconfig.NewStore(db.Main))
	proc.SetMainDB(db.Main)
	proc.SetVectorPool(db.Vector)
	proc.SetMaterializer(tabular.NewMaterializer(db.Main))
	proc.SetKGEventPublisher(kgevents.NewPublisher(rdb.Client))
	proc.SetKGDeleter(kg.NewPgStore(db.Main))
	mux.HandleFunc(jobs.TypeResearchExecution, worker.Instrument(worker.NewResearchExecutionHandler(aiResolver, searchService, rdb.Client, chatStore, sharedFetcher)))
	mux.HandleFunc(jobs.TypeAcademicResearchExecution, worker.Instrument(worker.NewAcademicResearchHandler(aiResolver, rdb.Client, chatStore, sharedFetcher)))
	mux.HandleFunc(jobs.TypeRAGASSample, worker.Instrument(worker.NewRAGASSampleHandlerForResolver(aiResolver)))
	// Crawl: moved out of the HTTP server so Chromium/rod doesn't run in
	// go-server. See internal/crawler/handler.go for the HTTP façade and
	// internal/worker/crawl.go for the BFS loop.
	mux.HandleFunc(jobs.TypeCrawl, worker.Instrument(worker.NewCrawlHandler(worker.CrawlDeps{
		Fetcher: sharedFetcher,
		Redis:   rdb.Client,
	})))

	// Re-embedding: delete old chunks first, then re-process the file.
	// Wrapped in MarkErrorOnExhaustion like file-processing so a failed
	// retry surfaces as status='error' with a reason instead of sitting in
	// 'processing' until the stuck-file sweep.
	reembedHandler := asynq.HandlerFunc(func(ctx context.Context, task *asynq.Task) error {
		var payload jobs.FileProcessingPayload
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			return fmt.Errorf("unmarshal re-embedding payload: %w", err)
		}
		slog.Info("re-embedding file", "fileId", payload.FileID, "kbId", payload.KbID)

		// Delete old chunks from every existing chunk table before re-processing.
		// Using the dynamic table list (not a hardcoded dim slice) guarantees we
		// neither fail on dims that were never provisioned nor leak chunks in
		// dims the operator has since added.
		if err := chunkService.DeleteChunksByFileIDAllDims(ctx, payload.FileID); err != nil {
			return fmt.Errorf("failed to delete old chunks for file %s: %w", payload.FileID, err)
		}

		// Phase 3 §D: drop old parents alongside old children. If the
		// file was previously ingested via the flat path the parents
		// table simply has no matching rows (no-op). Non-fatal: orphan
		// parents would accumulate and need a periodic cleanup, but
		// never break the user-facing re-ingest.
		if err := chunkService.DeleteParentChunksByFileID(ctx, payload.FileID); err != nil {
			slog.Warn("re-embedding: parent cleanup failed",
				"fileId", payload.FileID, "error", err)
		}

		// Now re-process the file using the standard file processing handler.
		// The "file_added" reason on success is shared with new ingestion;
		// the explicit chunk-deletion above also justifies an immediate
		// invalidation, but this is captured by the post-ProcessFile hook.
		return worker.NewFileProcessingHandler(proc, kbStore, searchService, stor)(ctx, task)
	})
	mux.HandleFunc(jobs.TypeReEmbedding, worker.Instrument(worker.MarkErrorOnExhaustion(reembedHandler, filesStore)))

	// Confluence sync: fetch pages, convert to markdown, enqueue file processing.
	mux.HandleFunc(jobs.TypeConfluenceSync, worker.Instrument(confluence.NewSyncHandler(confluence.SyncDeps{
		Store:        confStore,
		JWTSecret:    cfg.JWTSecret,
		AsynqClient:  rssClient,
		Storage:      stor,
		ChunkService: chunkService,
	})))

	// Content generation (podcast with TTS audio).
	genContentStore := gencontent.NewStore(db.Main)
	mux.HandleFunc(jobs.TypeContentGeneration, worker.Instrument(worker.NewPodcastHandler(worker.PodcastDeps{
		AIResolver:    aiResolver,
		SearchSvc:     searchService,
		Store:         genContentStore,
		Redis:         rdb.Client,
		TTSHTTPClient: ai.NewTTSHTTPClient(),
	})))

	// In-app eval runner: executes production-context retrieval (and optional
	// LLM-as-judge) against a persisted eval_runs row.
	//
	// RunDeps deliberately does NOT include the shared searchService — the
	// runner constructs its own private SearchService per run so that
	// installing the frozen SnapshotConfigReader doesn't mutate the
	// process-wide singleton and race with live chat searches.
	// EmbeddingCache is intentionally nil here: eval runs are isolated and
	// should not share or pollute the cache used by production traffic.
	evalStore := eval.NewStore(db.Main)
	evalDeps := eval.RunDeps{
		AIResolver:     aiResolver,
		VectorDB:       db.Vector,
		MainDB:         db.Main,
		EmbeddingCache: nil, // intentionally isolated from live traffic cache
		GoldenSetStore: eval.NewGoldenSetStore(db.Main),
		// KbSystemPrompt is read by OrchestratorDispatchAdapter so the
		// supervisor / plan-execute / agentic orchestrators see the
		// same KB system prompt production wires through tryDeepChat.
		// Errors and missing values resolve to the empty string —
		// orchestrators tolerate that and run without the prompt.
		KbSystemPrompt: func(ctx context.Context, kbID string) string {
			if sp, err := chatStore.GetKBSystemPrompt(ctx, kbID); err == nil && sp != nil {
				return *sp
			}
			return ""
		},
	}
	evalWorker := admineval.NewWorker(db.Main, evalStore, func(ctx context.Context, r eval.Run) (json.RawMessage, error) {
		return eval.RunInProcessFromRecord(ctx, r, evalDeps)
	})
	mux.HandleFunc(jobs.TypeEvalRun, worker.Instrument(evalWorker.HandleRun))

	// Corpus-based golden-set generation.
	genJobStore := eval.NewGenJobStore(db.Main)
	genGoldenSetStore := eval.NewGoldenSetStore(db.Main)
	genWorker := admineval.NewGenWorker(genJobStore, genGoldenSetStore, func(kbID, lang, model string) *gen.Generator {
		return gen.New(
			gen.NewDBCorpus(db.Main, chunkService),
			gen.NewAIQGen(aiResolver, kbID, lang, model),
			gen.NewSearchNeighbors(searchService, 5),
			0.5,
		)
	})
	mux.HandleFunc(jobs.TypeGenerateGoldenSet, worker.Instrument(genWorker.HandleGenerate))

	// Per-user memory re-embedding: recomputes embeddings for all live
	// user_memory rows after the embedding dim has changed.
	longmemStore := longmem.NewPgStore(db.Main)
	memEmbed := func(ctx context.Context, texts []string) ([][]float64, error) {
		return ai.GenerateEmbeddings(ctx, aiResolver, texts, "", embeddingCache)
	}
	mux.HandleFunc(jobs.TypeReEmbedUserMemory, worker.Instrument(worker.NewReEmbedUserMemoryHandler(longmemStore, memEmbed)))

	// EDC entity canonicalization: admin-triggered batch entity dedup per KB.
	canonStore := kg.NewPgStore(db.Main)
	embedFor := func(kbID string) canonicalize.EmbedFunc {
		return func(ctx context.Context, texts []string) ([][]float64, error) {
			return ai.GenerateEmbeddings(ctx, aiResolver, texts, kbID, embeddingCache)
		}
	}
	confirmFor := func(kbID string) canonicalize.ConfirmFunc {
		return func(ctx context.Context, ents []canonicalize.CanonEntity, pairs []canonicalize.Pair) ([]canonicalize.Pair, error) {
			model := chat.ResolveFastTierModel(ctx, chatStore, "kg_canonicalization_model")
			res, gerr := ai.GenerateCompletionStructured(ctx,
				aiResolver, canonicalize.BuildConfirmPrompt(ents, pairs), canonicalize.ConfirmSystemPrompt,
				kbID, model, &ai.StructuredSpec{Name: "kg_canon_confirm", Schema: json.RawMessage(canonicalize.ConfirmSchema)})
			if gerr != nil {
				return nil, gerr
			}
			return canonicalize.ParseConfirm(res.Content, pairs)
		}
	}
	canonPub := kgevents.NewPublisher(rdb.Client)
	mux.HandleFunc(jobs.TypeEntityCanonicalizeKB, worker.Instrument(worker.NewEntityCanonicalizeHandler(worker.CanonicalizeDeps{
		Store:      canonStore,
		Reader:     chatStore,
		EmbedFor:   embedFor,
		ConfirmFor: confirmFor,
		Publish:    func(ctx context.Context, kbID string) { canonPub.PublishGraphChanged(ctx, kbID) },
	})))

	// RSS and Confluence schedulers no longer run in the worker — they are
	// started by go-server under a Redis leader lock (see internal/app/scheduler.go).
	// Workers only execute the enqueued tasks.

	// Start periodic maintenance tasks (only if enabled — set WORKER_MAINTENANCE=false
	// on replicas to avoid duplicate work in K8s). stopMaintenance both cancels
	// the maintenance context and blocks until every loop has returned, so the
	// deferred DB-pool teardown below cannot race in-flight queries.
	var stopMaintenance func()
	if cfg.WorkerMaintenance {
		stopMaintenance = worker.StartMaintenance(ctx, worker.MaintenanceConfig{
			MainDB:           db.Main,
			VectorDB:         db.Vector,
			StuckFileTimeout: cfg.StuckFileTimeout,
		})
	}
	defer func() {
		if stopMaintenance != nil {
			stopMaintenance()
		}
	}()

	// Health server cleanup on any exit path.
	defer healthSrv.Shutdown()

	// Mark worker as ready for K8s readiness probe.
	healthSrv.SetReady(true)

	sigCh, stopSignals := newShutdownSignals()
	defer stopSignals()

	// Track the signal handler so its lifecycle is explicit: defers are
	// LIFO, so this Wait() runs BEFORE the health-server, maintenance,
	// DB-pool, and tracing teardowns — preventing the handler from
	// touching srv / healthSrv while their resources are being released.
	var sigWg sync.WaitGroup
	sigWg.Add(1)
	defer sigWg.Wait()
	// Registered AFTER the Wait above so it runs BEFORE it (LIFO): the
	// signal goroutine's ctx.Done() escape hatch only works if ctx is
	// actually cancelled before we block on Wait. Without this, an
	// srv.Run failure deadlocks here until a manual SIGTERM. Idempotent.
	defer cancel()
	// Graceful shutdown on signal — cancel context so schedulers, maintenance,
	// and in-flight handlers all stop. Deferred cleanup handles the rest.
	// Escape on ctx.Done so an early srv.Run failure doesn't leak this
	// goroutine waiting for a signal that will never arrive.
	// safego.Go provides panic recovery so a nil-deref during shutdown
	// (the worst possible moment to crash) doesn't bypass deferred cleanup.
	safego.Go(func() {
		defer sigWg.Done()
		select {
		case sig := <-sigCh:
			slog.Info("received shutdown signal", "signal", sig)
			healthSrv.SetReady(false)
			srv.Shutdown()
			cancel()
		case <-ctx.Done():
		}
	})

	// Log effective queue configuration.
	activeQueues := cfg.WorkerQueues
	if len(activeQueues) == 0 {
		activeQueues = []string{jobs.QueueQuick, jobs.QueueHeavy, jobs.QueueBatch}
	}
	slog.Info("Go worker starting",
		"queues", activeQueues,
		"concurrency", cfg.WorkerConcurrency,
		"maintenance", cfg.WorkerMaintenance,
		"healthPort", cfg.WorkerHealthPort,
	)
	if err := srv.Run(mux); err != nil {
		return fmt.Errorf("worker error: %w", err)
	}

	slog.Info("worker stopped gracefully")
	return nil
}

// siteConfigReaderForDocling is the minimum interface buildDoclingClient
// needs. *chat.PGStore satisfies it implicitly.
type siteConfigReaderForDocling interface {
	GetSiteConfigValue(ctx context.Context, key string) (*string, error)
}

// buildDoclingClient reads site-config to decide whether to construct a
// Docling Serve client. Returns nil when disabled, when base URL is empty,
// or when the site-config read fails.
//
// The DOCLING_TIMEOUT_SECONDS env var (default 300) controls the per-request
// timeout. Generous because Docling can take ~60s on a complex 20-page PDF.
func buildDoclingClient(ctx context.Context, scr siteConfigReaderForDocling, resolver *ai.ConfigResolver) *docling.Client {
	enabledRaw, err := scr.GetSiteConfigValue(ctx, "docling_enabled")
	if err != nil {
		slog.Warn("docling: failed to read docling_enabled site-config; falling back to built-in parsers", "error", err)
		return nil
	}
	if enabledRaw == nil || (*enabledRaw != "true" && *enabledRaw != "1") {
		return nil
	}
	urlRaw, err := scr.GetSiteConfigValue(ctx, "docling_base_url")
	if err != nil {
		slog.Warn("docling: failed to read docling_base_url site-config; falling back to built-in parsers", "error", err)
		return nil
	}
	if urlRaw == nil || *urlRaw == "" {
		slog.Warn("docling_enabled=true but docling_base_url is empty; falling back to pdftotext")
		return nil
	}
	timeout := 300 * time.Second
	if v := os.Getenv("DOCLING_TIMEOUT_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			timeout = time.Duration(n) * time.Second
		}
	}
	client := docling.NewClient(*urlRaw, timeout)
	client.Options = readDoclingOptions(ctx, scr, resolver)
	return client
}

// readDoclingOptions derives caption/table flags from site-config. Read
// failures degrade to safe defaults (captioning off, table_mode "fast").
//
// When picture description is on, the vision endpoint + API key are sourced
// from the app's AI provider config (the same one /api/describe-image uses) and
// passed to Docling per-request, so the model-API credential never lives on the
// Docling sidecar.
func readDoclingOptions(ctx context.Context, scr siteConfigReaderForDocling, resolver *ai.ConfigResolver) docling.ConvertOptions {
	opts := docling.ConvertOptions{TableMode: "fast"}

	if v, err := scr.GetSiteConfigValue(ctx, "docling_picture_description_enabled"); err == nil && v != nil && (*v == "true" || *v == "1") {
		opts.PictureDescription = true
		opts.PictureClassification = true // classification is a cheap filter signal; on whenever captioning is on

		// Vision model: same resolution as /api/describe-image
		// (describe_image_model → model_tier_fast).
		opts.PictureAPIModel = chat.DescribeImageModel(ctx, scr)
		// Endpoint + bearer key from the AI provider config. BaseURL carries a
		// trailing slash; the chat-completions path mirrors ai.Client.
		if resolver != nil {
			if cfg, rerr := resolver.Resolve(ctx, ""); rerr == nil && cfg != nil {
				opts.PictureAPIURL = cfg.BaseURL + "chat/completions"
				opts.PictureAPIKey = cfg.APIKey
			} else if rerr != nil {
				slog.Warn("docling: could not resolve model API config for image captioning; captions may be skipped", "error", rerr)
			}
		}
		if opts.PictureAPIModel == "" {
			slog.Warn("docling: picture description enabled but no vision model resolved (set describe_image_model or model_tier_fast)")
		}
	}

	opts.PictureAreaThreshold = 0.05
	if v, err := scr.GetSiteConfigValue(ctx, "docling_picture_area_threshold"); err == nil && v != nil {
		if f, perr := strconv.ParseFloat(strings.TrimSpace(*v), 64); perr == nil && f >= 0 && f <= 1 {
			opts.PictureAreaThreshold = f
		}
	}

	if v, err := scr.GetSiteConfigValue(ctx, "docling_table_mode"); err == nil && v != nil {
		if m := strings.TrimSpace(*v); m == "accurate" || m == "fast" {
			opts.TableMode = m
		}
	}
	return opts
}
