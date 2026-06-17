package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hibiken/asynq"
	"github.com/justrag/go-backend/internal/academic"
	"github.com/justrag/go-backend/internal/adminagentmetrics"
	"github.com/justrag/go-backend/internal/adminconfigs"
	"github.com/justrag/go-backend/internal/admineval"
	"github.com/justrag/go-backend/internal/adminfeedback"
	"github.com/justrag/go-backend/internal/adminglobalkbs"
	"github.com/justrag/go-backend/internal/adminkboverview"
	"github.com/justrag/go-backend/internal/adminmaintenance"
	"github.com/justrag/go-backend/internal/adminmcp"
	"github.com/justrag/go-backend/internal/adminproviders"
	"github.com/justrag/go-backend/internal/adminusers"
	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/analytics"
	"github.com/justrag/go-backend/internal/apidocs"
	"github.com/justrag/go-backend/internal/apikeyauth"
	"github.com/justrag/go-backend/internal/apikeys"
	"github.com/justrag/go-backend/internal/auditlogs"
	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/authhandler"
	"github.com/justrag/go-backend/internal/cascade"
	"github.com/justrag/go-backend/internal/chat"
	"github.com/justrag/go-backend/internal/chatattach"
	"github.com/justrag/go-backend/internal/config"
	"github.com/justrag/go-backend/internal/confluence"
	"github.com/justrag/go-backend/internal/contentgen"
	"github.com/justrag/go-backend/internal/crawler"
	"github.com/justrag/go-backend/internal/eval"
	"github.com/justrag/go-backend/internal/feedback"
	"github.com/justrag/go-backend/internal/fetcher"
	"github.com/justrag/go-backend/internal/files"
	"github.com/justrag/go-backend/internal/gencontent"
	"github.com/justrag/go-backend/internal/health"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/kb"
	"github.com/justrag/go-backend/internal/kbaccess"
	"github.com/justrag/go-backend/internal/kbconfig"
	"github.com/justrag/go-backend/internal/kg"
	"github.com/justrag/go-backend/internal/kggraph"
	"github.com/justrag/go-backend/internal/longmem"
	"github.com/justrag/go-backend/internal/mcp"
	"github.com/justrag/go-backend/internal/mcp/builtin"
	memorypkg "github.com/justrag/go-backend/internal/memory"
	"github.com/justrag/go-backend/internal/middleware"
	"github.com/justrag/go-backend/internal/misc"
	"github.com/justrag/go-backend/internal/openaicompat"
	"github.com/justrag/go-backend/internal/prompts"
	"github.com/justrag/go-backend/internal/proxy"
	"github.com/justrag/go-backend/internal/publicapi"
	"github.com/justrag/go-backend/internal/publicconfigs"
	"github.com/justrag/go-backend/internal/research"
	"github.com/justrag/go-backend/internal/rss"
	"github.com/justrag/go-backend/internal/safego"
	"github.com/justrag/go-backend/internal/sessionmem"
	"github.com/justrag/go-backend/internal/siteconfig"
	"github.com/justrag/go-backend/internal/sserelay"
	"github.com/justrag/go-backend/internal/systemhealth"
	"github.com/justrag/go-backend/internal/tabular"
	"github.com/justrag/go-backend/internal/users"
	"github.com/justrag/go-backend/internal/vector"
	"github.com/justrag/go-backend/internal/websearch"
)

// routeCtx holds shared dependencies used across route registration functions.
type routeCtx struct {
	mux             *http.ServeMux
	infra           *serverInfra
	cfg             *config.Config
	authMw          *auth.Middleware
	kbMw            *kbaccess.Middleware
	kbAccessStore   *kbaccess.PGStore
	aiResolver      *ai.ConfigResolver
	searchService   *vector.SearchService
	chatStore       *chat.PGStore
	filesStore      *files.PGStore
	filesHandler    *files.Handler
	genContentStore *gencontent.PGStore
	chunkService    *vector.ChunkService
	cascadeDeleter  *cascade.Deleter
	kbStore         *kb.PGStore
	sharedFetcher   *fetcher.Fetcher
	asynqInspector  *asynq.Inspector
	// agentDecisionStore is the Phase 1 §1.4 agent_decisions persistence
	// shared between the admin metrics handler (read path) and the chat
	// handler (fire-and-forget write path). Constructed in setupRoutes
	// before any register* call so registration order is not load-bearing.
	agentDecisionStore *adminagentmetrics.PgStore

	// kgStore is the AP-C3 / AP-C4 read-side kg.Store. Shared
	// between the graph_search MCP tool and the chat handler's
	// AP-C4 router heuristic.
	kgStore kg.Store

	// feedbackReader aggregates per-chunk feedback signals from the main
	// DB. Shared between the server-side SearchService (retrieval boost,
	// gated by chat_feedback_boost_enabled) and the admin review endpoint
	// (GET /api/admin/feedback/chunks).
	feedbackReader *feedback.Reader

	// Middleware chains
	superadminChain func(http.HandlerFunc) http.Handler
	adminChain      func(http.HandlerFunc) http.Handler
	kbViewChain     func(http.HandlerFunc) http.Handler
	kbEditChain     func(http.HandlerFunc) http.Handler
	kbTuningChain   func(http.HandlerFunc) http.Handler
	analyticsChain  func(http.HandlerFunc) http.Handler
	apiKeyChain     func(http.HandlerFunc) http.Handler

	// Per-KB settings
	kbConfigStore   *kbconfig.Store
	kbConfigHandler *kbconfig.Handler
}

// setupRoutes registers all HTTP routes on the given mux using the provided
// infrastructure and config. The returned cleanup function shuts down any
// background goroutines created during route setup (e.g. rate limiters).
func setupRoutes(ctx context.Context, mux *http.ServeMux, infra *serverInfra, cfg *config.Config, buildVersion string) (func(), error) {
	authMiddleware := auth.NewMiddleware(cfg.JWTSecret, infra.blacklist)
	filesStore := files.NewStore(infra.db.Main)
	genContentStore := gencontent.NewStore(infra.db.Main)
	kbAccessStore := kbaccess.NewStore(infra.db.Main)
	kbMw := kbaccess.NewMiddleware(kbAccessStore)
	chunkService := vector.NewChunkService(infra.db.Vector)
	cascadeDeleter := cascade.New(infra.db.Main, infra.db.Vector, infra.stor)
	aiResolver := ai.NewConfigResolver(ai.NewStore(infra.db.Main))
	embeddingCache := ai.NewEmbeddingCache(infra.rdb.Client, cfg.EmbeddingCacheTTL)
	chatStore := chat.NewStore(infra.db.Main)
	kbConfigStore := kbconfig.NewStore(infra.db.Main)
	kbConfigHandler := kbconfig.NewHandler(kbConfigStore, chatStore)
	queryCache := vector.NewQueryCache(infra.db.Vector)
	queryCache.StartWriter(ctx)
	// Track the sweeper goroutine so routeCleanup can drain it before the
	// infra cleanup() closes the vector pool — mirrors the schedulerWg /
	// sigWg shutdown discipline in RunServer. Its own cancelable context
	// lets routeCleanup stop it deterministically without depending on the
	// outer ctx (which, on a startup error path, is canceled only by the
	// final LIFO defer — after routeCleanup has already run). safego.Go
	// adds panic recovery, matching StartWriter and the schedulers.
	sweeperCtx, sweeperCancel := context.WithCancel(ctx)
	var sweeperWg sync.WaitGroup
	sweeperWg.Add(1)
	safego.Go(func() {
		defer sweeperWg.Done()
		runQueryCacheSweeper(sweeperCtx, queryCache)
	})
	searchService := vector.NewSearchService(infra.db.Vector, infra.db.Main, aiResolver,
		vector.WithEmbeddingCache(embeddingCache),
		vector.WithSiteConfigReader(chatStore),
		vector.WithChunkService(chunkService),
		vector.WithQueryCache(queryCache),
	)
	kbStore := kb.NewStore(infra.db.Main)

	// Shared Fetcher used by crawler, websearch, and academic handlers.
	// JustFindUserDataDir holds the persistent rod profile so the Anubis
	// challenge cookie survives across restarts. Mounted from the
	// rag-justfind-profile docker volume in production. Locally, ensure
	// the directory exists or Tier 3 calls will fail.
	//
	// AllowNoSandbox is gated on FETCHER_ALLOW_NO_SANDBOX — only set to
	// true inside the container image, where /app/server runs as root and
	// Chromium refuses to start without --no-sandbox. Local `go run` and
	// bare-metal deployments keep Chromium's OS sandbox enabled.
	fetcherCfg := fetcher.DefaultConfig()
	fetcherCfg.JustFindUserDataDir = cfg.JustFindUserDataDir
	fetcherCfg.AllowNoSandbox = cfg.FetcherAllowNoSandbox
	sharedFetcher := fetcher.New(ctx, fetcherCfg)

	// Single shared files handler — stateless beyond injected deps, so the
	// previous per-route construction allocated identical objects twice.
	filesHandler := files.NewHandler(filesStore, infra.stor, chunkService, infra.asynqClient)
	filesHandler.SetFetcher(sharedFetcher)
	// Wire the QueryCache invalidator so file-mutation handlers (delete)
	// nuke cached SearchResults whose chunks may reference outdated content.
	filesHandler.SetQueryCacheInvalidator(searchService)
	cascadeDeleter.SetQueryCacheInvalidator(searchService)

	// Online feedback loop: per-chunk feedback aggregate reader (main DB).
	// Attached to the request-time SearchService for the retrieval boost
	// (gated by chat_feedback_boost_enabled) and reused by the admin review
	// endpoint below.
	feedbackReader := feedback.NewReader(infra.db.Main)
	searchService.SetFeedbackReader(feedbackReader)

	// Single shared asynq Inspector — each Inspector owns its own Redis
	// connection, so creating one per route group multiplied connections.
	asynqInspector := asynq.NewInspector(asynq.RedisClientOpt{
		Addr:     infra.rdb.Options().Addr,
		Password: infra.rdb.Options().Password,
	})

	// Phase 1 §1.4 agent_decisions store — shared between the admin
	// metrics handler (read path) and the chat handler (fire-and-forget
	// write path). Constructed here so neither registerAdminRoutes nor
	// registerChatRoutes is responsible for initialization order.
	agentDecisionStore := adminagentmetrics.NewPgStore(infra.db.Main)

	rc := &routeCtx{
		mux:                mux,
		infra:              infra,
		cfg:                cfg,
		authMw:             authMiddleware,
		kbMw:               kbMw,
		kbAccessStore:      kbAccessStore,
		aiResolver:         aiResolver,
		searchService:      searchService,
		chatStore:          chatStore,
		filesStore:         filesStore,
		filesHandler:       filesHandler,
		genContentStore:    genContentStore,
		chunkService:       chunkService,
		cascadeDeleter:     cascadeDeleter,
		kbStore:            kbStore,
		sharedFetcher:      sharedFetcher,
		asynqInspector:     asynqInspector,
		agentDecisionStore: agentDecisionStore,
		feedbackReader:     feedbackReader,
		superadminChain:    auth.RoleChain(authMiddleware, auth.RoleSuperAdmin),
		adminChain:         auth.RoleChain(authMiddleware, auth.RoleAdmin, auth.RoleSuperAdmin),
		kbViewChain: func(h http.HandlerFunc) http.Handler {
			return authMiddleware.Authenticate(kbMw.RequireKBPermission("view")(http.HandlerFunc(h)))
		},
		kbEditChain: func(h http.HandlerFunc) http.Handler {
			return authMiddleware.Authenticate(kbMw.RequireKBPermission("edit")(http.HandlerFunc(h)))
		},
		// kbTuningChain gates the per-KB Settings surface: KB edit permission
		// AND role in {api-user, admin, superadmin}. Order:
		// Authenticate -> RequireRole -> RequireKBPermission("edit").
		// Superadmin bypasses RequireRole and is granted edit by the KB check.
		kbTuningChain: func(h http.HandlerFunc) http.Handler {
			return authMiddleware.Authenticate(
				authMiddleware.RequireRole(auth.RoleAPIUser, auth.RoleAdmin)(
					kbMw.RequireKBPermission("edit")(http.HandlerFunc(h))))
		},
		kbConfigStore:   kbConfigStore,
		kbConfigHandler: kbConfigHandler,
		analyticsChain: func(h http.HandlerFunc) http.Handler {
			return authMiddleware.Authenticate(
				kbMw.RequireKBPermission("view")(
					kbMw.RequireAnalyticsAccess(http.HandlerFunc(h))))
		},
		apiKeyChain: auth.RoleChain(authMiddleware, auth.RoleAdmin, auth.RoleSuperAdmin, auth.RoleAPIUser),
	}

	// Per-endpoint Redis rate limiters. Most use the cheap fixed-window
	// counter (INCR + EXPIRE). Login flips to ModeSlidingWindow so an
	// attacker can't double the per-window budget by straddling the
	// boundary — the failure-counting path here is the one place where
	// the 2x burst gap actually changes the security posture.
	loginRL := middleware.NewRedisRateLimiter(infra.rdb.Client, middleware.RedisRateLimitConfig{
		Max: 5, Window: 15 * time.Minute, Category: "login",
		Mode: middleware.ModeSlidingWindow, FailClosed: true,
	})
	chatRL := middleware.NewRedisRateLimiter(infra.rdb.Client, middleware.RedisRateLimitConfig{
		Max: 20, Window: time.Minute, Category: "chat",
	})
	researchRL := middleware.NewRedisRateLimiter(infra.rdb.Client, middleware.RedisRateLimitConfig{
		Max: 5, Window: time.Minute, Category: "research",
	})
	generateRL := middleware.NewRedisRateLimiter(infra.rdb.Client, middleware.RedisRateLimitConfig{
		Max: 10, Window: time.Minute, Category: "generate",
	})
	apiRL := middleware.NewRedisRateLimiter(infra.rdb.Client, middleware.RedisRateLimitConfig{
		Max: 100, Window: time.Minute, Category: "api",
	})

	registerHealthRoutes(rc, buildVersion)
	loginLimiter := registerAuthRoutes(ctx, rc, loginRL)
	registerAdminRoutes(rc)
	registerKBRoutes(rc)
	registerChatRoutes(ctx, rc, chatRL)
	registerFileRoutes(rc)
	registerContentGenRoutes(rc, generateRL)
	registerResearchRoutes(rc, researchRL)
	registerPublicAPIRoutes(rc, apiRL)
	registerMiscRoutes(rc)
	registerStaticRoutes(rc)

	// Only the in-memory loginLimiter (returned from registerAuthRoutes) owns
	// a background goroutine and needs an explicit Shutdown. The Redis-backed
	// limiters above (chatRL, researchRL, generateRL, apiRL, loginRL) keep no
	// long-lived state — they call INCR/EXPIRE per request — so there is
	// nothing to stop. The standalone in-memory apiLimiter created in
	// RunServer is shut down via its own defer there.
	cleanup := func() {
		// Stop the query-cache sweeper and wait for any in-flight
		// SweepExpired to unwind before the caller's infra cleanup closes
		// the vector pool — avoids a pool-close race / log noise on shutdown.
		sweeperCancel()
		sweeperWg.Wait()
		loginLimiter.Shutdown()
	}

	if err := registerFrontendServing(rc); err != nil {
		cleanup()
		return nil, err
	}
	return cleanup, nil
}

func registerHealthRoutes(rc *routeCtx, buildVersion string) {
	healthHandler := health.NewHandler(rc.infra.db, buildVersion, health.WithRedis(rc.infra.rdb))
	rc.mux.HandleFunc("GET /health", healthHandler.Health)
	rc.mux.HandleFunc("GET /ready", healthHandler.Ready)
	rc.mux.HandleFunc("GET /version", healthHandler.Version)

	// Prometheus metrics — admin-only.
	rc.mux.Handle("GET /metrics", rc.adminChain(middleware.MetricsHandler()))

	docsHandler := apidocs.NewHandler()
	rc.mux.HandleFunc("GET /api/v1/openapi.json", docsHandler.ServeSpec)
	rc.mux.HandleFunc("GET /api/v1/docs", docsHandler.ServeDocs)
}

func registerAuthRoutes(ctx context.Context, rc *routeCtx, redisRL *middleware.RedisRateLimiter) *middleware.RateLimiter {
	authHandler := authhandler.NewHandler(authhandler.NewStore(rc.infra.db.Main), rc.cfg.JWTSecret, rc.infra.blacklist)
	// Pass the server ctx (not context.Background) so the limiter's
	// internal cleanup goroutine stops when the server cancels — defence
	// in depth: even if Shutdown() is skipped on an early-error path,
	// the goroutine winds down with the surrounding server.
	loginLimiter := middleware.NewRateLimiter(ctx, rc.cfg.RateLimitLogin)

	// Wire up failure-only rate limiting: CheckOnly blocks already-exceeded IPs,
	// RecordFailure is called by the handler only on failed login attempts.
	authHandler.SetFailureRecorders(redisRL, loginLimiter)
	rc.mux.Handle("POST /api/auth/login", redisRL.CheckOnly(loginLimiter.CheckOnly(http.HandlerFunc(authHandler.Login))))
	rc.mux.Handle("POST /api/auth/logout", rc.authMw.Authenticate(http.HandlerFunc(authHandler.Logout)))
	rc.mux.Handle("POST /api/auth/refresh", rc.authMw.Authenticate(http.HandlerFunc(authHandler.Refresh)))

	// OIDC SSO — public; the login form discovers it via GET /api/auth/providers.
	rc.mux.HandleFunc("GET /api/auth/providers", authHandler.ListPublicProviders)
	rc.mux.HandleFunc("GET /api/auth/oidc/login", authHandler.OIDCLogin)
	rc.mux.HandleFunc("GET /api/auth/oidc/callback", authHandler.OIDCCallback)
	rc.mux.HandleFunc("GET /api/auth/oidc/logout", authHandler.OIDCLogout)
	// Back-Channel Logout 1.0: the IdP POSTs a signed logout_token (server-to-
	// server, no JustRAG JWT) when the user's SSO session ends in another RP.
	rc.mux.HandleFunc("POST /api/auth/oidc/logout", authHandler.OIDCBackchannelLogout)

	// Site config — optional auth (parses token if present so admin sees all keys)
	siteConfigHandler := siteconfig.NewHandler(siteconfig.NewStore(rc.infra.db.Main))
	rc.mux.Handle("GET /api/site-config", rc.authMw.OptionalAuth(http.HandlerFunc(siteConfigHandler.GetSiteConfig)))
	rc.mux.Handle("POST /api/site-config", rc.superadminChain(siteConfigHandler.UpdateSiteConfig))
	rc.mux.Handle("POST /api/site-config/logo", rc.superadminChain(siteConfigHandler.UploadLogo))

	// Public configs — requires auth
	publicConfigsHandler := publicconfigs.NewHandler(publicconfigs.NewStore(rc.infra.db.Main))
	rc.mux.Handle("GET /api/public/configs", rc.authMw.Authenticate(http.HandlerFunc(publicConfigsHandler.GetConfigs)))

	// Users — auth required
	usersHandler := users.NewHandler(users.NewStore(rc.infra.db.Main))
	rc.mux.Handle("GET /api/users/{id}", rc.authMw.Authenticate(http.HandlerFunc(usersHandler.GetUser)))
	rc.mux.Handle("PATCH /api/users/{id}", rc.authMw.Authenticate(http.HandlerFunc(usersHandler.UpdateUser)))

	// My Memory (GDPR: list / delete one / bulk clear / export). Available
	// to every authenticated user regardless of chat_longmem_enabled.
	memoryHandler := memorypkg.NewHandler(longmem.NewPgStore(rc.infra.db.Main))
	rc.mux.Handle("GET /api/user/memory", rc.authMw.Authenticate(http.HandlerFunc(memoryHandler.List)))
	rc.mux.Handle("GET /api/user/memory/export", rc.authMw.Authenticate(http.HandlerFunc(memoryHandler.Export)))
	rc.mux.Handle("DELETE /api/user/memory/{id}", rc.authMw.Authenticate(http.HandlerFunc(memoryHandler.DeleteOne)))
	rc.mux.Handle("DELETE /api/user/memory", rc.authMw.Authenticate(http.HandlerFunc(memoryHandler.DeleteAll)))

	// API Keys
	apiKeysHandler := apikeys.NewHandler(apikeys.NewStore(rc.infra.db.Main))
	rc.mux.Handle("POST /api/api-keys", rc.apiKeyChain(apiKeysHandler.CreateApiKey))
	rc.mux.Handle("GET /api/api-keys", rc.apiKeyChain(apiKeysHandler.ListApiKeys))
	rc.mux.Handle("DELETE /api/api-keys/{id}", rc.apiKeyChain(apiKeysHandler.DeleteApiKey))

	return loginLimiter
}

func registerAdminRoutes(rc *routeCtx) {
	// System health
	systemHealthSvc := systemhealth.NewService(systemhealth.NewStore(rc.infra.db.Main), rc.infra.db.Main, rc.infra.db.Vector, rc.infra.rdb.Client, rc.asynqInspector, rc.cfg.S3.S3Enabled())
	systemHealthHandler := systemhealth.NewHandler(systemHealthSvc, rc.aiResolver)
	rc.mux.Handle("GET /api/system-health/live", rc.adminChain(systemHealthHandler.GetLiveMetrics))
	rc.mux.Handle("GET /api/system-health/history", rc.adminChain(systemHealthHandler.GetHistoricalMetrics))
	rc.mux.Handle("GET /api/system-health/subsystems", rc.adminChain(systemHealthHandler.GetSubsystems))
	rc.mux.Handle("POST /api/system-health/ai-check", rc.adminChain(systemHealthHandler.CheckAIHealth))

	kbOverviewSvc := adminkboverview.NewService(adminkboverview.NewStore(rc.infra.db.Main), rc.asynqInspector)
	kbOverviewHandler := adminkboverview.NewHandler(kbOverviewSvc)
	rc.mux.Handle("GET /api/admin/kb-overview", rc.adminChain(kbOverviewHandler.Overview))

	// Audit Logs — superadmin only
	auditLogsHandler := auditlogs.NewHandler(auditlogs.NewStore(rc.infra.db.Main))
	rc.mux.Handle("GET /api/admin/audit-logs", rc.superadminChain(auditLogsHandler.GetAuditLogs))

	// Admin AI Configs
	adminConfigsHandler := adminconfigs.NewHandler(adminconfigs.NewStore(rc.infra.db.Main), func() { rc.aiResolver.InvalidateAll() })
	rc.mux.Handle("GET /api/admin/configs", rc.adminChain(adminConfigsHandler.ListConfigs))
	rc.mux.Handle("POST /api/admin/configs", rc.adminChain(adminConfigsHandler.CreateConfig))
	rc.mux.Handle("PATCH /api/admin/configs/{id}", rc.adminChain(adminConfigsHandler.UpdateConfig))
	rc.mux.Handle("DELETE /api/admin/configs/{id}", rc.adminChain(adminConfigsHandler.DeleteConfig))
	rc.mux.Handle("POST /api/admin/configs/{id}/activate", rc.adminChain(adminConfigsHandler.ActivateConfig))
	rc.mux.Handle("POST /api/admin/configs/{id}/test", rc.adminChain(adminConfigsHandler.TestConfig))

	// Admin Auth Providers — OIDC hooks live in authhandler so the wrap +
	// invalidate functions are passed in here.
	adminProvidersStore := adminproviders.NewStore(rc.infra.db.Main)
	adminProvidersHandler := adminproviders.NewHandler(adminProvidersStore).WithOIDCHooks(
		authhandler.EncryptProviderSecret,
		adminProvidersStore.HasActiveOIDCProviderOtherThan,
		authhandler.InvalidateOIDCProviderCache,
	)
	rc.mux.Handle("GET /api/admin/auth-providers", rc.adminChain(adminProvidersHandler.ListAuthProviders))
	rc.mux.Handle("POST /api/admin/auth-providers", rc.adminChain(adminProvidersHandler.CreateAuthProvider))
	rc.mux.Handle("PATCH /api/admin/auth-providers/{id}", rc.adminChain(adminProvidersHandler.UpdateAuthProvider))
	rc.mux.Handle("DELETE /api/admin/auth-providers/{id}", rc.adminChain(adminProvidersHandler.DeleteAuthProvider))

	// Admin Users
	adminUsersHandler := adminusers.NewHandler(adminusers.NewStore(rc.infra.db.Main), rc.infra.blacklist, rc.cascadeDeleter)
	rc.mux.Handle("GET /api/admin/users", rc.adminChain(adminUsersHandler.ListUsers))
	rc.mux.Handle("PATCH /api/admin/users/{id}/role", rc.adminChain(adminUsersHandler.UpdateUserRole))
	rc.mux.Handle("DELETE /api/admin/users/{id}", rc.superadminChain(adminUsersHandler.DeleteUser))

	// Admin Global KBs
	adminGlobalKBsHandler := adminglobalkbs.NewHandlerWithDeleter(adminglobalkbs.NewStore(rc.infra.db.Main), rc.cascadeDeleter)
	rc.mux.Handle("GET /api/admin/global-kbs", rc.adminChain(adminGlobalKBsHandler.ListGlobalKBs))
	rc.mux.Handle("POST /api/admin/global-kbs", rc.adminChain(adminGlobalKBsHandler.CreateGlobalKB))
	rc.mux.Handle("PATCH /api/admin/global-kbs/{id}", rc.adminChain(adminGlobalKBsHandler.UpdateGlobalKB))
	rc.mux.Handle("DELETE /api/admin/global-kbs/{id}", rc.adminChain(adminGlobalKBsHandler.DeleteGlobalKB))
	rc.mux.Handle("GET /api/admin/global-kbs/{id}/editors", rc.adminChain(adminGlobalKBsHandler.ListEditors))
	rc.mux.Handle("POST /api/admin/global-kbs/{id}/editors", rc.adminChain(adminGlobalKBsHandler.AddEditor))
	rc.mux.Handle("DELETE /api/admin/global-kbs/{id}/editors/{userId}", rc.adminChain(adminGlobalKBsHandler.RemoveEditor))

	// Maintenance endpoints — bulk re-embed sweep, user-memory re-embed, and agent template upload.
	maintenanceHandler := adminmaintenance.NewHandler(rc.kbStore, rc.infra.asynqClient, longmem.NewPgStore(rc.infra.db.Main))
	rc.mux.Handle("POST /api/admin/reembed-all", rc.superadminChain(maintenanceHandler.ReembedAll))
	rc.mux.Handle("POST /api/admin/reembed-user-memory", rc.superadminChain(maintenanceHandler.ReembedUserMemory))
	rc.mux.Handle("POST /api/admin/agent/template", rc.superadminChain(maintenanceHandler.UploadAgentTemplate))
	rc.mux.Handle("POST /api/kb/{id}/reembed", rc.kbTuningChain(maintenanceHandler.ReembedKB))

	// Phase 1 §1.4 admin agent-metrics panel — per-(window,kb) outcome
	// distributions for the agentic / plan-execute / CRAG paths. Reads
	// from the agent_decisions table populated fire-and-forget at the
	// end of each chat run. Both the read store and the chat-handler
	// recorder share the *PgStore instance constructed in setupRoutes.
	agentMetricsHandler := adminagentmetrics.NewHandler(rc.agentDecisionStore)
	rc.mux.Handle("GET /api/admin/agent-metrics", rc.adminChain(agentMetricsHandler.GetMetrics))

	// Online feedback loop: admin review endpoint for the most net-negative
	// chunks. Backed by the same feedbackReader attached to the SearchService
	// so the two share a single DB pool and construction path.
	feedbackHandler := adminfeedback.NewHandler(rc.feedbackReader)
	rc.mux.Handle("GET /api/admin/feedback/chunks", rc.adminChain(feedbackHandler.GetNegativeChunks))

	// In-app eval runner — guarded by eval_ui_enabled site-config kill switch.
	registerAdminEvalRoutes(rc)
}

// ---------------------------------------------------------------------------
// registerAdminEvalRoutes — eval run management (kill-switch guarded)
// ---------------------------------------------------------------------------

// evalUIEnabled reads the eval_ui_enabled site-config key at startup time.
// Returns true when the key is absent, empty, or any truthy value, and
// false on DB error (fail-closed): if we can't reach the kill switch we
// don't expose the routes it gates.
//
// Setting "false", "0", "off", or "no" disables the eval endpoints at
// boot. The DB call is wrapped in a 5s timeout so a stuck/overloaded DB
// at boot doesn't block HTTP route registration indefinitely.
//
// Note: this runs once at startup. Flipping eval_ui_enabled at runtime
// requires a server restart — moving the guard into per-request handler
// logic would add a DB lookup to every admin call without enough payoff
// for an admin-only kill switch.
func evalUIEnabled(reader interface {
	GetSiteConfigValue(ctx context.Context, key string) (*string, error)
}) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	v, err := reader.GetSiteConfigValue(ctx, "eval_ui_enabled")
	if err != nil {
		slog.Warn("eval UI: site_configs read failed; fail-closed (eval routes disabled). Restart after DB recovery.", "error", err)
		return false
	}
	if v == nil {
		return true // key absent → default enabled
	}
	s := strings.ToLower(strings.TrimSpace(*v))
	return !(s == "false" || s == "0" || s == "off" || s == "no")
}

// registerAdminEvalRoutes wires the five eval-run HTTP endpoints.
// All endpoints are guarded by the admin middleware chain.
// The routes are skipped entirely when eval_ui_enabled is falsy in site_configs.
func registerAdminEvalRoutes(rc *routeCtx) {
	if !evalUIEnabled(siteconfig.NewStore(rc.infra.db.Main)) {
		slog.Info("eval UI disabled via eval_ui_enabled=false (or DB unreachable); skipping eval routes")
		return
	}

	evalStore := eval.NewStore(rc.infra.db.Main)
	goldenSetStore := eval.NewGoldenSetStore(rc.infra.db.Main)
	genJobStore := eval.NewGenJobStore(rc.infra.db.Main)
	h := admineval.NewHandler(
		evalStore,
		rc.kbStore,
		rc.chatStore,
		rc.infra.asynqClient,
		goldenSetStore,
		genJobStore,
	).WithKBOverrides(rc.kbConfigStore)

	rc.mux.Handle("POST /api/admin/eval/runs", rc.adminChain(h.CreateRun))
	rc.mux.Handle("GET /api/admin/eval/runs", rc.adminChain(h.ListRuns))
	rc.mux.Handle("GET /api/admin/eval/runs/{id}", rc.adminChain(h.GetRun))
	rc.mux.Handle("GET /api/admin/eval/runs/{id}/export", rc.adminChain(h.ExportRun))
	rc.mux.Handle("DELETE /api/admin/eval/runs/{id}", rc.adminChain(h.DeleteRun))

	rc.mux.Handle("POST /api/admin/eval/golden-sets", rc.adminChain(h.CreateGoldenSet))
	rc.mux.Handle("GET /api/admin/eval/golden-sets", rc.adminChain(h.ListGoldenSets))
	rc.mux.Handle("DELETE /api/admin/eval/golden-sets/{id}", rc.adminChain(h.DeleteGoldenSet))

	rc.mux.Handle("POST /api/admin/eval/golden-sets/generate", rc.adminChain(h.GenerateGoldenSet))
	rc.mux.Handle("GET /api/admin/eval/golden-sets/jobs", rc.adminChain(h.ListGenJobs))
	rc.mux.Handle("GET /api/admin/eval/golden-sets/{id}", rc.adminChain(h.GetGoldenSet))

	// Per-KB eval surface (api-user/admin/superadmin with KB edit permission).
	rc.mux.Handle("GET /api/kb/{id}/eval/golden-sets", rc.kbTuningChain(h.ListGoldenSetsForKB))
	rc.mux.Handle("POST /api/kb/{id}/eval/golden-sets", rc.kbTuningChain(h.CreateGoldenSetForKB))
	rc.mux.Handle("POST /api/kb/{id}/eval/golden-sets/generate", rc.kbTuningChain(h.GenerateGoldenSetForKB))
	rc.mux.Handle("GET /api/kb/{id}/eval/golden-sets/jobs", rc.kbTuningChain(h.ListGenJobsForKB))
	rc.mux.Handle("GET /api/kb/{id}/eval/golden-sets/{gsId}", rc.kbTuningChain(h.GetGoldenSetForKB))
	rc.mux.Handle("DELETE /api/kb/{id}/eval/golden-sets/{gsId}", rc.kbTuningChain(h.DeleteGoldenSetForKB))
	rc.mux.Handle("POST /api/kb/{id}/eval/runs", rc.kbTuningChain(h.CreateRunForKB))
	rc.mux.Handle("GET /api/kb/{id}/eval/runs", rc.kbTuningChain(h.ListRunsForKB))
	rc.mux.Handle("GET /api/kb/{id}/eval/runs/{runId}", rc.kbTuningChain(h.GetRunForKB))
	rc.mux.Handle("GET /api/kb/{id}/eval/runs/{runId}/export", rc.kbTuningChain(h.ExportRunForKB))
	rc.mux.Handle("DELETE /api/kb/{id}/eval/runs/{runId}", rc.kbTuningChain(h.DeleteRunForKB))
}

func registerKBRoutes(rc *routeCtx) {
	kbHandler := kb.NewHandler(rc.kbStore)
	kbUpdateHandler := kb.NewUpdateHandler(rc.kbStore, func(kbID string) { rc.aiResolver.Invalidate(kbID) })
	kbSharingHandler := kb.NewSharingHandler(rc.kbStore)
	kbDeleteHandler := kb.NewDeleteHandler(rc.cascadeDeleter)

	// KB listing (auth only)
	rc.mux.Handle("GET /api/kb", rc.authMw.Authenticate(http.HandlerFunc(kbHandler.ListKnowledgeBases)))
	rc.mux.Handle("GET /api/kb/global", rc.authMw.Authenticate(http.HandlerFunc(kbHandler.ListGlobalKnowledgeBases)))
	rc.mux.Handle("POST /api/kb", rc.authMw.Authenticate(http.HandlerFunc(kbHandler.CreateKnowledgeBase)))

	// Per-KB settings (registry-driven RAG-pipeline overrides)
	rc.mux.Handle("GET /api/kb/{id}/settings", rc.kbTuningChain(rc.kbConfigHandler.GetSettings))
	rc.mux.Handle("PUT /api/kb/{id}/settings", rc.kbTuningChain(rc.kbConfigHandler.PutSettings))
	rc.mux.Handle("DELETE /api/kb/{id}/settings/{key}", rc.kbTuningChain(rc.kbConfigHandler.DeleteSetting))

	// KB operations (need KB permission middleware)
	rc.mux.Handle("PATCH /api/kb/{id}", rc.kbEditChain(kbUpdateHandler.UpdateKB))
	// kbEditChain rejects shared-view users at the middleware boundary; the
	// handler itself further restricts deletion to owner-or-superadmin.
	rc.mux.Handle("DELETE /api/kb/{id}", rc.kbEditChain(kbDeleteHandler.DeleteKB))
	rc.mux.Handle("GET /api/kb/{id}/files", rc.kbViewChain(kbUpdateHandler.ListFiles))
	rc.mux.Handle("GET /api/kb/{id}/shares", rc.kbViewChain(kbSharingHandler.ListShares))
	rc.mux.Handle("POST /api/kb/{id}/share", rc.kbViewChain(kbSharingHandler.AddShare))
	rc.mux.Handle("DELETE /api/kb/{id}/share/{userId}", rc.kbViewChain(kbSharingHandler.RemoveShare))

	// Analytics
	analyticsHandler := analytics.NewHandler(analytics.NewStore(rc.infra.db.Main))
	rc.mux.Handle("GET /api/kb/{id}/analytics", rc.analyticsChain(analyticsHandler.GetDashboard))
	rc.mux.Handle("GET /api/kb/{id}/analytics/files", rc.analyticsChain(analyticsHandler.GetFiles))
	rc.mux.Handle("GET /api/kb/{id}/analytics/activity", rc.analyticsChain(analyticsHandler.GetActivity))
	rc.mux.Handle("GET /api/kb/{id}/analytics/chats", rc.analyticsChain(analyticsHandler.GetChats))
	rc.mux.Handle("GET /api/kb/{id}/analytics/generated", rc.analyticsChain(analyticsHandler.GetGenerated))
	rc.mux.Handle("GET /api/kb/{id}/analytics/retrieval-quality", rc.analyticsChain(analyticsHandler.GetRetrievalQuality))

	// RSS feeds (KB-level)
	rssHandler := rss.NewHandler(rss.NewStore(rc.infra.db.Main), rc.infra.asynqClient)
	rc.mux.Handle("POST /api/kb/{id}/rss", rc.kbEditChain(rssHandler.CreateRSSFeed))
	rc.mux.Handle("GET /api/kb/{id}/rss", rc.kbViewChain(rssHandler.ListRSSFeeds))
	rc.mux.Handle("PATCH /api/kb/{id}/rss/{feedId}", rc.kbEditChain(rssHandler.UpdateRSSFeed))
	rc.mux.Handle("DELETE /api/kb/{id}/rss/{feedId}", rc.kbEditChain(rssHandler.DeleteRSSFeed))
	rc.mux.Handle("POST /api/kb/{id}/rss/{feedId}/poll", rc.kbEditChain(rssHandler.TriggerPoll))

	// Confluence connections (auth only, user-level)
	confluenceHandler := confluence.NewHandler(confluence.NewStore(rc.infra.db.Main), rc.cfg.JWTSecret, rc.infra.asynqClient)
	rc.mux.Handle("GET /api/confluence/connections", rc.authMw.Authenticate(http.HandlerFunc(confluenceHandler.GetConnection)))
	rc.mux.Handle("POST /api/confluence/connections", rc.authMw.Authenticate(http.HandlerFunc(confluenceHandler.CreateConnection)))
	rc.mux.Handle("PUT /api/confluence/connections/{id}", rc.authMw.Authenticate(http.HandlerFunc(confluenceHandler.UpdateConnection)))
	rc.mux.Handle("DELETE /api/confluence/connections/{id}", rc.authMw.Authenticate(http.HandlerFunc(confluenceHandler.DeleteConnection)))
	rc.mux.Handle("POST /api/confluence/connections/{id}/verify", rc.authMw.Authenticate(http.HandlerFunc(confluenceHandler.VerifyConnection)))
	rc.mux.Handle("GET /api/confluence/spaces", rc.authMw.Authenticate(http.HandlerFunc(confluenceHandler.ListSpaces)))
	rc.mux.Handle("GET /api/confluence/spaces/{spaceKey}/pages", rc.authMw.Authenticate(http.HandlerFunc(confluenceHandler.ListSpacePages)))
	rc.mux.Handle("GET /api/confluence/spaces/{spaceKey}/pages/all", rc.authMw.Authenticate(http.HandlerFunc(confluenceHandler.ListAllSpacePages)))
	rc.mux.Handle("GET /api/confluence/pages/{pageId}/children", rc.authMw.Authenticate(http.HandlerFunc(confluenceHandler.ListPageChildren)))

	// Confluence sources (KB-level)
	rc.mux.Handle("POST /api/kb/{id}/confluence-sources", rc.kbEditChain(confluenceHandler.CreateSource))
	rc.mux.Handle("GET /api/kb/{id}/confluence-sources", rc.kbViewChain(confluenceHandler.ListSources))
	rc.mux.Handle("PATCH /api/kb/{id}/confluence-sources/{sourceId}", rc.kbEditChain(confluenceHandler.UpdateSource))
	rc.mux.Handle("DELETE /api/kb/{id}/confluence-sources/{sourceId}", rc.kbEditChain(confluenceHandler.DeleteSource))
	rc.mux.Handle("POST /api/kb/{id}/confluence-sources/{sourceId}/sync", rc.kbEditChain(confluenceHandler.TriggerSync))

	// Web search (KB view permission)
	webSearchHandler := websearch.NewHandler(rc.chatStore)
	rc.mux.Handle("POST /api/kb/{id}/websearch", rc.kbViewChain(webSearchHandler.WebSearch))

	// Knowledge-graph overview (frontend mind map) — KB view permission.
	// Returns empty nodes/edges on KBs without KG extraction.
	kgGraphHandler := kggraph.NewHandler(kg.NewPgStore(rc.infra.db.Main))
	rc.mux.Handle("GET /api/kb/{id}/graph", rc.kbViewChain(kgGraphHandler.GetGraph))

	// Add sources (KB edit permission)
	rc.mux.Handle("POST /api/kb/{id}/add-sources", rc.kbEditChain(rc.filesHandler.AddSources))
	// Fetch a URL and either preview its extracted content (previewOnly=true)
	// or ingest it into the KB via the URL-processing pipeline. Uses the
	// shared Fetcher when available (browser fallback + readability).
	rc.mux.Handle("POST /api/kb/{id}/fetch-url", rc.kbEditChain(rc.filesHandler.FetchURL))
	rc.mux.Handle("POST /api/kb/{id}/text", rc.kbEditChain(rc.filesHandler.AddTextSource))

	// Crawl is async: the POST enqueues a TypeCrawl job onto rag-quick and
	// returns a jobId immediately; the BFS crawl itself runs in a worker
	// (see internal/worker/crawl.go) so Chromium doesn't live in the
	// HTTP server process. The client polls the status endpoint until the
	// job reaches a terminal state and the pages list is in Redis.
	crawlerHandler := crawler.NewHandler(rc.infra.asynqClient, rc.asynqInspector, rc.infra.rdb.Client)
	rc.mux.Handle("POST /api/kb/{id}/crawl", rc.kbEditChain(crawlerHandler.Crawl))
	rc.mux.Handle("GET /api/kb/{id}/crawl/status/{jobId}", rc.kbViewChain(crawlerHandler.GetCrawlStatus))
}

func registerChatRoutes(ctx context.Context, rc *routeCtx, chatRL *middleware.RedisRateLimiter) {
	// Phase 2 §2.1: build the MCP registry and register the in-process
	// built-in tools (kb_search, web_search). Per-KB remote MCP servers
	// (if any) are added via Registry.Configure on demand from the chat
	// hot path or admin-knob flips. The registry is wired into the chat
	// handler as a tool dispatcher — actual use is gated by the
	// chat_use_mcp_tools site_config flag (default off).
	mcpRegistry := mcp.NewRegistry()
	mcpRegistry.RegisterBuiltin(builtin.NewKbSearch(rc.searchService))
	// AP-B1 Phase B retrieval-tier expansion: keyword_search (BM25-only,
	// fast exact-match), chunk_read (sibling navigation), and
	// document_outline (heading breadcrumbs). All three reuse the
	// shared per-KB chunks-table cache on SearchService — first call
	// per KB pays one probe query, subsequent calls hit the sync.Map.
	mcpRegistry.RegisterBuiltin(builtin.NewKeywordSearch(rc.searchService))
	mcpRegistry.RegisterBuiltin(builtin.NewCountMentions(rc.searchService))
	mcpRegistry.RegisterBuiltin(builtin.NewChunkRead(rc.searchService))
	mcpRegistry.RegisterBuiltin(builtin.NewDocumentOutline(rc.searchService))

	// AP-B2 non-retrieval tools: calculator (in-process arithmetic),
	// sql_query (read-only SELECT against allowlisted tables),
	// code_exec (gVisor-sandboxed Python). The sql_query tool MUST run
	// against a SELECT-only role — operators configure it via
	// JUSTRAG_DB_URL_READONLY. Fail closed: when that role is unset or
	// unreachable (sqlToolDB == nil) we register a disabled stub rather
	// than falling through to the read/write main pool, so flipping
	// chat_use_mcp_tools on without provisioning the role cannot expose
	// the RW pool to LLM-authored SQL. The tool's string validator is
	// defence-in-depth only; the role's GRANTs are the real boundary.
	mcpRegistry.RegisterBuiltin(builtin.NewCalculator())
	if rc.infra.sqlToolDB != nil {
		mcpRegistry.RegisterBuiltin(builtin.NewSQLQuery(rc.infra.sqlToolDB))
	} else {
		slog.Warn("sql_query tool registered in disabled state; set JUSTRAG_DB_URL_READONLY to a SELECT-only role to enable it")
		mcpRegistry.RegisterBuiltin(builtin.NewSQLQueryUnconfigured())
	}
	codeExecEnabled := func(ctx context.Context) bool {
		return chat.ChatCodeExecEnabled(ctx, rc.chatStore)
	}
	mcpRegistry.RegisterBuiltin(builtin.NewCodeExec(builtin.DefaultCodeExecConfig(), nil, codeExecEnabled))

	tabularEnabled := func(ctx context.Context) bool {
		return chat.ChatTabularQueryEnabled(ctx, rc.chatStore)
	}
	if rc.infra.sqlToolDB != nil {
		mcpRegistry.RegisterBuiltin(builtin.NewTableQuery(rc.infra.sqlToolDB, tabularEnabled))
	} else {
		mcpRegistry.RegisterBuiltin(builtin.NewTableQueryUnconfigured())
	}

	// AP-C3 graph_search: traverses kg_entities + kg_edges populated
	// by the AP-C1 ingestion stage. The KG read-side store is
	// constructed once and shared between the MCP tool (planner-
	// callable) and the AP-C4 router heuristic (chat pre-step).
	kgStore := kg.NewPgStore(rc.infra.db.Main)
	mcpRegistry.RegisterBuiltin(builtin.NewGraphSearch(kgStore))
	rc.kgStore = kgStore

	// AP-D1 long-term memory: per-user durable facts. Wired only
	// when chat_longmem_enabled flips it on; the chat handler's
	// nil-store branch short-circuits otherwise.
	longmemStore := longmem.NewPgStore(rc.infra.db.Main)
	webClient := research.NewWebClient(rc.chatStore, rc.sharedFetcher)
	mcpRegistry.RegisterBuiltin(builtin.NewWebSearch(webClient))

	// Phase 2 §2.3: chat-session memory store + the corresponding MCP
	// built-in tools (memory_read / memory_write). The session-memory
	// store is shared between the chat handler (which reads it at turn
	// start to prepend the Conversation memory: block) and the MCP
	// tools (which read/write it as the LLM directs).
	sessionMemoryStore := sessionmem.NewRedisStore(rc.infra.rdb.Client, sessionmem.DefaultTTL)
	mcpRegistry.RegisterBuiltin(builtin.NewMemoryRead(sessionMemoryStore))
	// Per-turn rate limiting is enforced via a context-attached
	// sessionmem.WriteCounter (see chat handler) — the production
	// memory_write tool reads it via sessionmem.FromContext. Passing
	// nil here means "no override; honor the per-turn counter the
	// caller attached".
	mcpRegistry.RegisterBuiltin(builtin.NewMemoryWrite(sessionMemoryStore, nil))

	// AP-A4: adapter that translates the kb.Store's ListKnowledgeBases
	// result into the candidate shape the chat-side router expects.
	// Lives here in routes (the wiring layer) instead of inside the
	// chat package so the chat package doesn't gain a dependency on
	// kb just for this one feature.
	kbRouterLister := &kbRouterCandidateAdapter{store: rc.kbStore}

	chatOpts := []chat.HandlerOption{
		chat.WithRedis(rc.infra.rdb.Client),
		chat.WithSiteConfigReader(rc.chatStore),
		chat.WithKBConfigStore(rc.kbConfigStore),
		chat.WithAsynqClient(rc.infra.asynqClient),
		chat.WithToolDispatcher(chat.NewMCPDispatcher(mcpRegistry)),
		chat.WithSessionMemory(sessionMemoryStore),
		chat.WithKBRouterCandidates(kbRouterLister),
		chat.WithKGStore(kgStore),
		chat.WithLongmemStore(longmemStore),
		chat.WithTabularCatalog(tabular.NewCatalog(rc.infra.db.Main)),
		// Phase F RAPTOR: vector.ChunkService implements the
		// RaptorDescendantsResolver shape via
		// GetRaptorDescendantLeafContentsAcrossDims. When summary
		// chunks land in the citation pool the validator expands
		// them to their leaf descendants for the n-gram check.
		chat.WithRaptorDescendantsResolver(rc.chunkService),
		// Corpus-table: vector.ChunkService satisfies CorpusChunkReader
		// via GetChunksByFileID — used by RunCorpusTableChat to assemble
		// full per-file text for comparison queries.
		chat.WithCorpusChunks(rc.chunkService),
		// In-chat document comparison: Redis-backed store for parsed
		// uploaded attachments (compared against a KB, never ingested).
		// Reuses the shared Redis client. TTL is read once at startup from
		// chat_compare_attachment_ttl_hours (default 24h); changes need a
		// restart. NewRedisStore guards ttl<=0 → chatattach.DefaultTTL.
		// parserFactory is left unset — the handler's lazy attachmentFactory()
		// falls back to parser.DefaultFactoryWith(nil).
		chat.WithAttachmentStore(chatattach.NewRedisStore(
			rc.infra.rdb.Client,
			time.Duration(chat.CompareAttachmentTTLHours(context.Background(), rc.chatStore))*time.Hour,
		)),
	}
	if rc.agentDecisionStore != nil {
		chatOpts = append(chatOpts, chat.WithDecisionRecorder(&decisionRecorderAdapter{store: rc.agentDecisionStore}))
	}
	chatHandler := chat.NewHandler(rc.chatStore, rc.aiResolver, rc.searchService, chatOpts...)

	// Phase 2 admin UI: load configured remote MCP servers from
	// site_configs at startup and expose status / reload endpoints so
	// admins can edit the config and apply it without restarting. The
	// registry is the same instance the chat handler dispatches against,
	// so a successful reload takes effect on the next chat request.
	//
	// Bounded context: without a deadline a slow/unreachable remote MCP
	// server would block server startup indefinitely. 15s comfortably
	// covers the per-server dialTimeout used by probeServers while still
	// failing fast enough that operators notice. Parented to the server ctx
	// (not context.Background) so a SIGTERM during init aborts the probe
	// instead of holding startup for the full 15s.
	mcpStartupCtx, mcpStartupCancel := context.WithTimeout(ctx, 15*time.Second)
	adminmcp.LoadAndApplyAtStartup(mcpStartupCtx, mcpRegistry, rc.chatStore)
	mcpStartupCancel()
	mcpAdminHandler := adminmcp.NewHandler(mcpRegistry, rc.chatStore)
	rc.mux.Handle("GET /api/admin/mcp/status", rc.adminChain(mcpAdminHandler.GetStatus))
	rc.mux.Handle("POST /api/admin/mcp/reload", rc.adminChain(mcpAdminHandler.PostReload))
	genContentHandler := gencontent.NewHandler(rc.genContentStore)

	// Generated content listing (high-frequency polling)
	rc.mux.Handle("GET /api/kb/{id}/generated-content", rc.kbViewChain(genContentHandler.ListGenContent))

	// Chat — listing
	rc.mux.Handle("GET /api/kb/{id}/chats", rc.kbViewChain(chatHandler.ListChats))

	// Chat — messages and deletion (auth only, ownership checked in handler)
	rc.mux.Handle("GET /api/chats/{id}/messages", rc.authMw.Authenticate(http.HandlerFunc(chatHandler.GetMessages)))
	rc.mux.Handle("DELETE /api/chats/{id}", rc.authMw.Authenticate(http.HandlerFunc(chatHandler.DeleteChat)))

	// Chat — send message (KB view permission). The rate limiter wraps the
	// auth chain from the outside so unauthenticated requests don't consume
	// quota — matching the pattern used by registerContentGenRoutes.
	rc.mux.Handle("POST /api/kb/{id}/chat", chatRL.Middleware(rc.kbViewChain(chatHandler.SendMessage)))
	// In-chat document comparison: upload a document to compare against the KB.
	// Same auth + KB-view ACL chain and "chat" rate limiter as the chat send route —
	// the upload triggers synchronous parsing + a Redis write, so it must be metered.
	rc.mux.Handle("POST /api/kb/{id}/chat/attachment", chatRL.Middleware(rc.kbViewChain(chatHandler.UploadAttachment)))

	// Chat — feedback
	rc.mux.Handle("POST /api/kb/{id}/chats/{chatId}/messages/{messageId}/feedback", rc.kbViewChain(chatHandler.SubmitFeedback))
}

func registerFileRoutes(rc *routeCtx) {
	rc.mux.Handle("GET /api/files/{id}/download", rc.authMw.Authenticate(http.HandlerFunc(rc.filesHandler.Download)))
	rc.mux.Handle("DELETE /api/files/{id}", rc.authMw.Authenticate(http.HandlerFunc(rc.filesHandler.Delete)))
	rc.mux.Handle("POST /api/files/{id}/retry", rc.authMw.Authenticate(http.HandlerFunc(rc.filesHandler.Retry)))
	rc.mux.Handle("POST /api/kb/{id}/files", rc.kbEditChain(rc.filesHandler.Upload))
	rc.mux.Handle("POST /api/kb/{id}/files/retry-failed", rc.kbEditChain(rc.filesHandler.RetryFailed))

	// Generated content — CRUD + download + stream (auth required)
	genContentHandler := gencontent.NewHandler(rc.genContentStore)
	rc.mux.Handle("PATCH /api/generated-content/{id}", rc.authMw.Authenticate(http.HandlerFunc(genContentHandler.UpdateGenContent)))
	rc.mux.Handle("DELETE /api/generated-content/{id}", rc.authMw.Authenticate(http.HandlerFunc(genContentHandler.DeleteGenContent)))
	rc.mux.Handle("GET /api/generated-content/{id}/download", rc.authMw.Authenticate(http.HandlerFunc(genContentHandler.DownloadGenContent)))
	rc.mux.Handle("GET /api/generated-content/{id}/stream", rc.authMw.Authenticate(http.HandlerFunc(genContentHandler.StreamGenContent)))
}

func registerContentGenRoutes(rc *routeCtx, generateRL *middleware.RedisRateLimiter) {
	contentGenHandler := contentgen.NewHandler(rc.genContentStore, rc.aiResolver, rc.searchService, rc.infra.asynqClient, rc.asynqInspector, rc.infra.rdb.Client)
	// Chart generation hybrid SQL path needs the tabular catalog + the
	// SELECT-only pool. When the read-only pool is unconfigured the handler
	// falls back to the LLM-from-context chart path.
	if rc.infra.sqlToolDB != nil {
		contentGenHandler.SetChartDeps(tabular.NewCatalog(rc.infra.db.Main), rc.infra.sqlToolDB)
	}

	rc.mux.Handle("POST /api/kb/{id}/generate/cards", generateRL.Middleware(rc.kbViewChain(contentGenHandler.GenerateCards)))
	rc.mux.Handle("POST /api/kb/{id}/generate/quiz", generateRL.Middleware(rc.kbViewChain(contentGenHandler.GenerateQuiz)))
	rc.mux.Handle("POST /api/kb/{id}/generate/presentation", generateRL.Middleware(rc.kbViewChain(contentGenHandler.GeneratePresentation)))
	rc.mux.Handle("POST /api/kb/{id}/generate/podcast", generateRL.Middleware(rc.kbViewChain(contentGenHandler.GeneratePodcast)))
	rc.mux.Handle("GET /api/kb/{id}/generate/podcast/status/{jobId}", rc.kbViewChain(contentGenHandler.GetPodcastStatus))
	rc.mux.Handle("POST /api/kb/{id}/generate/chart", generateRL.Middleware(rc.kbViewChain(contentGenHandler.GenerateChart)))
	rc.mux.Handle("POST /api/kb/{id}/generate/analysis", generateRL.Middleware(rc.kbViewChain(contentGenHandler.GenerateAnalysis)))
	rc.mux.Handle("POST /api/kb/{id}/generate/abstract", generateRL.Middleware(rc.kbViewChain(contentGenHandler.GenerateAbstract)))

	// Markdown-text study artifacts (briefing doc, FAQ, study guide, timeline)
	// share one generic handler; each is one ArtifactSpec in the registry.
	for _, spec := range contentgen.TextArtifacts {
		rc.mux.Handle("POST /api/kb/{id}/generate/"+spec.Type, generateRL.Middleware(rc.kbViewChain(contentGenHandler.GenerateArtifact(spec))))
	}
}

// dispatchResearchAction resolves the request-time conflict between the
// /api/research/deep/{deepChatId} subtree and the action endpoints
// /api/research/{researchId}/{status|report}. Both share the same
// 4-segment shape, so ServeMux can't disambiguate them statically (see
// the comment at the registration site).
//
// The literal "deep" can never collide with a real session ID: research
// session IDs are the `chats.id` column, typed `uuid PRIMARY KEY DEFAULT
// gen_random_uuid()` (migration 0000). Postgres rejects any non-UUID value
// for that column, so a generated ID equal to "deep" is impossible at the
// schema level — this is a hard guarantee, not a naming convention.
func dispatchResearchAction(h *research.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		researchID := r.PathValue("researchId")
		action := r.PathValue("action")

		if researchID == "deep" {
			// Path is /api/research/deep/{deepChatId}; the captured
			// {action} segment is actually the deep chat ID.
			r.SetPathValue("deepChatId", action)
			h.DeepChatSSE(w, r)
			return
		}

		switch action {
		case "status":
			h.GetStatus(w, r)
		case "report":
			r.SetPathValue("sessionId", researchID)
			h.ExportReport(w, r)
		default:
			http.NotFound(w, r)
		}
	}
}

func registerResearchRoutes(rc *routeCtx, researchRL *middleware.RedisRateLimiter) {
	relay := sserelay.New(rc.infra.rdb.Client, rc.infra.asynqClient)
	researchHandler := research.NewHandler(rc.chatStore, relay, rc.aiResolver, rc.searchService, rc.infra.rdb.Client)
	academicHandler := academic.NewHandler(&academicDeps{PGStore: rc.chatStore, filesStore: rc.filesStore}, relay, rc.infra.rdb.Client, rc.infra.stor, rc.infra.asynqClient, rc.sharedFetcher)

	// Misc endpoints — enhance, export (auth required)
	miscHandler := misc.NewHandler(rc.aiResolver)
	// Both routes block on a single synchronous LLM call. The 90s ceiling
	// cancels the upstream call and returns a 503 before the server's 120s
	// WriteTimeout would silently drop the connection with the call still
	// running.
	llmTimeout := middleware.Timeout(90 * time.Second)
	rc.mux.Handle("POST /api/enhance", rc.authMw.Authenticate(llmTimeout(http.HandlerFunc(miscHandler.Enhance))))
	miscHandler.SetSiteConfig(rc.chatStore)
	rc.mux.Handle("POST /api/describe-image", rc.authMw.Authenticate(llmTimeout(http.HandlerFunc(miscHandler.DescribeImage))))
	// Export endpoints matching the frontend contract (POST /api/kb/{id}/export/...).
	rc.mux.Handle("POST /api/kb/{id}/export/docx", rc.kbViewChain(miscHandler.ExportDocx))
	rc.mux.Handle("POST /api/kb/{id}/export/bibtex", rc.kbViewChain(miscHandler.ResearchBibtex))
	rc.mux.Handle("POST /api/kb/{id}/export/academic-bibtex", rc.kbViewChain(miscHandler.AcademicBibtex))
	// Legacy routes (kept for backwards compat with any direct API callers).
	rc.mux.Handle("POST /api/research/{sessionId}/bibtex", rc.authMw.Authenticate(http.HandlerFunc(miscHandler.ResearchBibtex)))
	rc.mux.Handle("POST /api/academic-research/{sessionId}/bibtex", rc.authMw.Authenticate(http.HandlerFunc(miscHandler.AcademicBibtex)))

	// Academic research endpoints
	rc.mux.Handle("POST /api/kb/{id}/academic-search", rc.kbViewChain(academicHandler.Search))
	rc.mux.Handle("POST /api/kb/{id}/academic-research", rc.kbViewChain(academicHandler.StartResearch))
	rc.mux.Handle("POST /api/academic-research/{researchId}/papers/add", rc.authMw.Authenticate(http.HandlerFunc(academicHandler.AddPapers)))

	// Research endpoints sharing /api/research/{x}/{y}.
	//
	// Go's ServeMux refuses to register both
	//   GET /api/research/deep/{deepChatId}
	//   GET /api/research/{researchId}/{status|report}
	// because /api/research/deep/status matches both with no clear winner
	// (literal "deep" wins segment 3, literal "status" wins segment 4) —
	// it panics at startup. The single combined pattern below dispatches
	// at request time instead. A real research session ID is a UUID, so
	// the literal "deep" cannot collide with one in practice.
	rc.mux.Handle("GET /api/research/{researchId}/{action}", rc.authMw.Authenticate(http.HandlerFunc(dispatchResearchAction(researchHandler))))
	rc.mux.Handle("POST /api/kb/{id}/research", researchRL.Middleware(rc.kbViewChain(researchHandler.StartResearch)))
	rc.mux.Handle("POST /api/kb/{id}/web-research", researchRL.Middleware(rc.kbViewChain(researchHandler.StartWebResearch)))
	rc.mux.Handle("POST /api/research/{researchId}/abort", rc.authMw.Authenticate(http.HandlerFunc(researchHandler.Abort)))
}

func registerPublicAPIRoutes(rc *routeCtx, apiRL *middleware.RedisRateLimiter) {
	apiKeyAuth := apikeyauth.NewMiddleware(apikeyauth.NewStore(rc.infra.db.Main))
	openaiHandler := openaicompat.NewHandler(&openaiDeps{PGStore: rc.kbStore, kbAccessStore: rc.kbAccessStore}, rc.aiResolver, rc.searchService)

	rc.mux.Handle("GET /openai/v1/models", apiRL.Middleware(apiKeyAuth.Authenticate(http.HandlerFunc(openaiHandler.ListModels))))
	rc.mux.Handle("POST /openai/v1/chat/completions", apiRL.Middleware(apiKeyAuth.Authenticate(http.HandlerFunc(openaiHandler.ChatCompletions))))

	publicHandler := publicapi.NewHandler(&publicAPIDeps{PGStore: rc.chatStore, kbStore: rc.kbStore}, rc.aiResolver, rc.searchService)
	publicHandler.SetResearchDeps(rc.chatStore, rc.infra.rdb.Client)

	rc.mux.Handle("GET /api/v1/kb", apiRL.Middleware(apiKeyAuth.Authenticate(http.HandlerFunc(publicHandler.ListKBs))))
	rc.mux.Handle("GET /api/v1/kb/{id}/chats", apiRL.Middleware(apiKeyAuth.Authenticate(
		rc.kbMw.RequireKBPermission("view")(http.HandlerFunc(publicHandler.ListChats)))))
	rc.mux.Handle("GET /api/v1/kb/{id}/chats/{chatId}/messages", apiRL.Middleware(apiKeyAuth.Authenticate(
		rc.kbMw.RequireKBPermission("view")(http.HandlerFunc(publicHandler.GetMessages)))))
	rc.mux.Handle("POST /api/v1/kb/{id}/chat", apiRL.Middleware(apiKeyAuth.Authenticate(
		rc.kbMw.RequireKBPermission("view")(http.HandlerFunc(publicHandler.SendMessage)))))
	rc.mux.Handle("POST /api/v1/kb/{id}/research", apiRL.Middleware(apiKeyAuth.Authenticate(
		rc.kbMw.RequireKBPermission("view")(http.HandlerFunc(publicHandler.Research)))))
}

func registerMiscRoutes(rc *routeCtx) {
	// Data Explorer — disabled (requires DuckDB/CGo, not ported)
	dataExplorerDisabled := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotImplemented, "Data Explorer is not available (DuckDB not ported to Go backend)")
	})
	rc.mux.Handle("GET /api/kb/{id}/data-explorer/schema", rc.kbViewChain(dataExplorerDisabled))
	rc.mux.Handle("POST /api/kb/{id}/data-explorer/query", rc.kbViewChain(dataExplorerDisabled))
	rc.mux.Handle("POST /api/kb/{id}/data-explorer/export", rc.kbViewChain(dataExplorerDisabled))
}

func registerStaticRoutes(rc *routeCtx) {
	// Resolve once at startup so route handlers don't depend on the
	// process working directory. filepath.Abs returns an error only when
	// os.Getwd does, so on success there's nothing to handle; on failure
	// we fall back to the relative path with a warning.
	logoDir := "uploads/logo"
	if abs, err := filepath.Abs(logoDir); err == nil {
		logoDir = abs
	} else {
		slog.Warn("could not resolve uploads/logo to absolute path; serving relative", "error", err)
	}
	logoFS := http.FileServer(http.Dir(logoDir))

	// Serve uploaded logos (public, no auth required)
	rc.mux.Handle("GET /uploads/logo/", http.StripPrefix("/uploads/logo/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		logoFS.ServeHTTP(w, r)
	})))
}

func registerFrontendServing(rc *routeCtx) error {
	if rc.cfg.IsProduction() {
		// Resolve once so request-time os.Stat doesn't depend on CWD.
		distDir := "client/dist"
		if abs, err := filepath.Abs(distDir); err == nil {
			distDir = abs
		} else {
			slog.Warn("could not resolve client/dist to absolute path; serving relative", "error", err)
		}
		setupStaticServing(rc.mux, distDir)
		return nil
	}

	legacyURL := fmt.Sprintf("http://%s:%d", rc.cfg.LegacyHost, rc.cfg.LegacyPort)
	legacyProxy, err := proxy.New(legacyURL)
	if err != nil {
		return fmt.Errorf("create dev proxy: %w", err)
	}
	rc.mux.Handle("/", legacyProxy)
	return nil
}

// setupStaticServing registers a catch-all handler on mux that serves the
// React SPA from staticDir. API and OpenAI routes are left to their own
// handlers (already registered before this is called); the handler returns
// 404 for those prefixes so the mux falls through.
func setupStaticServing(mux *http.ServeMux, staticDir string) {
	fs := http.FileServer(http.Dir(staticDir))

	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// API and OpenAI routes have dedicated handlers — skip here.
		if strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/openai/") {
			http.NotFound(w, r)
			return
		}

		// Health / ready / version are already registered — skip.
		if path == "/health" || path == "/ready" || path == "/version" {
			http.NotFound(w, r)
			return
		}

		// Uploads are served by their own handler — skip.
		if strings.HasPrefix(path, "/uploads/") {
			http.NotFound(w, r)
			return
		}

		// Try to serve a real file from the dist directory.
		filePath := filepath.Join(staticDir, path)
		if _, err := os.Stat(filePath); err == nil {
			// File exists — set appropriate cache headers.
			switch {
			case strings.HasSuffix(path, ".html"):
				w.Header().Set("Cache-Control", "no-cache")
			case isServiceWorkerAsset(path):
				// vite-plugin-pwa emits the service-worker bootstrap files at
				// the dist root WITHOUT a content hash. Serving them immutable
				// pins the SW script in the browser (and any intermediary
				// proxy) for a year, so a new deployment is never discovered
				// and every PWA client stays frozen on the bundle it first
				// cached. Force revalidation so update detection works.
				w.Header().Set("Cache-Control", "no-cache")
			default:
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			fs.ServeHTTP(w, r)
			return
		}

		// SPA catch-all: serve index.html for all non-file routes.
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, filepath.Join(staticDir, "index.html"))
	})
}

// isServiceWorkerAsset reports whether path is one of the non-hashed PWA
// bootstrap files vite-plugin-pwa emits at the dist root. These gate update
// discovery for every PWA client, so they must never be served immutable.
// Content-hashed precache chunks (workbox-<hash>.js) stay immutable — only
// the stable entry points need revalidation.
func isServiceWorkerAsset(path string) bool {
	switch path {
	case "/sw.js", "/registerSW.js", "/manifest.webmanifest":
		return true
	default:
		return false
	}
}

// decisionRecorderAdapter implements chat.DecisionRecorder by forwarding
// to the adminagentmetrics PgStore. Lives in this package (routes layer)
// so the chat package keeps no dependency on adminagentmetrics — and the
// per-package ToolCallRecord / ToolCallEntry slice conversion lands in
// one place.
//
// The conversion is a flat field-by-field copy. If the two structs ever
// drift, a compile-time error here is the canary; this adapter is the
// only call site that touches both.
type decisionRecorderAdapter struct {
	store *adminagentmetrics.PgStore
}

func (a *decisionRecorderAdapter) Record(ctx context.Context, kbID, mode, outcome string, hops, rounds, latencyMs int, toolCalls []chat.ToolCallRecord) {
	if a.store == nil {
		return
	}
	entries := make([]adminagentmetrics.ToolCallEntry, len(toolCalls))
	for i, c := range toolCalls {
		entries[i] = adminagentmetrics.ToolCallEntry{
			Tool:       c.Tool,
			DurationMs: c.DurationMs,
			Status:     c.Status,
		}
	}
	a.store.Record(ctx, kbID, mode, outcome, hops, rounds, latencyMs, entries)
}

// kbRouterCandidateAdapter implements chat.KBRouterCandidateLister by
// re-using kb.Store's existing ListKnowledgeBases query (which already
// returns owned + shared + global-published rows). The translation is
// trivial — strip everything except id, name, description.
//
// Lives in this package (not chat/) so the chat package stays free of
// a kb dependency. The adapter caps the candidate list at 100 — KB
// fan-outs beyond that don't fit one router LLM call anyway.
type kbRouterCandidateAdapter struct {
	store *kb.PGStore
}

func (a *kbRouterCandidateAdapter) ListKBRouterCandidates(ctx context.Context, userID string) ([]prompts.KBRouterCandidate, error) {
	rows, err := a.store.ListKnowledgeBases(ctx, userID, 100, 0)
	if err != nil {
		return nil, err
	}
	out := make([]prompts.KBRouterCandidate, 0, len(rows))
	for _, r := range rows {
		desc := ""
		if r.Description != nil {
			desc = *r.Description
		}
		out = append(out, prompts.KBRouterCandidate{
			ID:          r.ID,
			Name:        r.Name,
			Description: desc,
		})
	}
	return out, nil
}
