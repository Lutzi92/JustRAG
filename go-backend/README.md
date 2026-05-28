# Go Backend

The Go backend is the runtime for this repository.

It provides:

- an HTTP API server in [`cmd/server`](cmd/server/main.go)
- an Asynq worker in [`cmd/worker`](cmd/worker/main.go)
- a migration/bootstrap binary in [`cmd/migrate`](cmd/migrate/main.go)
- a retrieval evaluation harness in [`cmd/eval`](cmd/eval) (see [`../eval/golden/README.md`](../eval/golden/README.md))

## Runtime Shape

Wire / HTTP:

- `internal/app/`: server and worker startup wiring
- `internal/middleware/`, `internal/auth/`, `internal/authhandler/`, `internal/apikeyauth/`: auth chain, rate limiting, CORS, logging, security headers, Prometheus metrics
- `internal/httputil/`, `internal/sserelay/`, `internal/requestid/`: request plumbing and SSE relay

Chat orchestration and retrieval:

- `internal/chat/`, `internal/agents/`, `internal/prompts/`: the four orchestrators (agentic / plan-execute / supervisor / standard), trajectory events, refine/factuality gates
- `internal/vector/`: chunk storage, hybrid retrieval, fusion, reranker blend, MMR, BM25 floor, deduplication
- `internal/ai/`, `internal/splitter/`, `internal/processor/`, `internal/parser/`, `internal/pptx/`: LLM client, chunking, ingestion, document parsing (PDF, DOCX, XLSX, XLS, ODS, PPTX, EPUB, ODT, LaTeX, CSV, text, images, audio)

Tools and knowledge graph:

- `internal/mcp/`, `internal/mcp/builtin/`: MCP registry + dispatcher and built-in tools (kb_search, keyword_search, chunk_read, document_outline, calculator, sql_query, code_exec, graph_search, web_search, memory_*)
- `internal/kg/`, `internal/sessionmem/`, `internal/longmem/`, `internal/aibudget/`: knowledge graph read path, session and long-term memory, per-turn token budget

Storage / data:

- `internal/database/`, `internal/store/`, `internal/storage/`, `internal/pgxutil/`: pgx pools for main and vector DBs, file lifecycle, local filesystem and S3-compatible storage
- `internal/files/`, `internal/kbaccess/`, `internal/cascade/`: file metadata, KB ACLs, cascade-delete

Background workers:

- `internal/worker/`, `internal/jobs/`, `internal/fetcher/`, `internal/crawler/`, `internal/rss/`, `internal/research/`, `internal/confluence/`, `internal/academic/`, `internal/gencontent/`, `internal/contentgen/`: Asynq handlers for ingestion, RSS polling, Confluence crawl, research, content generation

Admin, eval, observability:

- `internal/adminagentmetrics/`, `internal/adminconfigs/`, `internal/admineval/`, `internal/adminglobalkbs/`, `internal/adminmaintenance/`, `internal/adminmcp/`, `internal/adminproviders/`, `internal/adminusers/`, `internal/kb/`, `internal/users/`, `internal/apikeys/`, `internal/auditlogs/`: admin endpoints and matching `site_config` readers
- `internal/eval/`, `internal/observability/`, `internal/logctx/`, `internal/analytics/`, `internal/systemhealth/`, `internal/apidocs/`: golden-set eval, Prometheus metrics, structured logging, health checks

Public surfaces and config:

- `internal/publicapi/`, `internal/publicconfigs/`, `internal/openaicompat/`: public API at `/api/v1/*` and OpenAI-compatible surface at `/openai/v1/*`
- `internal/config/`, `internal/siteconfig/`: env parsing and dynamic site config

## Local Development

From the repository root, start the dependencies:

```bash
docker compose up -d db vectordb redis minio
```

Then run the Go services from `go-backend/`:

```bash
cd go-backend
go run ./cmd/migrate
go run ./cmd/server
go run ./cmd/worker
```

Defaults:

- server on `http://localhost:3000`
- worker health on `http://localhost:8081/healthz`

## Migrations

The primary migration runtime is the Go `migrate` binary.

Commands:

```bash
cd go-backend
go run ./cmd/migrate
go run ./cmd/migrate --status
go run ./cmd/migrate --seed-only
```

Migration sources:

- `migrations/main/`
- `migrations/vector/`

The runtime also ensures dynamic vector tables exist after migrations.

## Quality Checks

```bash
cd go-backend
go test ./...
go test -bench . -run ^$ ./internal/splitter ./internal/vector
```

Useful profiling settings:

- `PPROF_ENABLED=true`
- optional `PPROF_ADDR=127.0.0.1:6060`

Then inspect with:

```bash
go tool pprof http://127.0.0.1:6060/debug/pprof/heap
```

## Operational Notes

- [`../docker-compose.yml`](../docker-compose.yml) is the default deployment
- [`../docker-compose.production.yml`](../docker-compose.production.yml) adds specialized worker pools
- [`../docker-compose.local.yml`](../docker-compose.local.yml) bring-up tailored for local development
- [`../docker-compose.k8s.yml`](../docker-compose.k8s.yml) Kubernetes-friendly variant
- [`../docker-compose.docling.yml`](../docker-compose.docling.yml) opt-in Docling sidecar for layout-aware PDF/DOCX/PPTX parsing (see [`../docs/observability/docling.md`](../docs/observability/docling.md))

## Current Runtime Gaps

The Go backend currently returns `501` for:

- Data Explorer endpoints (DuckDB integration not ported)
- chart generation
- `POST /api/describe-image`

Do not treat those as implemented in the default runtime.
