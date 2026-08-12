# API Reference

This document reflects the current Go backend API surface in `go-backend/internal/app/routes.go`.

## API Surfaces

### Web/session API

- Base path: `/api`
- Authentication: JWT bearer token
- Used by the React frontend

### Public API

- Base path: `/api/v1`
- Authentication: API key bearer token

### OpenAI-compatible API

- Base path: `/openai/v1`
- Authentication: API key bearer token

## Docs and Service Endpoints

| Method | Path | Notes |
|---|---|---|
| GET | `/health` | Liveness |
| GET | `/ready` | Readiness |
| GET | `/version` | Build metadata |
| GET | `/metrics` | Admin-only Prometheus endpoint |
| GET | `/api/v1/openapi.json` | Embedded OpenAPI spec |
| GET | `/api/v1/docs` | Scalar API reference |

## Web/Session API

All routes below are under `/api`.

### Authentication

| Method | Path |
|---|---|
| POST | `/auth/login` |
| POST | `/auth/logout` |
| POST | `/auth/refresh` |

### Users, site config, API keys

| Method | Path |
|---|---|
| GET | `/users/{id}` |
| PATCH | `/users/{id}` |
| GET | `/public/configs` |
| GET | `/site-config` |
| POST | `/site-config` |
| POST | `/site-config/logo` |
| POST | `/api-keys` |
| GET | `/api-keys` |
| DELETE | `/api-keys/{id}` |

### Knowledge bases

| Method | Path |
|---|---|
| GET | `/kb` |
| GET | `/kb/global` |
| POST | `/kb` |
| PATCH | `/kb/{id}` |
| DELETE | `/kb/{id}` |
| GET | `/kb/{id}/files` |

### KB members and ownership

Four roles, strictly ordered `view < edit < admin < owner` (migration 0064,
`kb_members`). "Min. role" is the effective KB role the caller must resolve to
via `kbaccess.EffectiveRole`; `owner` and `self` are enforced inside the
handler rather than by the route gate. Replaces the removed `/kb/{id}/shares`
and `/kb/{id}/share[/{userId}]` endpoints.

| Method | Path | Min. role |
|---|---|---|
| GET | `/kb/{id}/members` | admin |
| PUT | `/kb/{id}/members/{userId}` | admin |
| DELETE | `/kb/{id}/members/{userId}` | admin |
| POST | `/kb/{id}/members/bulk` | admin |
| DELETE | `/kb/{id}/members/pending/{username}` | admin |
| POST | `/kb/{id}/transfer-owner` | owner |
| DELETE | `/kb/{id}/membership` | view (self) |
| GET | `/kb/{id}/membership/impact` | view (self) |

`PUT /members/{userId}` and `POST /members/bulk` accept `view`, `edit` and
`admin` only — ownership moves solely through `/transfer-owner`, and the target
must already be a member. `DELETE /members/{userId}` (an admin revoking someone)
leaves that user's chats intact; `DELETE /membership` (self-service leave)
deletes them, and `GET /membership/impact` returns the chat count backing the
confirmation dialog. The member list sits behind `admin`, not `view`: on a
published global KB every authenticated caller resolves to `view`, and the
roster is not theirs to read.

### Files and source ingestion

| Method | Path |
|---|---|
| POST | `/kb/{id}/files` |
| POST | `/kb/{id}/text` |
| POST | `/kb/{id}/add-sources` |
| POST | `/kb/{id}/fetch-url` |
| POST | `/kb/{id}/crawl` |
| GET | `/kb/{id}/crawl/status/{jobId}` |
| POST | `/kb/{id}/websearch` |
| GET | `/files/{id}/download` |
| DELETE | `/files/{id}` |

### Chat

| Method | Path |
|---|---|
| GET | `/kb/{id}/chats` |
| GET | `/chats/{id}/messages` |
| DELETE | `/chats/{id}` |
| POST | `/kb/{id}/chat` |
| POST | `/kb/{id}/chats/{chatId}/messages/{messageId}/feedback` |

### Generated content

| Method | Path | Status |
|---|---|---|
| GET | `/kb/{id}/generated-content` | available |
| POST | `/kb/{id}/generate/cards` | available |
| POST | `/kb/{id}/generate/presentation` | available |
| POST | `/kb/{id}/generate/podcast` | available |
| GET | `/kb/{id}/generate/podcast/status/{jobId}` | available |
| POST | `/kb/{id}/generate/chart` | returns `501` in Go runtime |
| POST | `/kb/{id}/generate/analysis` | available |
| POST | `/kb/{id}/generate/abstract` | available |
| PATCH | `/generated-content/{id}` | available |
| DELETE | `/generated-content/{id}` | available |
| GET | `/generated-content/{id}/download` | available |
| GET | `/generated-content/{id}/stream` | available |
| POST | `/describe-image` | returns `501` in Go runtime |

### Research and export

| Method | Path |
|---|---|
| POST | `/enhance` |
| POST | `/kb/{id}/research` |
| POST | `/kb/{id}/web-research` |
| GET | `/research/{researchId}/status` |
| GET | `/research/{researchId}/report` |
| GET | `/research/deep/{deepChatId}` |
| POST | `/research/{researchId}/abort` |
| POST | `/kb/{id}/export/docx` |
| POST | `/kb/{id}/export/bibtex` |
| POST | `/research/{sessionId}/bibtex` |

### Academic research

| Method | Path |
|---|---|
| POST | `/kb/{id}/academic-search` |
| POST | `/kb/{id}/academic-research` |
| POST | `/academic-research/{researchId}/papers/add` |
| POST | `/kb/{id}/export/academic-bibtex` |
| POST | `/academic-research/{sessionId}/bibtex` |

### RSS

| Method | Path |
|---|---|
| POST | `/kb/{id}/rss` |
| GET | `/kb/{id}/rss` |
| PATCH | `/kb/{id}/rss/{feedId}` |
| DELETE | `/kb/{id}/rss/{feedId}` |
| POST | `/kb/{id}/rss/{feedId}/poll` |

### Confluence

| Method | Path |
|---|---|
| GET | `/confluence/connections` |
| POST | `/confluence/connections` |
| PUT | `/confluence/connections/{id}` |
| DELETE | `/confluence/connections/{id}` |
| POST | `/confluence/connections/{id}/verify` |
| GET | `/confluence/spaces` |
| GET | `/confluence/spaces/{spaceKey}/pages` |
| GET | `/confluence/pages/{pageId}/children` |
| POST | `/kb/{id}/confluence-sources` |
| GET | `/kb/{id}/confluence-sources` |
| PATCH | `/kb/{id}/confluence-sources/{sourceId}` |
| DELETE | `/kb/{id}/confluence-sources/{sourceId}` |
| POST | `/kb/{id}/confluence-sources/{sourceId}/sync` |

### Analytics and system health

| Method | Path |
|---|---|
| GET | `/kb/{id}/analytics` |
| GET | `/kb/{id}/analytics/files` |
| GET | `/kb/{id}/analytics/activity` |
| GET | `/kb/{id}/analytics/chats` |
| GET | `/kb/{id}/analytics/generated` |
| GET | `/kb/{id}/analytics/retrieval-quality` |
| GET | `/system-health/live` |
| GET | `/system-health/history` |
| GET | `/system-health/subsystems` |
| POST | `/system-health/ai-check` |

### Admin

| Method | Path |
|---|---|
| GET | `/admin/configs` |
| POST | `/admin/configs` |
| PATCH | `/admin/configs/{id}` |
| DELETE | `/admin/configs/{id}` |
| POST | `/admin/configs/{id}/activate` |
| POST | `/admin/configs/{id}/test` |
| GET | `/admin/auth-providers` |
| POST | `/admin/auth-providers` |
| PATCH | `/admin/auth-providers/{id}` |
| DELETE | `/admin/auth-providers/{id}` |
| GET | `/admin/users` |
| PATCH | `/admin/users/{id}/role` |
| DELETE | `/admin/users/{id}` |
| GET | `/admin/global-kbs` |
| POST | `/admin/global-kbs` |
| PATCH | `/admin/global-kbs/{id}` |
| DELETE | `/admin/global-kbs/{id}` |
| GET | `/admin/global-kbs/{id}/editors` |
| POST | `/admin/global-kbs/{id}/editors` |
| DELETE | `/admin/global-kbs/{id}/editors/{userId}` |
| GET | `/admin/audit-logs` |
| POST | `/admin/reembed-all` |
| POST | `/admin/agent/template` |

### Admin evaluation runner

These routes drive the in-app evaluation runner. They are admin-only and are skipped at boot when `eval_ui_enabled` in `site_configs` is set to a falsy value (`false`, `0`, `off`, `no`).

| Method | Path |
|---|---|
| POST | `/admin/eval/runs` |
| GET | `/admin/eval/runs` |
| GET | `/admin/eval/runs/{id}` |
| GET | `/admin/eval/runs/{id}/export` |
| DELETE | `/admin/eval/runs/{id}` |
| POST | `/admin/eval/golden-sets` |
| GET | `/admin/eval/golden-sets` |
| DELETE | `/admin/eval/golden-sets/{id}` |

### Data Explorer

These routes are registered in the Go server for compatibility but currently return `501 Not Implemented`.

| Method | Path |
|---|---|
| GET | `/kb/{id}/data-explorer/schema` |
| POST | `/kb/{id}/data-explorer/query` |
| POST | `/kb/{id}/data-explorer/export` |

## Public API

All routes below are under `/api/v1` and require `Authorization: Bearer <api-key>`.

| Method | Path |
|---|---|
| GET | `/kb` |
| GET | `/kb/{id}/chats` |
| GET | `/kb/{id}/chats/{chatId}/messages` |
| POST | `/kb/{id}/chat` |
| POST | `/kb/{id}/research` |

Notes:

- KB permissions are still enforced.
- `POST /api/v1/kb/{id}/research` supports streaming and non-streaming behavior.

## OpenAI-Compatible API

All routes below are under `/openai/v1` and require an API key.

| Method | Path |
|---|---|
| GET | `/models` |
| POST | `/chat/completions` |

Model IDs are exposed as `kb-{uuid}` and map directly to knowledge bases.

## Rate Limiting

The Go server applies per-category Redis-backed rate limits:

| Category | Limit | Endpoints |
|---|---|---|
| login | 5 / 15 min | `POST /api/auth/login` |
| chat | 20 / min | `POST /api/kb/{id}/chat` |
| research | 5 / min | `POST /api/kb/{id}/research`, `POST /api/kb/{id}/web-research` |
| generate | 10 / min | `POST /api/kb/{id}/generate/*` |
| api | 100 / min | `/api/v1/*`, `/openai/v1/*` |
