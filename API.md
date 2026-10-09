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
| GET | `/kb/{id}` |
| PATCH | `/kb/{id}` |
| DELETE | `/kb/{id}` |
| GET | `/kb/{id}/files` |

`GET /kb` is paged with `?limit=` (default 50, maximum 100 — larger values are
clamped to 100, non-positive or non-numeric ones fall back to 50) and
`?offset=` (default 0). It lists the caller's private KBs; global KBs come from
`GET /kb/global`.

Card fields on the KB rows of `GET /kb`, `GET /kb/global` and `GET /kb/{id}`:

- `oldestFileAt` — the earliest effective file date
  (`MIN(COALESCE(published_at, created_at))`); omitted for a KB without files.
- `lastIngestedAt` — when content was last successfully ingested: the latest
  ingest time of a `completed` or `partial` file. This is ingest time, not the
  document's own date, and failed, pending or still-processing files do not
  count. A retry or re-embed of an existing file moves it; omitted while no
  file has been ingested.

`PATCH /kb/{id}` returns the KB in the same caller-aware shape as
`GET /kb/{id}` (card fields, `myRole`, and the per-user filter fields below),
so a client can replace its copy with the response.

### Per-user topic filters

The shell's chip row: a favorite (star) on any KB the caller can see, and the
caller's own categories with their KB assignments. Both are personal display
state and grant no access. A favorite is not a subscription: subscribing
decides whether a global KB is in the caller's overview at all, while the star
pins any KB — private or global — to the "Favoriten" chip.

| Method | Path | Gate | Success |
|---|---|---|---|
| PUT | `/kb/{id}/favorite` | KB role view | 204 |
| DELETE | `/kb/{id}/favorite` | KB role view | 204 |
| GET | `/kb-user-categories` | authenticated | 200, `[{id, name, sortOrder}]` (`[]` when none) |
| POST | `/kb-user-categories` | authenticated | 201, the created category |
| PATCH | `/kb-user-categories/{catId}` | authenticated | 200, the updated category |
| DELETE | `/kb-user-categories/{catId}` | authenticated | 204 |
| PUT | `/kb/{id}/user-categories/{catId}` | KB role view | 204 |
| DELETE | `/kb/{id}/user-categories/{catId}` | KB role view | 204 |

- PUT and DELETE on the favorite and on an assignment are idempotent: they set
  a state, so repeating them, or deleting something absent, is still 204.
- The `{id}` routes answer like every other KB route: 404 when the KB does not
  exist, 403 when the caller may not view it.
- `POST`/`PATCH` body: `{"name": string, "sortOrder": integer}`. `name` is
  trimmed and must be 1–100 characters (counted in Unicode code points), must
  contain no control characters, and must contain at least one visible
  character (whitespace and invisible format characters alone are refused);
  `sortOrder` defaults to 0 and must fit a signed 32-bit integer. Violations
  are 400. `PATCH` replaces both fields.
- 409 when the caller already has a category with the same name
  (case-insensitive), or already owns 50 categories (create only).
- 404 for a `catId` that is not a UUID; and on `PATCH`/`DELETE` of a category
  and `PUT` of an assignment, also for one that does not exist or belongs to
  another user — indistinguishable on purpose. (`DELETE` of an assignment is
  idempotent and stays 204.)
- Deleting a category removes its assignments. Leaving a KB or being removed
  from it removes the user's favorite and assignments on that KB when they can
  no longer view it afterwards (a private or unpublished KB); on a published
  global KB, which they can still view, both stay. Their categories always
  stay.

The KB rows of `GET /kb`, `GET /kb/global`, `GET /kb/{id}`, `PATCH /kb/{id}`,
`POST /kb` and the entries of `GET /kb/catalog` carry the caller's own state:

- `isFavorite` (boolean) — the caller starred this KB.
- `userCategoryIds` (string array, `[]` when none) — ids of the caller's own
  categories assigned to this KB.

Both fields are per caller and never appear on the API-key surfaces
(`GET /api/v1/kb`, `GET /openai/v1/models`).

### Search

| Method | Path | Auth |
|---|---|---|
| GET | `/search?q=<text>[&kb_id=<uuid>][&limit=<n>]` | authenticated (no KB role) |

The shell header's global search: one request, one object with four arrays —
`topics`, `sources`, `chats`, `messages` — always all present. `internal/globalsearch`.

- **Rate limit: 60 requests per minute per user** (fixed one-minute window,
  category `search`; see Rate Limiting below). Over the budget the answer is:

  ```
  HTTP/1.1 429 Too Many Requests
  Retry-After: <seconds until the window resets, 1-60>
  Content-Type: application/json

  {"error":"Too many requests, please try again later."}
  ```

  A client should treat this as "too many searches, wait a moment" and retry
  after `Retry-After` seconds, not as a failed search. The budget is counted
  **per authenticated user, not per IP**, so users behind one NAT or VPN
  egress each have their own. Only authenticated requests count: a request
  without a valid token is answered `401` before it reaches the limiter. If
  Redis is unavailable the limit is not enforced (fail open); search keeps
  working.

- Status codes: `200` with the body below; `400` for an invalid `q` or
  `limit`; `401` without a valid token; `404` for a `kb_id` that is not
  visible (see below); `429` over the rate limit; `500` on a server error.
  A request the client cancels before the answer is ready is logged with
  `499` (client closed request); the client never sees it.
- `q` is trimmed; fewer than 3 characters (counted in characters, not bytes,
  so `äöü` is enough) is `400` — never a full listing. More than 200
  characters is also `400`, and so is `q` that is not valid UTF-8 or
  contains a NUL character.
- `limit` applies **per group**: default 5, maximum 20. Any larger positive
  integer is clamped to 20 (even one too large for a 64-bit integer); a
  non-integer, `0` or a negative value is `400`.
- `kb_id` restricts every group to that topic. The caller needs at least
  `view` on it. If the topic does not exist, is not visible to the caller, or
  the id is not a UUID, the answer is `404` — never `403`, so the route cannot
  confirm that a hidden topic exists. (This differs on purpose from the
  KB-scoped routes `/kb/{id}/…`, which answer `404` for a missing KB and `403`
  for one the caller cannot access: here the topic is a filter, not the
  addressed resource.)
- Matching is **fuzzy**: a hit is either a case-insensitive literal substring
  (`ILIKE`, with `%`, `_` and `\` escaped) or trigram-similar to `q`
  (`pg_trgm` word similarity ≥ 0.4, migration 0084), so a typo such as
  `Statsitik` still finds `Statistik`. Topics match on name (substring or
  fuzzy) and on description and header text (substring only: fuzzy on long
  free text is noise). Sources match on the file name (substring or fuzzy).
- Each hit reports how it matched in `match`: `prefix` (the name starts with
  `q`), `substring` (the name, or for topics the description or header text,
  contains `q`) or `fuzzy` (only trigram-similar).
- Order within each group: `prefix`, then `substring` (for topics, name hits
  before description/header hits), then `fuzzy` by similarity, most similar
  first; ties by name, then id. A fuzzy hit never widens visibility: it is
  drawn from the same visible topics as every other hit.
- **Chats** match on the title exactly like file names (substring or fuzzy,
  same `match` tiers and order); ties go to the most recently updated chat.
- **Messages** match on content with Postgres full-text search
  (`to_tsvector('simple', …)`, migration 0085): the query is split into words
  by Postgres' own parser and every word must occur as a **word prefix**
  (`Statis` finds `Statistik`), case-insensitive, without stemming or
  typo tolerance. Words shorter than 3 characters are ignored (`abc de`
  searches only `abc`; `ab cd` finds no messages). Operator characters in `q`
  (`& | ! ( ) :`) are ignored, not evaluated. At most **one hit per chat** — its best-ranked message
  (`ts_rank`, ties to the newest) — ordered by rank. Only the first 100 000
  characters of a message are searchable.

**Visibility.** Every group contains only topics the caller could open, decided
per row by one SQL predicate that mirrors `kbaccess.EffectiveRole` rule for rule
(superadmin → owner; a `kb_members` row → that role; public + system admin →
admin; public + published → view; otherwise invisible). That is a superset of
`GET /kb/catalog`: published public topics the caller has not subscribed to are
included. Superadmins and system admins therefore see many results; that is
intended, not a leak.

**Chats are private.** `chats` and `messages` contain **only the caller's own
chats**, and only in topics the caller can still open under the rule above — a
chat in a topic the caller has lost access to is not returned. No role widens
this, superadmin included: members cannot read each other's chats anywhere in
the app, and search is no exception.

```json
{
  "query": "prüfung",
  "kbId": null,
  "topics": [
    {
      "id": "uuid",
      "name": "Prüfungsordnungen",
      "description": "First 160 characters of the description …",
      "visibility": "public",
      "role": "view",
      "match": "prefix"
    }
  ],
  "sources": [
    {
      "id": "uuid", "name": "PO-2024.pdf", "type": "pdf", "kbId": "uuid",
      "kbName": "Prüfungsordnungen", "match": "fuzzy"
    }
  ],
  "chats": [
    {
      "id": "uuid", "title": "Prüfungsfragen", "type": "chat", "kbId": "uuid",
      "kbName": "Prüfungsordnungen", "updatedAt": "RFC 3339", "match": "prefix"
    }
  ],
  "messages": [
    {
      "id": "uuid", "chatId": "uuid", "chatTitle": "Prüfungsfragen", "chatType": "chat",
      "kbId": "uuid", "kbName": "Prüfungsordnungen", "role": "ai",
      "snippet": "Laut der \ue000Prüfungsordnung\ue001 von 2024 …",
      "createdAt": "RFC 3339"
    }
  ]
}
```

- `kbId` echoes the (canonicalised) scope, or `null` for a global search.
- `topics[].description` is the description, falling back to the header text,
  whitespace-collapsed and cut at 160 characters (ending in `…` when cut);
  `null` when both are empty. `role` is the caller's effective KB role.
- `match` on topics, sources and chats is one of `prefix`, `substring`,
  `fuzzy` (see above). Message hits carry no `match`: they are always
  full-text hits.
- `chats[].type` and `messages[].chatType` are `chats.type` as stored:
  `chat`, `research` or `academic-research` (hyphen). `messages[].role` is
  `messages.role` as stored: `user` or `ai` (not `assistant`).
- `messages[].snippet` is **plain text**, never HTML: one fragment of about
  8–20 words around the best match, whitespace-collapsed. Each matched word is
  wrapped in **U+E000** (start) and **U+E001** (end), two Unicode private-use
  characters that are stripped from the content before the snippet is cut, so
  every occurrence is a highlight marker. A client splits on them and renders
  every part as a text node (the marked parts highlighted). HTML/XML tags in
  the content are dropped by `ts_headline`; any other `<` or `&` stays
  literal, so the snippet must never be inserted as HTML.
- All four arrays are always present (`[]` when nothing matched).

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
| GET | `/kb/{id}/invite-links` | admin |
| POST | `/kb/{id}/invite-links` | admin |
| DELETE | `/kb/{id}/invite-links/{linkId}` | admin |
| POST | `/invites/{token}/redeem` | authenticated (no KB role) |
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

An invite link is a permanent, revocable credential that grants the role it
was minted with. `POST /invites/{token}/redeem` is deliberately not KB-gated —
the caller has no role on the KB yet — and is rate-limited to 10 requests per
minute. Redeeming never lowers an existing role and never touches the owner.

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
| GET | `/kb/{id}/starter-questions` |
| GET | `/chat/rag-system-prompt` |
| GET | `/chats/{id}/messages` |
| PATCH | `/chats/{id}` |
| DELETE | `/chats/{id}` |
| POST | `/kb/{id}/chat` |
| POST | `/kb/{id}/chats/{chatId}/messages/{messageId}/feedback` |

`POST /kb/{id}/chat` takes an optional boolean `webSearch` (per-turn web search):

- absent — unchanged behaviour (the answer LLM gets the tools `chat_answer_tools_enabled` gives it);
- `true` — the answer LLM gets the `web_search` tool for this turn; requires `?stream=true`;
- `false` — `web_search` is kept out of this turn, even when `chat_answer_tools_enabled` is on.

A request with `webSearch: true` is refused with **422 Unprocessable Entity** — before a chat is created
or usage is recorded — when web search is not available on the server (admin gate
`chat_web_search_enabled`, `web_search_enabled` or the Google credentials), when the request is not
streaming, or when it also selects an agent or team (`teamId`/`agentId`). The error message is generic;
the reason is in the server log. When the admin's per-route tool allowlist removes `web_search` for the
turn's route, the turn is answered without it and the stream carries the trajectory event
`{"agentTrajectory":{"stage":"web_search","decision":"skipped","reason":"web search is not available for this turn"}}`;
the specific cause is only in the server log.

**`PATCH /chats/{id}`** renames a chat. Authenticated; only the chat's owner
may rename it. Body `{"title": "…"}` (at most 4 KiB); every whitespace run in the title,
line breaks included, collapses to one space, other control characters are
refused, and the result must be 1–200 characters (counted as Unicode
characters, not bytes). Renaming does not change
`updatedAt`, so the chat keeps its place in the history list.

| Status | When |
|---|---|
| `204` | Renamed (no body) |
| `400` | Invalid or oversize JSON, empty title, a control character, or title over 200 characters |
| `401` | Not authenticated |
| `404` | `{id}` is not a UUID, the chat does not exist, or it belongs to another user |

**`GET /chat/rag-system-prompt?lang=de|en`** returns the fixed answer
instructions that the default answer path appends after a KB's own system
prompt, so the system-prompt panel can show what a KB prompt is combined with:
`{"language": "de", "prompt": "…"}`. Authenticated, read-only, no KB scope.
`lang` selects the language; a missing or unsupported value falls back to `de`,
as for `POST /kb/{id}/chat`. It is not the complete prompt of a turn: per turn
the default path adds the current-date line, low-confidence/abstain notices,
enumeration/recency/conflict addenda and the retrieved context; memory blocks
and tabular guidance are added to the KB prompt when those features are on;
and the corpus-table path and the document-comparison summary without a team
use their own instructions instead. Answers `200`, or `401` when not
authenticated.

**`GET /kb/{id}/starter-questions?lang=de|en`** suggests up to 6 questions to
open an empty chat with: `{"questions": ["…"]}`. Requires `view` on the KB and
counts against its own `starter_questions` rate limit (30 / min per client IP,
not shared with `generate`). The questions are generated by the fast-tier
model (`model_tier_fast`, else the KB's chat model) from the KB name and one
excerpt per document — the document's top-level RAPTOR summary where
one exists, else its first chunk — for the 12 newest documents with status
`completed` or `partial`; files flagged by the ingest-time injection screen are
never used. Each served question is a single line of at most 120 characters,
without links, and the list carries no duplicates. Results are cached per KB,
language and document set (KB name plus document ids) for an hour, a failed
generation for a minute; the cache is per server process. `lang` falls back to
`de` like the other chat routes.

| Status | When |
|---|---|
| `200` | Questions, or `[]` when the KB has no eligible documents, no model is configured, or generation failed or produced no valid question |
| `401` / `403` / `404` | Not authenticated / no `view` role / KB not found |
| `429` | `starter_questions` rate limit exceeded |
| `500` | The KB's documents could not be read |

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

### Source attribution

`POST /chat/completions` returns the retrieved chunks that produced the answer,
in two complementary forms. Both are additive to the standard OpenAI envelope —
clients that ignore them are unaffected.

**`message.annotations[]`** — inline citations, following OpenAI's annotation
shape. One entry per `[n]` marker occurrence in the answer; a multi-cite marker
(`[1, 2]`) yields one entry per referenced source, all sharing the marker's
offset.

```json
{
  "type": "file_citation",
  "file_citation": { "file_id": "…", "filename": "handbuch.pdf", "index": 16 }
}
```

`index` is a **character** offset into `message.content` (not bytes), matching
OpenAI's definition — relevant because answers are frequently German.

**`message.context.citations[]`** — the retrieved chunk bodies, following the
Azure OpenAI "On Your Data" shape so OpenWebUI-family clients recognise them
without bespoke handling:

```json
{
  "content": "…chunk text…",
  "title": "handbuch.pdf",
  "filepath": "handbuch.pdf",
  "file_id": "…",
  "chunk_id": "…",
  "score": 0.91,
  "pages": [3]
}
```

Both keys are **omitted entirely** when retrieval returned nothing or the
answer cites nothing — they are never emitted as empty arrays, so clients can
branch on presence.

**Streaming.** Retrieval completes before the first token, so the sources ride
the opening chunk and the annotations ride the closing one:

| Chunk | Carries |
|---|---|
| first (`delta.role = "assistant"`) | `delta.context.citations[]` |
| last (`finish_reason = "stop"`) | `delta.annotations[]` |

This lets a client render source cards while the answer is still streaming;
annotation offsets only exist once the full text is assembled.

**Caveat.** This endpoint does not run the citation validator (that is a
post-response task on the in-app chat path), so annotations reflect what the
model claimed, not what was verified. Markers referencing a source that was
never retrieved are dropped rather than emitted as dangling annotations.

## Rate Limiting

The Go server applies per-category Redis-backed rate limits:

| Category | Limit | Endpoints |
|---|---|---|
| login | 5 / 15 min | `POST /api/auth/login` |
| chat | 20 / min | `POST /api/kb/{id}/chat` |
| research | 5 / min | `POST /api/kb/{id}/research`, `POST /api/kb/{id}/web-research` |
| generate | 10 / min | `POST /api/kb/{id}/generate/*` |
| starter_questions | 30 / min | `GET /api/kb/{id}/starter-questions` |
| api | 100 / min | `/api/v1/*`, `/openai/v1/*` |
| search | 60 / min **per user** | `GET /api/search` |

Limits are counted per client IP, except `search`, which runs after
authentication and is counted per user.
