# JustRAG

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Docker Build](https://github.com/Lutzi92/JustRAG/actions/workflows/docker-image.yml/badge.svg)](https://github.com/Lutzi92/JustRAG/actions/workflows/docker-image.yml)

Self-hosted retrieval-augmented generation for teams that need searchable knowledge bases, branching chat, research workflows, and generated study material.

## Current State

The runtime is Go-only — the legacy Node.js path has been removed:

- `go-backend/` is the server, worker, and migration runtime.
- `web/` is the React 19 + Vite 7 frontend.
- `docker-compose.yml` deploys the Go stack by default.

## Current Capabilities

- Knowledge bases with ownership, sharing, and admin-managed global KBs
- Branching chat with inline edits, regeneration, comparison, message feedback, and SSE streaming
- File and source ingestion for PDF, DOCX, XLSX, XLS, ODS, PPTX, EPUB, ODT, LaTeX, CSV, text, images, audio, URLs, RSS, and Confluence (with optional layout-aware Docling sidecar for PDF, DOCX, and PPTX)
- Hybrid retrieval with pgvector, PostgreSQL full-text search, RRF fusion, MMR diversity, cross-encoder reranking, BM25 floor reinsertion, quoted-phrase boosting, and prompt-injection checks
- Anthropic-style contextual chunk enrichment, pre-embed deduplication, and MRL two-pass vector retrieval for high-dimension embedding models
- Corrective RAG (CRAG) with relevance grading, single-doc shortcut, and query-rewrite retry, plus optional enumeration pre-pass for list-style queries
- Per-answer factcheck with persisted verification badges and a deterministic citation validator
- Research and academic research workflows with streaming progress plus DOCX and BibTeX export
- Generated content for cards, presentations, podcasts, analyses, and abstracts
- Built-in retrieval evaluation harness (`cmd/eval`) with golden sets, route-based metrics, optional LLM-as-judge, and an admin UI runner
- API key based public API at `/api/v1/*` plus an OpenAI-compatible surface at `/openai/v1/*`
- Structured logs with request-id correlation, Prometheus metrics, OpenTelemetry tracing with Langfuse deep-links, health/readiness probes, version metadata, and worker health endpoints

## Known Gaps

- Data Explorer endpoints exist but currently return `501 Not Implemented` (DuckDB integration not ported)
- Chart generation currently returns `501 Not Implemented`
- `POST /api/describe-image` currently returns `501 Not Implemented`

## Stack

- Backend: Go 1.26, `net/http`, `pgx`, `asynq`
- Frontend: React 19, Vite 7, TypeScript
- Data: PostgreSQL 18, pgvector, Redis 8
- Storage: local filesystem or S3-compatible object storage

## Quick Start

### Docker Compose

The default Compose stack runs:

- `nginx` on `http://localhost:3001`
- `go-server`
- `go-worker`
- `migrate` one-shot init job
- `db`
- `vectordb`
- `redis`
- `minio`

```bash
git clone https://github.com/Lutzi92/JustRAG.git
cd JustRAG
cp .env.docker .env
docker compose up -d
```

Default login:

- Email: `admin@example.com`
- Password: the value of `ADMIN_PASSWORD` in `.env`

After first login, configure AI providers in the admin UI.

### Local Development

Start the local dependencies:

```bash
docker compose up -d db vectordb redis minio
```

Copy the local environment file:

```bash
cp .env.example .env
```

Run the Go services:

```bash
cd go-backend
go run ./cmd/migrate
go run ./cmd/server
go run ./cmd/worker
```

That starts:

- API server on `http://localhost:3000`
- worker probe on `http://localhost:8081/healthz`

If you also want the frontend dev server:

```bash
npm install
npm run web
```

That starts Vite on `http://localhost:5173`.

## Repository Layout

```text
.
├── go-backend/          # Go server, worker, and embedded migrations
├── web/                 # React frontend
├── eval/                # Golden-set retrieval evaluation fixtures
├── docs/                # Architecture, retrieval, agent orchestration, and ops docs
├── k8s/                 # Kubernetes manifests
├── nginx/               # Nginx configs
└── scripts/             # Operational helpers (PDF/XLSX fixtures, DB migration scripts)
```

## Key Endpoints

- `GET /health`
- `GET /ready`
- `GET /version`
- `GET /api/v1/openapi.json`
- `GET /api/v1/docs`
- `GET /openai/v1/models`
- `POST /openai/v1/chat/completions`

## Documentation

- [ARCHITECTURE.md](ARCHITECTURE.md): runtime structure and data flow
- [API.md](API.md): current HTTP surfaces and endpoint status
- [DEPLOYMENT.md](DEPLOYMENT.md): Compose deployment and env configuration
- [CONTRIBUTING.md](CONTRIBUTING.md): local setup and contributor workflow
- [go-backend/README.md](go-backend/README.md): Go server, worker, and migration notes
- [web/README.md](web/README.md): frontend-specific notes
- [eval/golden/README.md](eval/golden/README.md): golden-set retrieval eval format and runner
- [docs/retrieval.md](docs/retrieval.md): full retrieval-pipeline mechanism and eval history
- [docs/agent-orchestration.md](docs/agent-orchestration.md): chat orchestrators and AP-feature reference

## License

Distributed under the MIT License. See [`LICENSE`](LICENSE).
