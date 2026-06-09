# JustRAG Architecture

This document describes the current repository layout and runtime architecture as it exists today.

## Overview

JustRAG currently runs as:

- a React frontend in `web/`
- a primary Go backend in `go-backend/`
- background processing via Asynq workers
- PostgreSQL for application state
- PostgreSQL + pgvector for embeddings
- Redis for queues, rate limiting, relay state, and caches
- optional filesystem or S3-compatible object storage

The legacy Node.js implementation under `src/` has been removed; the runtime is Go-only. Migrations are managed by the Go `migrate` entrypoint (`go-backend/cmd/migrate`).

## Primary Runtime Path

### HTTP server

The main application entrypoint is [`go-backend/cmd/server/main.go`](go-backend/cmd/server/main.go). It:

- loads environment-backed config from `internal/config/`
- initializes main DB, vector DB, Redis, storage, auth blacklist state, and Asynq client
- registers routes in [`go-backend/internal/app/routes.go`](go-backend/internal/app/routes.go)
- wraps handlers with request IDs, recovery, logging, CORS, security headers, Prometheus metrics, and Redis-backed rate limiting
- optionally starts localhost-only pprof when `PPROF_ENABLED=true`

The server exposes:

- `/health`
- `/ready`
- `/version`
- `/metrics` (admin-only)
- `/api/*`
- `/api/v1/*`
- `/openai/v1/*`

In production it also serves the built frontend from `/app/client/dist`.

### Worker model

The standalone worker entrypoint is [`go-backend/cmd/worker/main.go`](go-backend/cmd/worker/main.go). It:

- connects to PostgreSQL, Redis, storage, AI config resolution, parsers, and vector services
- registers Asynq handlers for file, text, URL, crawl, RSS, Confluence, research, academic, and generated-content jobs
- starts RSS and Confluence schedulers
- exposes `/healthz` and `/readyz`
- can run maintenance tasks when `WORKER_MAINTENANCE=true`

Current queue names:

- `rag-quick`
- `rag-heavy`
- `rag-batch`

### Migration runtime

The migration entrypoint is [`go-backend/cmd/migrate/main.go`](go-backend/cmd/migrate/main.go). It:

- runs embedded main DB migrations from `go-backend/migrations/main/`
- runs embedded vector DB migrations from `go-backend/migrations/vector/`
- skips vector migrations when main and vector DB point to the same database
- ensures dynamic vector tables exist
- seeds the admin user from `ADMIN_PASSWORD`

## Application Layers

### Routing and feature packages

Most Go features follow a package-per-domain layout under `go-backend/internal/`, for example:

- `auth`, `authhandler`
- `kb`, `kbaccess`
- `chat`
- `files`
- `research`
- `academic`
- `contentgen`, `gencontent`
- `rss`
- `confluence`
- `analytics`
- `adminconfigs`, `adminproviders`, `adminusers`, `adminglobalkbs`, `admineval`
- `eval`
- `publicapi`
- `openaicompat`

Handlers usually live in `http.go` or `handler.go`, persistence in `store_pg.go`, and package-specific logic alongside them.

### Core services

Important backend service areas:

- `ai/`: provider configs, completions, embeddings, reranking, streaming, health checks, STT/TTS, fact checking, CRAG relevance grading, enumeration extraction, embedding cache
- `vector/`: chunk persistence, hybrid retrieval, RRF fusion, MMR diversity, BM25 floor reinsertion, reranking integration, deduplication, chunk metadata, MRL two-pass routing
- `parser/`: PDF, DOCX, XLSX, XLS, ODS, PPTX, EPUB, ODT, LaTeX, CSV, text, image, and audio extraction; opt-in Docling client for PDF, DOCX, PPTX
- `fetcher/`: SSRF-aware URL fetching plus browser-backed extraction tiers
- `worker/`: background processing handlers and maintenance jobs
- `middleware/`: metrics, logging, recovery, security headers, timeouts, and rate limiting
- `storage/`: local filesystem or S3-compatible object storage
- `prompts/`: prompt templates for chat, factcheck, CRAG, enumeration, and judge evaluation
- `observability/`, `logctx/`, `requestid/`: Prometheus metric instruments, request-id propagation, and OpenTelemetry span helpers
- `pgxutil/`, `httpclient/`, `safego/`, `database/`: shared infrastructure helpers
- `eval/`, `admineval/`: golden-set storage, retrieval/judge metrics, and admin-driven eval run management

### Storage model

Current storage split:

- main PostgreSQL database for users, KBs, chats, files, settings, analytics, generated content, and auth state
- vector PostgreSQL database for chunks and embeddings
- Redis for queues, rate limiting state, embedding cache, and SSE relay support
- filesystem or S3-compatible object storage for uploaded artifacts

## Frontend Structure

The frontend lives in `web/src/` and is organized around:

- `components/`: feature UI, admin screens, studio, sidebars, API key UI
- `contexts/`: auth, theme, modal, toast, KB workspace state
- `hooks/`: chat, file management, RSS, Confluence, sharing, version checks, UI behavior
- `routes/`: route-level composition
- `utils/`: SSE parsing, message tree helpers, clipboard, viewport, error handling

The production build is generated in `web/dist` and copied into `/app/client/dist` by the root [`Dockerfile`](Dockerfile).

## Major Flows

### Ingestion

1. A file, URL, RSS item, or Confluence page is attached to a KB.
2. Metadata is stored in the main DB and artifacts are stored locally or in S3-compatible storage.
3. A worker job parses content and splits it into chunks.
4. Optional chunk enrichment may run before embedding.
5. Embeddings are generated and written into the vector DB.
6. File processing state is updated for the UI.

### Chat

1. A request enters `POST /api/kb/{id}/chat`.
2. Auth and KB access checks run first.
3. Prompt-injection validation is applied.
4. Retrieval runs through vector search, keyword search, RRF fusion, optional cross-encoder reranking, MMR diversity, and BM25 floor reinsertion.
5. CRAG relevance grading optionally rewrites and retries when the first round looks weak.
6. Enumeration pre-pass deterministically extracts list answers when the query is list-shaped.
7. Context is assembled (with `Context:` prefixes from contextual enrichment) and the answer streams over SSE when requested.
8. Factcheck and citation validation run after generation; results merge into `messages.verification`.
9. Messages are stored as a tree via parent-child links; admins can deep-link from any AI message into Langfuse via `messages.trace_id`.

### Research

Research and academic research reuse the KB and retrieval layers, then add:

- planning and multi-step execution
- progress streaming
- export to DOCX and BibTeX
- web and academic source acquisition

## Deployment Shapes

### Default Go deployment

- [`docker-compose.yml`](docker-compose.yml)
- `nginx -> go-server -> go-worker`

### Go production overlay

- [`docker-compose.production.yml`](docker-compose.production.yml)
- disables the default worker and scales `worker-quick`, `worker-heavy`, and `worker-batch`

### Other compose files

- [`docker-compose.local.yml`](docker-compose.local.yml) — local development; builds the Go image from source
- [`docker-compose.k8s.yml`](docker-compose.k8s.yml) — app-only variant; workers run as separate Kubernetes deployments
- [`docker-compose.docling.yml`](docker-compose.docling.yml) — opt-in Docling sidecar overlay for layout-aware parsing

## Feature Status Notes

- Data Explorer routes are registered in the Go server but currently return `501`
- Chart generation is also registered but currently returns `501`
- `POST /api/describe-image` currently returns `501`

Those gaps are important because the frontend still contains related UI and legacy code.
