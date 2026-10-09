# User file library API contract (phases 1–4)

Backend: `go-backend/internal/userfiles`, `internal/files/from_library.go`, `internal/cascade`.
The frontend builds against this document. Everything here matches the merged code.

## Conventions

- All endpoints require authentication (same as the rest of `/api`). Unauthenticated: `401 {"error":"Authentication required"}`.
- JSON keys are camelCase. Optional values are omitted, never `null`.
- Every error body is `{"error": "<string>"}`, except the two structured ones (`quota_exceeded`, `already_in_kb`) below.
- Library endpoints are **owner-only**: a user only ever sees their own files, a superadmin included. Another owner's id and a malformed id both return `404 {"error":"File not found"}` (never 403).
- 500 errors carry a sanitized message; show a generic failure text.

## Shapes

```
UserFile = {
  id: string,            // uuid
  name: string,          // display name, editable via PATCH (1..255 bytes)
  mime: string,
  size: number,          // bytes
  createdAt: string,     // RFC 3339
  kbs: KBLink[]          // always present, [] when in no KB
}
KBLink  = { kbId: string, fileId: string, status: string }
          // fileId = the KB's indexed copy (files row); status = that copy's ingest
          // status (pending | processing | completed | partial | error).
          // partial = ingested, but some optional stages failed; the file is searchable, treat like completed
KBUsage = { id: string, name: string, visibility: "private"|"public", memberCount: number }
```

Not exposed: owner id, sha256, storage path.

## Library endpoints

### GET /api/library/files

Query: `limit` (default 50, clamped 1..500), `offset` (default 0, negatives ignored). Newest first.

200:
```json
{
  "items": [
    {"id":"7b0c9f64-2b1e-4a39-9d51-0a6a1f0e2c11","name":"report.pdf","mime":"application/pdf","size":482113,
     "createdAt":"2026-10-08T09:12:44Z",
     "kbs":[{"kbId":"3f2a8c10-5d7e-4b1a-8f3c-6e9d2a4b7c01","fileId":"91d4e6a2-0c3b-4f58-a7d1-2b8e5c9f3a10","status":"completed"}]}
  ],
  "total": 1
}
```
`items` is `[]` when empty. `total` is the unpaged count.

### POST /api/library/files

`multipart/form-data`, field `file`. Transport cap 500 MiB. Uploads into the library only (no KB, no indexing).

| Status | Body |
|---|---|
| 201 | `UserFile` (new file) |
| 200 | `UserFile` plus `"deduplicated": true`: this user already holds identical bytes; the existing file is returned, nothing is stored or charged to quota |
| 400 | `{"error":"Invalid multipart form"}`, `"File field is required"`, `"File must not be empty"`, `"File type not allowed"`, `"File type not supported (.ext)"`, `"Filename must not exceed 255 bytes"` |
| 413 | transport cap: `{"error":"File too large: the upload limit is 500 MiB"}`; spreadsheet gate: `{"error":"Spreadsheet too large (X): the limit is Y (tabular_max_file_bytes)"}` |
| 413 | quota: `{"error":"quota_exceeded","usedBytes":1048576000,"quotaBytes":1073741824}` |

201 example:
```json
{"id":"7b0c9f64-2b1e-4a39-9d51-0a6a1f0e2c11","name":"report.pdf","mime":"application/pdf","size":482113,"createdAt":"2026-10-08T09:12:44Z","kbs":[]}
```

Dedup is per user and by content hash. Different users uploading the same bytes each get their own file. The quota check happens before the hash is known (see "Quota semantics").

### GET /api/library/files/{id}

200 `UserFile`. 404 `{"error":"File not found"}`.

### PATCH /api/library/files/{id}

Body `{"name":"new name.pdf"}`. The name is trimmed. Renames the library entry only (KB copies keep their own names). The file extension cannot change (compared case-insensitively), and the new name must pass the upload filename rules (no dangerous or unsupported extension, at most 255 bytes): the extension decides which parser and size gate a KB copy gets.

200 `UserFile`. 400 `{"error":"invalid name"}` (empty/whitespace-only, longer than 255 bytes, changed or dangerous/unsupported extension, or unparseable body). 404.

### DELETE /api/library/files/{id}

**Removes the file from every KB it was added to** (each KB copy with its chunks, graph and tabular data), then the library entry and the blob. Irreversible.

204 no body. 404 `{"error":"File not found"}`. 500 on partial failure (the library entry is kept so the delete can be retried).

UI: before calling this, fetch `GET .../usage` and confirm with the user, listing the KBs. Deleting a user account deletes their whole library the same way: **KB uploads made after this release are library files of the uploader, so deleting that account removes them from every KB, public KBs included** (uploads from before this release are unaffected). An admin impact preview for this (`GET /api/admin/users/{id}/file-impact`, spec section 6) is **not** part of phase 1; the admin UI cannot show the consequence yet.

### GET /api/library/files/{id}/download

200 with the raw bytes, `Content-Disposition: attachment; filename="..."; filename*=UTF-8''...`, `Content-Type` = the file's mime. 404 `{"error":"File not found"}` (or `"File has no storage path"`). 500 `{"error":"Failed to read file"}`.

### GET /api/library/files/{id}/usage

KBs currently holding a copy (impact preview for the delete dialog). `memberCount` is the number of members of that KB.

200:
```json
{"kbs":[{"id":"3f2a8c10-5d7e-4b1a-8f3c-6e9d2a4b7c01","name":"Handbuch","visibility":"public","memberCount":14}]}
```
`kbs` is `[]` when unused. 404.

### GET /api/library/quota

200: `{"usedBytes":1048576,"quotaBytes":0}`. `quotaBytes: 0` means **unlimited**. `usedBytes` sums the library files' sizes; a file added to N KBs counts once.

## KB endpoints

### POST /api/kb/{id}/files (changed)

Auth: KB role `edit` or higher (unchanged). Multipart `file`. Validation, status codes, messages and the 201 `FileRecord` shape are unchanged, with these differences:

- The bytes now also land in the uploader's library (deduplicated, quota-charged). The 201 `FileRecord` gains `"userFileId": "<uuid>"`.
- New 413 `{"error":"quota_exceeded","usedBytes":N,"quotaBytes":M}` when the upload would exceed the uploader's library quota.
- New 409 when the KB already holds a copy of the same library file (same bytes, same uploader):
  `{"error":"already_in_kb","fileId":"<existing KB file id>"}`.
  `fileId` is present except in a rare concurrent-upload race, where the body is just `{"error":"already_in_kb"}`. Do not depend on `fileId`.

Quota gotcha: the quota is checked before the content hash is known. A user who is at their quota and re-uploads bytes they already own gets 413 (not 409, not a dedup). To add a file that is already in the library to a KB, call `POST /api/kb/{id}/files/from-library`, which never charges quota. Offer "add from library" in the UI rather than re-uploading.

### POST /api/kb/{id}/files/from-library

Auth: KB role `edit` or higher. Adds the caller's library files to the KB without re-uploading. Each added file becomes a new KB file that is ingested (chunked, embedded) as usual.

Request: `{"userFileIds":["7b0c9f64-2b1e-4a39-9d51-0a6a1f0e2c11","c1e2d3f4-1111-4a2b-9c3d-5e6f7a8b9c0d"]}` (1..100 ids).

200 (whenever the request itself is valid; per-id problems are reported in `skipped`):
```json
{
  "added":   [{"fileId":"91d4e6a2-0c3b-4f58-a7d1-2b8e5c9f3a10","userFileId":"7b0c9f64-2b1e-4a39-9d51-0a6a1f0e2c11"}],
  "skipped": [{"userFileId":"c1e2d3f4-1111-4a2b-9c3d-5e6f7a8b9c0d","reason":"already_in_kb"}]
}
```
Both arrays are always present (possibly empty). `reason`:

| reason | meaning |
|---|---|
| `not_found` | not in the caller's library (another owner's id and malformed ids land here too) |
| `already_in_kb` | the KB already holds this library file |
| `kb_full` | the KB's file-count cap (500; 1000 for public KBs) or 500 MB total-size cap would be exceeded |

Errors: 400 `{"error":"userFileIds must contain 1-100 ids"}`, 401, 403 (no `edit` role on the KB), 404 (unknown KB), 500 (`"failed to queue file for processing"`, `"Failed to create file record"`, `"Failed to check KB limits"`).

### Deleting a KB copy

`DELETE /api/files/{fileId}` (existing) removes only the KB copy; the library file stays. The KB file's `userFileId` links back to it: `GET /api/kb/{id}/files` rows carry `"userFileId": "<uuid>"` for library-backed copies (key omitted otherwise), visible to every role that can list files.

The 200 response of a deduplicated `POST /api/library/files` (bytes already in the library) lists the KBs the existing file is already in under `kbs`.

### Adopting legacy uploads

`POST /api/kb/{id}/files/adopt` moves uploads made before the library existed (`origin = "upload"`, no `userFileId`) into their uploader's library. The KB copy keeps its `files.id`, its chunks, graph and tabular data: the **index is untouched**; only the file's blob moves to `users/<uid>/<userFileId>` and the row gains `userFileId`.

Authorization (stricter than the route's KB-admin gate):

- Private KB: the KB `owner` (a superadmin resolves to owner).
- Public KB: a system `admin` or `superadmin`.
- Anyone else, including a plain KB `admin`: `403 {"error":"Insufficient permissions"}`. Unknown KB: 404, no token: 401.

Body: `{"fileIds": ["<files.id>", ...]}`, 1..100 ids, otherwise `400 {"error":"fileIds must contain 1-100 ids"}` (a malformed body is the same 400).

Response `200`:

```json
{
  "adopted": [{"fileId": "<files.id>", "userFileId": "<UserFile.id>"}],
  "skipped": [{"fileId": "<files.id>", "reason": "already_library"}]
}
```

Both arrays are always present (possibly empty). Per-file problems never fail the request; `reason` is one of:

| `reason` | Meaning |
|---|---|
| `not_found` | no such file in this KB (also: malformed id, file of another KB) |
| `not_upload` | `origin` is not `upload` (RSS, Confluence, repository, crawl, text, URL, academic) |
| `already_library` | already library-backed, or a concurrent adoption / an existing copy of the same library file in this KB won |
| `quota_exceeded` | the target user would exceed their quota (nothing is written) |
| `blob_missing` | the stored object is gone, or the row has no storage path |
| `busy` | the file's `status` is `pending` or `processing` (an ingestion/re-embed task may still read the old path); retry once it is `completed`/`error` |
| `duplicate_in_kb` | the KB already holds another copy of the same library file (identical bytes uploaded twice); this row stays a legacy upload |

Target user per file: `uploadedBy` when that user still exists, otherwise the caller. The file then appears in that user's library (`GET /api/library/files`) with this KB under `kbs` (`GET /api/library/files/{id}/usage`). If the target already owns the same bytes (same SHA-256), the KB copy is linked to that existing library file: no copy, no quota change. `uploadedBy` is set to the caller when it was empty. The old blob is deleted only when no other file row still references it. An unexpected infrastructure error answers `500 {"error":"Internal Server Error"}`; files handled before it stay adopted but are not reported, so retrying the same ids is safe (they come back as `already_library`).

Ownership: after adoption the target user (normally the uploader) owns the library file and can delete it everywhere, including this KB's copy and its index, even if they are no longer a member of the KB (the same semantics as phase-1 uploads).

## Quota semantics

- Effective quota = per-user override (`users.file_quota_bytes`) if set, else the global site_config key `user_file_quota_bytes`; `0` = unlimited. The default is unlimited, so nothing changes until an operator sets one.
- Usage counts each library file once, regardless of how many KBs hold it. An upload that brings usage exactly to the quota is allowed; one byte over is rejected with 413.
- Checked on both `POST /api/library/files` and `POST /api/kb/{id}/files`.
- Frontend handoff: there is no admin-UI field for the global key or the per-user override yet. The override lives in `users.file_quota_bytes` (DB only for now). Always read the current limit from `GET /api/library/quota`.

## Not in phase 1

Text, URL, crawl and academic imports remain KB-scoped files (no `userFileId`, not in the library). Files uploaded before phase 1 are not library files. `parseStatus` / `parseError` do not exist (the phase-2 parse cache is internal and not exposed). KB-less chat is phase 3 (see below).

## Phase 2 note: faster completion

Phase 2 adds no API change. However, `POST …/from-library` copies (and uploads rerouted through the library) may now be completed by a server-side index copy: the KB file's status can reach `completed` quickly without passing through the usual stage progression (`stage_detail` steps). The frontend must not assume intermediate stages are ever observed.

## Phase 3: Library chat (KB-less chat with your files)

Backend: `go-backend/internal/chat/library_{http,context,text}.go`, migration 0081. No KB, no retrieval index, no orchestrator: the selected files' parsed text is the context. Owner-only; the frontend builds against this section.

### Conventions

- All endpoints require authentication (`401`) and are owner-only. A malformed, missing, foreign or non-library chat id (any KB chat included) answers `404 {"error":"chat not found"}`; a malformed, missing or foreign file id answers `404 {"error":"file not found"}` (never 403/500, no file named). Error bodies are `{"error": "<string>"}`.
- If the library chat service is not wired: `503 {"error":"library chat is not available"}`.
- A library chat is an ordinary `chats` row with `type = "library"` and no KB. The KB chat list `GET /api/kb/{id}/chats` (and every other KB-scoped chat list) never contains it; use `GET /api/library/chats`.

### POST /api/library/chat[?stream=true]

Same rate limiter as the KB chat send route. Body (JSON):

| Field | Notes |
|---|---|
| `message` | required, at most 32,000 characters, same content validation as KB chat |
| `fileIds` | array of library file ids (`UserFile.id`), 1..20 for a new chat. Optional on an existing chat |
| `chatId` | omit to start a new chat |
| `parentMessageId` | message to branch from; ignored for a new chat. An id that is malformed, nonexistent or from another chat is ignored (stored as no parent) and the turn uses the chat's linear history |
| `language` | `de` or `en`, anything else falls back to the default |
| `reasoningEnabled`, `reasoningLevel` | as in KB chat |

File selection rules:

- New chat: `fileIds` required. It becomes the chat's stored selection.
- Existing chat with `fileIds`: the body list **replaces** the stored selection (the chat's files are then exactly those ids).
- Existing chat without `fileIds`: the stored selection is used. Files deleted from the library since drop out of it silently; if none are left the turn is `400 "no library files selected"`.
- Duplicate ids collapse (first occurrence wins).
- All rejections happen before a chat is created, a selection replaced or a usage event recorded. A new chat is also removed again if saving its selection fails.

Errors:

| Status | `error` |
|---|---|
| 400 | `invalid request body`, `message is required`, `message exceeds maximum length of 32,000 characters`, `message contains disallowed content` |
| 400 | any `regenerateOfMessageId` is rejected: `regenerateOfMessageId requires chatId` (no `chatId`), `regenerateOfMessageId must be a message id` (malformed id) — both from the shared KB body validation, checked first — else `regenerate is not supported for library chats` |
| 400 | `at most 20 library files can be selected` |
| 400 | `no library files selected` (new chat without `fileIds`, or all stored files deleted) |
| 400 | `the file "<name>" cannot be read as text` (no parser, or the parser failed; the cause is never echoed) |
| 400 | `the file "<name>" took too long to read` (the server-side parse of that file ran past 120 s; a later retry may succeed) |
| 400 | `the selected files contain no text` |
| 400 | `selected files are too large for one chat turn (<tokens> tokens, maximum <max>)`, or `(at least <tokens> tokens, maximum <max>)` when a cheap pre-check rejected the selection before counting exactly (`<tokens>` is then a lower bound) |
| 404 | `chat not found`, `file not found` (also when a file is deleted in the instant between validation and saving the selection) |
| 500 | `failed to fetch chat`, `failed to create chat`, `failed to save library files`, `failed to load library files`, `failed to read library file`, `failed to prepare context`, `failed to save user message` |

**Where errors appear in stream mode.** The checks that need no file parsing — body validation, chat ownership, file ownership/selection (`invalid request body` … `no library files selected`, `chat not found`, a `file not found` for a body id, `failed to fetch chat`, `failed to load library files`) — answer with the status code and JSON body above in both modes. After them, a streaming request is committed to SSE (`200`, `text/event-stream`) and its first frame is `{"stage":"library_prepare"}` (a trajectory-event-shaped frame sent before the files are parsed, so a slow parse cannot idle-time-out a proxy). Every error from then on (`cannot be read as text`, `took too long to read`, `contain no text`, `too large`, the refs-race `file not found`, `failed to read library file`, `failed to prepare context`, `failed to create chat`, `failed to save library files`, `failed to save user message`, and the KB-path stream errors like `failed to start AI stream`) arrives as an SSE frame `{"error": "<same message>"}` followed by `[DONE]` — the KB chat's post-SSE error shape. Non-streaming requests return those as status code + JSON exactly as listed.

Audio files are **not supported**: library chat parses with the built-in parsers only (no Docling, no transcriber), so an audio file is `400 the file "<name>" cannot be read as text`.

Parsing happens on demand the first time a file is used in a chat and is cached afterwards. A parse that finishes after the client disconnected is still cached, so retrying a turn on a large scanned PDF gets faster.

**Parse progress frames (stream mode only).** After `{"stage":"library_prepare"}` and before each selected file's text is resolved (cache hit or server-side parse), the server sends and flushes `{"stage":"library_parse","file":"<file name>","index":<0-based position>,"total":<number of selected files>}`. They keep the connection alive during long parses and let the UI show "reading file i of n". They arrive in selection order, one per file, before the opening `chatId` frame; a request that fails at file k has already emitted frames 0..k. Non-streaming requests never emit them. Clients that do not know the stage should ignore it, like any other trajectory frame.

Response. Non-streaming: the KB chat JSON body (`answer`, `reasoning`, `sources`, `enhancedQuery`, `chatId`, `userMessageId`, `aiMessageId`, `followUpQuestions`, `verification`, ...). Streaming (`?stream=true`): first `{"stage":"library_prepare"}`, then one `{"stage":"library_parse",...}` per file, then the same SSE frames in the same order as KB chat (opening `sources` / ids frames, trajectory events, `content` / `reasoning` deltas, `aiMessageId`, `followUpQuestions`, `verification`, `[DONE]`), including the degenerate-run guard. Read `chatId` from the opening frame (the first frame carrying `chatId`; it follows the prepare and parse frames, so do not rely on its position) to continue the conversation.

### Sources: `userFileId`

Each `ChatSource` of a library turn carries `userFileId` (the library file's id). `fileId` is always present and always the empty string `""` (the key is not omitted) — it is not a KB file. Render citations and links from `userFileId` (for example `GET /api/library/files/{id}/download`); never from `fileId`. `pages` is set for paged formats. `createdAt` / `publishedAt` are absent. The same shape is persisted in the message and returned on reload. KB chats never carry `userFileId`.

A source is one page of a file, or a piece of at most 1,500 tokens of one: a large page, or a file without page structure (plain text, Markdown, …), is split, so one file can yield many sources, several with the same `pages`.

`content` on a library source is a **snippet**: at most the first 600 characters (Unicode code points) of that page/piece, both in the stream and in the persisted message / reload. The answer model and citation validation saw the full text; the snippet is for display only.

### Budget semantics

Total size of the selected files' text is measured in tokens:

1. At most `chat_library_fulltext_max_tokens` (default 60,000, range 4,000..200,000, global-only): the **full text** goes to the answer model, one source per page or ≤ 1,500-token piece (see above).
2. Above that, up to `chat_longcontext_max_tokens` (default 100,000): a map_reduce pass extracts findings per chunk group first and answers from them. This tier exists only when `chat_library_fulltext_max_tokens` is below `chat_longcontext_max_tokens`; with the full-text budget at or above it, a selection is either full text or tier 3. The source list is still the **whole page pool** (every page/piece of the selection, in selection order), not only the pages findings came from. Findings are capped to fit the model context: if there are too many, the tail findings are dropped (logged, trajectory event), so on very large selections some material can be missing from the answer.
3. Above the larger of the two budgets (normally `chat_longcontext_max_tokens`; `<max>` in the message is that value): `400 selected files are too large for one chat turn (...)`. Nothing is truncated silently; ask the user to select fewer files.

A selected file that yields no text (for example a scanned PDF without OCR text) is skipped and, **in stream mode only**, named in a trajectory event `{"stage":"library_files_skipped","files":["a.pdf"]}` (the non-streaming response has no equivalent field); those files are not used for the answer, so the UI should tell the user. All files empty is the `400 the selected files contain no text` above.

### GET /api/library/chats

200 `{"items": LibraryChat[]}`, only the caller's library chats, newest activity first. `items` is `[]` when empty. `Cache-Control: no-cache`. 500 `failed to fetch chats`.

```
LibraryChat = {
  id: string, title: string,       // title = first 50 characters of the first message
  createdAt: string, updatedAt: string,   // RFC 3339
  fileIds: string[]                // the stored selection, [] if none
}
```

`fileIds` is the stored selection and may still contain ids of files deleted since (the reference rows cascade away; re-fetch after a delete). Cross-check against `GET /api/library/files` if you display names.

### GET /api/library/chats/{id}

200 `LibraryChat`; 404 `chat not found`; 500 `failed to fetch chat`.

### Reused endpoints

- `GET /api/chats/{id}/messages` returns the chat's messages (owner-scoped, same shape as KB chats; assistant messages carry `sources` with `userFileId`).
- `DELETE /api/chats/{id}` deletes the chat (204); its stored selection is removed with it. Library files are untouched.

### What library mode does not do

Compared with a KB chat a library turn skips: long-term and session memory, answer-time tools, the tabular router/log, conflict surfacing, factuality verifier / refine / RAGAS, span-verified citations, source-date enrichment, `message_chunks` and `agent_decisions` rows. It keeps conversation history, the degenerate-run guard, citation validation and follow-up questions. It never reports low confidence (a full-text context legitimately has few sources). Usage is recorded in the ledger as a web turn with no KB.
