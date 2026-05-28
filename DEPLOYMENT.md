# Deployment Guide

This guide describes the deployment options that currently exist in the repository.

## Deployment Modes

### Default: Go backend

Use [`docker-compose.yml`](docker-compose.yml) for the standard deployment. It starts:

- `nginx`
- `migrate`
- `go-server`
- `go-worker`
- `db`
- `vectordb`
- `redis`
- `minio`

`nginx` is the public entrypoint on `http://localhost:3001` and proxies to the Go server on port `3000`.

### Production overlay

Use [`docker-compose.production.yml`](docker-compose.production.yml) together with the base file when you want separate worker pools:

- `worker-quick`
- `worker-heavy`
- `worker-batch`

The overlay disables the default `go-worker` service and scales dedicated workers instead.

### Other compose files

- [`docker-compose.local.yml`](docker-compose.local.yml) — local development; builds the Go image from source (`docker compose -f docker-compose.local.yml up --build`).
- [`docker-compose.k8s.yml`](docker-compose.k8s.yml) — app-only variant (no `go-worker`); workers run as separate Kubernetes deployments.
- [`docker-compose.docling.yml`](docker-compose.docling.yml) — opt-in sidecar overlay for layout-aware PDF/DOCX/PPTX parsing; layer it on the base file.

## Quick Start

```bash
git clone https://github.com/Lutzi92/JustRAG.git
cd JustRAG
cp .env.docker .env
docker compose up -d
```

Then open `http://localhost:3001`.

## Compose Service Layout

| Service | Purpose |
|---|---|
| `nginx` | public reverse proxy |
| `migrate` | one-shot init service for DB migrations, vector table setup, and admin seeding |
| `go-server` | HTTP API server and production static frontend host |
| `go-worker` | default Asynq worker |
| `db` | primary PostgreSQL 18 database |
| `vectordb` | pgvector PostgreSQL 18 database |
| `redis` | queues, caches, and rate limiting state |
| `minio` | S3-compatible object storage |

## Production Overlay Example

```bash
docker compose -f docker-compose.yml -f docker-compose.production.yml up -d
```

## Environment Files

- `.env.docker`: starting point for Compose deployment
- `.env.example`: starting point for local development

## Important Environment Variables

The Go runtime loads configuration from [`go-backend/internal/config/config.go`](go-backend/internal/config/config.go).

### Core server settings

- `NODE_ENV` or `GO_ENV`
- `PORT`
- `ALLOWED_ORIGINS`
- `JWT_SECRET`

### Main database

- `DB_HOST`
- `DB_PORT`
- `DB_USER`
- `DB_PASSWORD`
- `DB_NAME`
- `DB_POOL_MAX`
- `DB_POOL_MIN`
- `DB_IDLE_TIMEOUT`
- `DB_CONN_MAX_LIFETIME`
- `DB_CONNECTION_TIMEOUT`

### Vector database

- `VECTOR_DB_HOST`
- `VECTOR_DB_PORT`
- `VECTOR_DB_USER`
- `VECTOR_DB_PASSWORD`
- `VECTOR_DB_NAME`

If these are omitted, the Go runtime falls back to the main DB settings.

### Redis

- `REDIS_HOST`
- `REDIS_PORT`
- `REDIS_PASSWORD`
- `REDIS_DB`

### Storage

- `S3_ENDPOINT`
- `S3_ACCESS_KEY`
- `S3_SECRET_KEY`
- `S3_BUCKET`
- `S3_REGION`

If `S3_ENDPOINT` is empty, the app uses local filesystem storage.

### Worker settings

- `WORKER_QUEUES`
- `WORKER_CONCURRENCY`
- `WORKER_MAINTENANCE`
- `WORKER_HEALTH_PORT`
- `JUSTFIND_USER_DATA_DIR`

### Fetcher and parser settings

- `FETCHER_ALLOW_NO_SANDBOX`
- `FETCHER_BROWSER_BIN`
- `PARSER_ENABLE_OCR`
- `EMBEDDING_CACHE_TTL`

### Profiling

- `PPROF_ENABLED`
- `PPROF_ADDR`

## Health and Version Endpoints

The server exposes:

- `GET /health`
- `GET /ready`
- `GET /version`
- `GET /metrics` (admin-only)

The worker exposes:

- `GET /healthz`
- `GET /readyz`

## Production Recommendations

- Set a strong `JWT_SECRET` before first start
- Set a non-default `ADMIN_PASSWORD`
- Set `REDIS_PASSWORD`
- Replace example DB and MinIO credentials
- Set `ALLOWED_ORIGINS` explicitly
- Keep `nginx` in front of the app
- Restrict direct access to PostgreSQL, Redis, and MinIO ports
- Use the production overlay when you need independent worker scaling

## Current Runtime Limitations

The default Go deployment does not currently provide:

- Data Explorer execution
- Chart generation
- Image description

Those routes currently return `501 Not Implemented`.

## Kubernetes

The repository also contains manifests in `k8s/` for:

- namespace and config/secrets
- worker role separation

Treat them as starting points, not a turnkey production install.
