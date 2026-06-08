package config

import (
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

type DBConfig struct {
	Host              string
	Port              int
	User              string
	Password          string
	Name              string
	MaxConns          int
	MinConns          int
	IdleTimeout       time.Duration
	MaxConnLifetime   time.Duration
	ConnectionTimeout time.Duration
	StatementTimeout  time.Duration
	// SlowQueryThreshold logs any pgx query/exec exceeding this duration
	// at warn level (with sql + duration_ms) via the pool's Tracer hook.
	// Zero disables the tracer. Set via DB_SLOW_QUERY_THRESHOLD_MS.
	SlowQueryThreshold time.Duration
	// HealthCheckPeriod is how often pgxpool pings idle connections to
	// reap dead ones. Tune this against the load balancer / PgBouncer
	// idle timeout in front of Postgres — a longer interval than the
	// upstream idle limit means the pool will hand out half-closed
	// connections before noticing they're dead, surfacing as
	// "unexpected EOF" on the first query. Set via DB_HEALTH_CHECK_PERIOD_MS;
	// default 30s matches pgx's documented default.
	HealthCheckPeriod time.Duration
	// ConnectAttempts is how many times Connect pings the DB on startup
	// before giving up. The loop sleeps between attempts with exponential
	// backoff (1,2,4,8,… s), so N attempts ride out roughly (2^(N-1)-1)s of
	// DB-not-ready. Default 5 (~15s) suits Docker Compose; raise via
	// DB_CONNECT_ATTEMPTS for Kubernetes where a post-migration init
	// container can delay Postgres readiness past 15s. Zero/negative falls
	// back to the built-in default so test fixtures that bypass Load still work.
	ConnectAttempts int
}

type RedisConfig struct {
	Host     string
	Port     int
	Password string
	DB       int
	// PoolSize is the maximum number of connections each Redis client (the
	// shared client + the Asynq client) may open. Set explicitly to override
	// go-redis's default of 10×GOMAXPROCS, which on a 16-CPU container would
	// be 160 — far larger than the workload needs and out of step with the
	// pgxpool cap (~20). Set via REDIS_POOL_SIZE; default is sized to roughly
	// match WorkerConcurrency × 2 plus a margin for HTTP-side traffic.
	PoolSize int
}

type RateLimitConfig struct {
	Window time.Duration
	Max    int
}

type S3Config struct {
	Endpoint  string // S3_ENDPOINT — enables S3 storage when set
	AccessKey string // S3_ACCESS_KEY
	SecretKey string // S3_SECRET_KEY
	Bucket    string // S3_BUCKET
	Region    string // S3_REGION
}

type PprofConfig struct {
	Enabled bool   // PPROF_ENABLED
	Addr    string // PPROF_ADDR (default "127.0.0.1:6060")

	// MutexProfileFraction is the 1-in-N sampling rate for mutex contention
	// events passed to runtime.SetMutexProfileFraction. Default 0 disables
	// sampling and makes /debug/pprof/mutex return an empty profile —
	// appropriate for an always-on pprof endpoint. Operators bump this to
	// 5 (1-in-5) for a targeted profiling session; higher rates add
	// measurable overhead under heavy contention. PPROF_MUTEX_PROFILE_FRACTION.
	MutexProfileFraction int

	// BlockProfileRate is the threshold in nanoseconds passed to
	// runtime.SetBlockProfileRate. Default 0 disables sampling so
	// /debug/pprof/block returns empty. 1000 (1µs) samples every blocking
	// event longer than a microsecond — the practical value for tracking
	// channel/IO contention but only worth paying for targeted sessions.
	// PPROF_BLOCK_PROFILE_RATE.
	BlockProfileRate int
}

type Config struct {
	Env            string
	Port           int
	LegacyPort     int
	LegacyHost     string
	DB             DBConfig
	VectorDB       DBConfig
	Redis          RedisConfig
	S3             S3Config
	Pprof          PprofConfig
	JWTSecret      string
	AllowedOrigins []string

	// Worker settings
	WorkerQueues      []string // WORKER_QUEUES — if set, only process these queues
	WorkerConcurrency int      // WORKER_CONCURRENCY — total goroutines (default 8)
	WorkerMaintenance bool     // WORKER_MAINTENANCE — run maintenance tasks (default true)
	WorkerHealthPort  int      // WORKER_HEALTH_PORT — health endpoint port (default 8081)

	// WorkerDBStatementTimeout overrides DB.StatementTimeout for the worker's
	// DB pool. Worker tasks (batch embedding inserts, full-text tsvector
	// updates, vector index ops) routinely exceed the server's request-path
	// 120s wall; hitting that wall mid-task would kill the job. Default 0
	// (no limit). Set via WORKER_DB_STATEMENT_TIMEOUT_MS.
	WorkerDBStatementTimeout time.Duration

	// JustFindUserDataDir is the host path mounted into the worker for the
	// Tier 3 persistent rod profile (Anubis cookie). Set via
	// JUSTFIND_USER_DATA_DIR; defaults to /app/data/justfind-profile to
	// match the K8s/Docker volume mount.
	JustFindUserDataDir string

	// FetcherAllowNoSandbox enables Chromium's --no-sandbox flag in the
	// Tier 2/3 rod browser. Required when the server/worker binary runs
	// as root (the default in our Alpine container images), because
	// Chromium refuses to start as root without it. MUST stay false for
	// local `go run` or bare-metal deployments — the sandbox is a real
	// defence-in-depth layer. Set via FETCHER_ALLOW_NO_SANDBOX=true; the
	// Dockerfile flips it to true in the image entrypoint.
	FetcherAllowNoSandbox bool

	RateLimitAPI      RateLimitConfig
	RateLimitUpload   RateLimitConfig
	RateLimitGenerate RateLimitConfig
	RateLimitLogin    RateLimitConfig
	RateLimitCrawl    RateLimitConfig
	RateLimitSearch   RateLimitConfig

	// MaxRequestBodyBytes is the global cap applied by middleware.MaxBytesExcept on
	// non-upload routes. Upload routes set their own larger caps via
	// http.MaxBytesReader in their handler. Default 1 MiB; env MAX_REQUEST_BODY_BYTES.
	MaxRequestBodyBytes int64

	// CSPHeader overrides the built-in Content-Security-Policy header value.
	// Empty falls back to middleware.DefaultCSP. The default policy whitelists
	// an inline script via a sha256 hash that matches the frontend's Vite
	// build; if the build changes that inline content the hash drifts and
	// enforcing browsers silently refuse to execute it. Set CSP_HEADER at
	// deploy time to patch a stale hash (or relax the policy temporarily)
	// without a backend rebuild.
	CSPHeader string

	// EmbeddingCacheTTL is how long Redis keeps a (model, text) → vector entry
	// before it expires and the next query re-hits the embedding API. The
	// query-side cache is the only consumer; corpus embeddings are persisted
	// in Postgres. Default 7 days; env EMBEDDING_CACHE_TTL_HOURS.
	EmbeddingCacheTTL time.Duration

	// SQLToolReadOnlyURL is the connection string for the dedicated
	// SELECT-only Postgres role the sql_query MCP tool runs against. The
	// tool's string validator is defence-in-depth only; the authoritative
	// boundary is this role's GRANTs. When unset the sql_query tool is
	// registered in a disabled state that returns a "not configured" error
	// instead of silently falling through to the main read/write pool — so
	// enabling chat_use_mcp_tools without provisioning this role cannot
	// expose the RW pool to LLM-authored SQL. Env JUSTRAG_DB_URL_READONLY.
	SQLToolReadOnlyURL string

	// TrustedProxyHopCount is the number of trusted reverse-proxy hops in
	// front of this server. The rate-limiter parses X-Forwarded-For as
	// "client, proxy1, …, proxyN" and treats the rightmost N entries as
	// trusted, taking the client IP from XFF[len-N]. With value 0 (default)
	// XFF and X-Real-IP are ignored entirely and the rate-limit key falls
	// back to the TCP peer — required to prevent clients spoofing their
	// rate-limit identity. Set TRUSTED_PROXY_HOP_COUNT=1 when behind a
	// single reverse proxy (nginx, ALB, CDN → server).
	TrustedProxyHopCount int

	// LogVerbose disables the access-log suppression of health checks and
	// high-frequency frontend polling endpoints (/api/kb/{id}/files, /chats,
	// etc.). Off by default so a single open KB tab doesn't emit ~20
	// lines/sec. Set LOG_VERBOSE=true during incident investigation to see
	// every request, then unset it. Env LOG_VERBOSE.
	LogVerbose bool
}

func (c *Config) IsProduction() bool {
	return c.Env == "production"
}

func Load() (*Config, error) {
	// envInt collects parse errors into this slice so we can report them all at once.
	var errs []string
	mustInt := func(key string, fallback int) int {
		v, err := envInt(key, fallback)
		if err != nil {
			errs = append(errs, err.Error())
		}
		return v
	}
	// mustInt64 parses a 64-bit env var. Use it for sizes/limits stored as
	// int64 (e.g. byte caps) so the value never narrows through int on a
	// 32-bit build and the int64 intent is explicit at the call site.
	mustInt64 := func(key string, fallback int64) int64 {
		v := os.Getenv(key)
		if v == "" {
			return fallback
		}
		i, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s=%q is not a valid integer", key, v))
			return fallback
		}
		return i
	}
	// mustDurationMs reads an integer-millisecond env var, returning the
	// supplied default time.Duration when the env var is unset or invalid.
	// Use it for legacy *_MS keys; the default is expressed using
	// time.Second / time.Minute so the call site reads correctly.
	mustDurationMs := func(key string, fallback time.Duration) time.Duration {
		if os.Getenv(key) == "" {
			return fallback
		}
		ms, err := envInt(key, int(fallback/time.Millisecond))
		if err != nil {
			errs = append(errs, err.Error())
			return fallback
		}
		return time.Duration(ms) * time.Millisecond
	}

	envValue, envSource := envOrMultiWithSource([]string{"GO_ENV", "NODE_ENV"}, "development")
	if envSource == "NODE_ENV" {
		// NODE_ENV is a leftover from the Node.js migration and will be
		// removed on the next breaking-change release. Surface it now so
		// operators have time to migrate their deploy scripts.
		slog.Warn("NODE_ENV is deprecated; set GO_ENV instead. NODE_ENV fallback will be removed in a future release.")
	}

	cfg := &Config{
		Env:        envValue,
		Port:       mustInt("PORT", 3000),
		LegacyPort: mustInt("LEGACY_PORT", 3001),
		LegacyHost: envOr("LEGACY_HOST", "localhost"),
		DB: DBConfig{
			Host:              envOr("DB_HOST", "localhost"),
			Port:              mustInt("DB_PORT", 5432),
			User:              envOr("DB_USER", "postgres"),
			Password:          envOr("DB_PASSWORD", "postgres"),
			Name:              envOr("DB_NAME", "postgres"),
			MaxConns:          mustInt("DB_POOL_MAX", 20),
			MinConns:          mustInt("DB_POOL_MIN", 4),
			IdleTimeout:       mustDurationMs("DB_IDLE_TIMEOUT", 30*time.Second),
			MaxConnLifetime:   mustDurationMs("DB_CONN_MAX_LIFETIME", 30*time.Minute),
			ConnectionTimeout: mustDurationMs("DB_CONNECTION_TIMEOUT", 10*time.Second),
			StatementTimeout:  mustDurationMs("DB_STATEMENT_TIMEOUT_MS", 2*time.Minute),
			// Default 200 ms — anything slower than this on the hot path
			// is worth surfacing. Set DB_SLOW_QUERY_THRESHOLD_MS=0 to disable.
			SlowQueryThreshold: mustDurationMs("DB_SLOW_QUERY_THRESHOLD_MS", 200*time.Millisecond),
			HealthCheckPeriod:  mustDurationMs("DB_HEALTH_CHECK_PERIOD_MS", 30*time.Second),
			ConnectAttempts:    mustInt("DB_CONNECT_ATTEMPTS", 5),
		},
		Redis: RedisConfig{
			Host:     envOr("REDIS_HOST", "localhost"),
			Port:     mustInt("REDIS_PORT", 6379),
			Password: os.Getenv("REDIS_PASSWORD"),
			DB:       mustInt("REDIS_DB", 0),
			PoolSize: mustInt("REDIS_POOL_SIZE", 32),
		},
		JWTSecret: os.Getenv("JWT_SECRET"),

		RateLimitAPI:      RateLimitConfig{Window: mustDurationMs("RATE_LIMIT_WINDOW_MS", 15*time.Minute), Max: mustInt("RATE_LIMIT_MAX", 100)},
		RateLimitUpload:   RateLimitConfig{Window: mustDurationMs("UPLOAD_LIMIT_WINDOW_MS", 15*time.Minute), Max: mustInt("UPLOAD_LIMIT_MAX", 30)},
		RateLimitGenerate: RateLimitConfig{Window: mustDurationMs("GENERATE_LIMIT_WINDOW_MS", 15*time.Minute), Max: mustInt("GENERATE_LIMIT_MAX", 20)},
		RateLimitLogin:    RateLimitConfig{Window: mustDurationMs("LOGIN_LIMIT_WINDOW_MS", 15*time.Minute), Max: mustInt("LOGIN_LIMIT_MAX", 5)},
		RateLimitCrawl:    RateLimitConfig{Window: mustDurationMs("CRAWL_LIMIT_WINDOW_MS", time.Hour), Max: mustInt("CRAWL_LIMIT_MAX", 5)},
		RateLimitSearch:   RateLimitConfig{Window: mustDurationMs("SEARCH_LIMIT_WINDOW_MS", 15*time.Minute), Max: mustInt("SEARCH_LIMIT_MAX", 10)},
	}

	// 1 MiB default is generous for JSON payloads; uploads have their own cap.
	cfg.MaxRequestBodyBytes = mustInt64("MAX_REQUEST_BODY_BYTES", 1024*1024)

	cfg.EmbeddingCacheTTL = time.Duration(mustInt("EMBEDDING_CACHE_TTL_HOURS", 7*24)) * time.Hour

	cfg.CSPHeader = os.Getenv("CSP_HEADER")

	cfg.SQLToolReadOnlyURL = os.Getenv("JUSTRAG_DB_URL_READONLY")

	cfg.TrustedProxyHopCount = mustInt("TRUSTED_PROXY_HOP_COUNT", 0)

	cfg.LogVerbose = os.Getenv("LOG_VERBOSE") == "true"

	// Parse allowed origins
	if origins := os.Getenv("ALLOWED_ORIGINS"); origins != "" {
		for _, o := range strings.Split(origins, ",") {
			if trimmed := strings.TrimSpace(o); trimmed != "" {
				cfg.AllowedOrigins = append(cfg.AllowedOrigins, trimmed)
			}
		}
	} else if !cfg.IsProduction() {
		cfg.AllowedOrigins = []string{"http://localhost:5173", "http://localhost:3000"}
	}

	// Vector DB: falls back to main DB values
	vectorDBPort, err := envInt("VECTOR_DB_PORT", cfg.DB.Port)
	if err != nil {
		errs = append(errs, err.Error())
	}
	vectorStatementTimeoutMs, err := envInt("VECTOR_DB_STATEMENT_TIMEOUT_MS", int(cfg.DB.StatementTimeout/time.Millisecond))
	if err != nil {
		errs = append(errs, err.Error())
	}
	// Pool sizing is independently overridable in split-DB deployments
	// because the vector DB carries embedding-ingest / HNSW-query traffic
	// at a different shape than the metadata DB. Defaults inherit the
	// main DB sizing so single-DB deployments keep their current behaviour.
	vectorDBMaxConns, err := envInt("VECTOR_DB_POOL_MAX", cfg.DB.MaxConns)
	if err != nil {
		errs = append(errs, err.Error())
	}
	vectorDBMinConns, err := envInt("VECTOR_DB_POOL_MIN", cfg.DB.MinConns)
	if err != nil {
		errs = append(errs, err.Error())
	}
	// Idle timeout is independently overridable for the same reason as pool
	// sizing: long-lived HNSW search connections benefit from a different
	// idle window than short-lived metadata CRUD. Defaults to the main DB's
	// idle timeout when VECTOR_DB_IDLE_TIMEOUT is unset.
	vectorDBIdleTimeout := mustDurationMs("VECTOR_DB_IDLE_TIMEOUT", cfg.DB.IdleTimeout)
	cfg.VectorDB = DBConfig{
		Host:               envOr("VECTOR_DB_HOST", cfg.DB.Host),
		Port:               vectorDBPort,
		User:               envOr("VECTOR_DB_USER", cfg.DB.User),
		Password:           envOr("VECTOR_DB_PASSWORD", cfg.DB.Password),
		Name:               envOr("VECTOR_DB_NAME", cfg.DB.Name),
		MaxConns:           vectorDBMaxConns,
		MinConns:           vectorDBMinConns,
		IdleTimeout:        vectorDBIdleTimeout,
		MaxConnLifetime:    cfg.DB.MaxConnLifetime,
		ConnectionTimeout:  cfg.DB.ConnectionTimeout,
		StatementTimeout:   time.Duration(vectorStatementTimeoutMs) * time.Millisecond,
		SlowQueryThreshold: cfg.DB.SlowQueryThreshold,
		HealthCheckPeriod:  cfg.DB.HealthCheckPeriod,
		ConnectAttempts:    cfg.DB.ConnectAttempts,
	}

	// Worker settings
	if queues := os.Getenv("WORKER_QUEUES"); queues != "" {
		for _, q := range strings.Split(queues, ",") {
			if trimmed := strings.TrimSpace(q); trimmed != "" {
				cfg.WorkerQueues = append(cfg.WorkerQueues, trimmed)
			}
		}
	}
	cfg.WorkerConcurrency = mustInt("WORKER_CONCURRENCY", 8)
	cfg.WorkerHealthPort = mustInt("WORKER_HEALTH_PORT", 8081)
	cfg.WorkerMaintenance = os.Getenv("WORKER_MAINTENANCE") != "false"
	workerDBStatementTimeoutMs := mustInt("WORKER_DB_STATEMENT_TIMEOUT_MS", 0)
	if workerDBStatementTimeoutMs < 0 {
		errs = append(errs, fmt.Sprintf("WORKER_DB_STATEMENT_TIMEOUT_MS=%d must be non-negative", workerDBStatementTimeoutMs))
	}
	cfg.WorkerDBStatementTimeout = time.Duration(workerDBStatementTimeoutMs) * time.Millisecond
	cfg.JustFindUserDataDir = envOr("JUSTFIND_USER_DATA_DIR", "/app/data/justfind-profile")
	cfg.FetcherAllowNoSandbox = os.Getenv("FETCHER_ALLOW_NO_SANDBOX") == "true"

	// S3 storage settings (optional — local filesystem used when S3_ENDPOINT is empty)
	cfg.S3 = S3Config{
		Endpoint:  os.Getenv("S3_ENDPOINT"),
		AccessKey: os.Getenv("S3_ACCESS_KEY"),
		SecretKey: os.Getenv("S3_SECRET_KEY"),
		Bucket:    os.Getenv("S3_BUCKET"),
		Region:    os.Getenv("S3_REGION"),
	}

	// Pprof settings (optional — disabled by default). Profile-sampling
	// rates default to 0 (off) so an always-on pprof endpoint pays no
	// overhead; operators opt in for targeted sessions.
	cfg.Pprof = PprofConfig{
		Enabled:              os.Getenv("PPROF_ENABLED") == "true",
		Addr:                 envOr("PPROF_ADDR", "127.0.0.1:6060"),
		MutexProfileFraction: mustInt("PPROF_MUTEX_PROFILE_FRACTION", 0),
		BlockProfileRate:     mustInt("PPROF_BLOCK_PROFILE_RATE", 0),
	}

	if len(errs) > 0 {
		return nil, fmt.Errorf("invalid environment variables: %s", strings.Join(errs, "; "))
	}

	// JWT_SECRET is enforced in every environment: an empty or short secret
	// silently weakens token signing across all callers. .env.example ships
	// with a 32-char placeholder and all tests set a long secret, so this
	// only trips genuinely under-configured deployments.
	if len(cfg.JWTSecret) < 32 {
		return nil, fmt.Errorf("JWT_SECRET must be set and at least 32 characters")
	}
	// Length alone doesn't guarantee strength — a 32-char run of one character
	// passes the check above but is trivially brute-forced. Warn (don't fail,
	// to avoid breaking existing valid secrets) when the secret looks
	// low-entropy: fewer than three distinct character classes
	// (lower/upper/digit/symbol).
	if jwtSecretCharClasses(cfg.JWTSecret) < 3 {
		slog.Warn("JWT_SECRET appears low-entropy (fewer than 3 character classes); use a random value such as `openssl rand -base64 32`")
	}

	// AUTH_PROVIDER_SECRET_KEY wraps OIDC client_secret and LDAP bindCredentials
	// at rest (authhandler/secrets.go). It is only *required* once an OIDC/LDAP
	// row exists — which config can't know at boot — so we don't force it here.
	// But when it IS set we validate its shape (base64 → exactly 32 bytes for
	// AES-256) at startup rather than deferring to the first encrypt/decrypt: a
	// malformed key would otherwise surface only when an admin first saves a
	// provider or a user first logs in via OIDC, turning a typo into a runtime
	// auth outage. Fail fast instead.
	if raw := os.Getenv("AUTH_PROVIDER_SECRET_KEY"); raw != "" {
		key, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return nil, fmt.Errorf("AUTH_PROVIDER_SECRET_KEY is set but not valid base64: %w", err)
		}
		if len(key) != 32 {
			return nil, fmt.Errorf("AUTH_PROVIDER_SECRET_KEY must decode to exactly 32 bytes for AES-256 (got %d); generate one with `openssl rand -base64 32`", len(key))
		}
	}

	// Production validations
	if cfg.IsProduction() {
		if cfg.Redis.Password == "" {
			return nil, fmt.Errorf("REDIS_PASSWORD is required in production")
		}
		// Fail closed on an empty CORS allowlist. rs/cors treats an empty
		// AllowedOrigins slice as "allow all", and because AllowCredentials is
		// true (middleware/cors.go) that means it reflects ANY request Origin
		// back with credentials enabled — any site could make authenticated
		// cross-origin calls against the API. A missing ALLOWED_ORIGINS in
		// production is therefore a hard misconfiguration, not a warning.
		if len(cfg.AllowedOrigins) == 0 {
			return nil, fmt.Errorf("ALLOWED_ORIGINS must be set in production (an empty allowlist makes CORS reflect any origin with credentials)")
		}
		if cfg.FetcherAllowNoSandbox {
			slog.Warn("FETCHER_ALLOW_NO_SANDBOX=true disables the Chromium sandbox; only safe inside a container running as root")
		}
	}

	return cfg, nil
}

// jwtSecretCharClasses counts how many of the four character classes
// (lowercase, uppercase, digit, symbol) appear in s. Used only for a
// low-entropy warning — it is a cheap heuristic, not a real entropy estimate.
func jwtSecretCharClasses(s string) int {
	var lower, upper, digit, symbol bool
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			lower = true
		case r >= 'A' && r <= 'Z':
			upper = true
		case r >= '0' && r <= '9':
			digit = true
		default:
			symbol = true
		}
	}
	n := 0
	for _, present := range []bool{lower, upper, digit, symbol} {
		if present {
			n++
		}
	}
	return n
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// envOrMultiWithSource tries each key in order, returning the first
// non-empty value plus the env-var name that supplied it. Returns
// (fallback, "") when no key is set. Used by deprecation warnings that
// need to know which legacy key the operator is still relying on.
func envOrMultiWithSource(keys []string, fallback string) (string, string) {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v, k
		}
	}
	return fallback, ""
}

// S3Enabled reports whether S3 storage is configured.
func (s S3Config) S3Enabled() bool {
	return s.Endpoint != ""
}

func envInt(key string, fallback int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return fallback, fmt.Errorf("%s=%q is not a valid integer", key, v)
	}
	return i, nil
}
