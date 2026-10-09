# Agent chat API contract (AG-UI, ADK phase 2)

Backend: `go-backend/internal/chat/agentchat_*.go` (flow, handler, hooks, dead-end actions), `internal/agui` (AG-UI wire: request decoding, event translation, status codes), `internal/adkbridge` (tool policy, run lifecycle, sessions).
The frontend builds against this document. Everything here matches the code at the commit that adds this file, and the event sequences below were recorded from a live server.

Protocol: [AG-UI](https://docs.ag-ui.com). Any AG-UI client (for example `@ag-ui/client`'s `HttpAgent`) can drive this endpoint. This document describes only what this server sends and accepts.

## Conventions

- One endpoint: `POST /api/kb/{id}/agui/chat`. Auth as for the other chat routes (`Authorization: Bearer <jwt>` or the session cookie). KB role `view` or higher.
- It is a **separate endpoint** from the legacy `POST /api/kb/{id}/chat`. Both write into the same `chats`/`messages` tables, so an agent chat shows up in `GET /api/kb/{id}/chats` and its messages in `GET /api/chats/{id}/messages` like any other chat.
- **Feature flag:** per KB, `chat_agent_chat_enabled` (default off). It is set per KB via `PUT /api/kb/{id}/settings` `{"configs":{"chat_agent_chat_enabled":"true"}}`; that route needs **both** a system role `api-user`, `admin` or `superadmin` **and** KB role `admin` (the KB advanced-settings gate). While it is off the endpoint answers `404 {"error":"not_found"}` for that KB. Hide the agent-chat UI then.
- Errors before the stream starts are JSON `{"error":"<string>"}` with a non-200 status (table below). Once the response is `200 text/event-stream`, errors arrive as a `RUN_ERROR` event instead.
- Provider and database error text never reaches the client. Show a generic failure text for `500`, `RUN_ERROR` and tool errors reading `tool failed`.
- Rate limit: shared with the legacy chat send route (the `chat` limiter, 20 requests per minute). `429 {"error":"Too many requests from this IP, please try again later."}` with a `Retry-After` header. A resume counts as a request.

## Thread = chat

`threadId` **is** the chat id (a UUID).

- New conversation: send `threadId: ""` (or omit it). The server mints a chat id and creates the chat (title = the first 50 characters of the question) only once the turn actually starts. `RUN_STARTED.threadId` carries the new id. Keep it and send it on every later turn and resume.
- Continuing: send the chat id. It must be a chat the caller owns **in this KB** (a chat started in the legacy chat qualifies too; its earlier legacy turns are not part of the agent's history). Anything else (a non-UUID, an unknown id, another user's chat, a chat of another KB) answers `404 {"error":"thread_not_found"}`.
- History: the server keeps the conversation (ADK session + `messages`). It never trusts history from the client (see Request).
- Reload: read the transcript through the existing `GET /api/chats/{id}/messages`. Agent turns are ordinary messages: the user message (`role: "user"`) and, for a completed turn, the answer (`role: "ai"`, with `sources` and `reasoning`). A turn that paused at a dead end stored only the user message; the pause itself is not a message (see "Open interrupts after a reload").

## Request: `RunAgentInput`

Body cap 1 MiB. `Content-Type: application/json`.

```json
{
  "threadId": "a9670fd6-3162-41e6-b1c6-0438cc913c15",
  "runId": "",
  "messages": [{"id": "m2", "role": "user", "content": "Wie viele Urlaubstage habe ich?"}],
  "tools": [], "context": [], "state": {},
  "forwardedProps": {"reasoning": "low", "language": "de"}
}
```

| Field | Used? | Meaning |
|---|---|---|
| `threadId` | yes | chat id, or `""` for a new chat (see above) |
| `runId` | yes | optional. Any id that parses as a UUID (also upper-case, braced or `urn:uuid:` forms) is used in its canonical lower-case form; an empty or malformed id, or one that already exists, is replaced by a fresh one. `RUN_STARTED.runId` is the id actually used |
| `messages` | **only the newest `role: "user"` message with non-blank text** | `content` may be a string or an array of parts; only `{"type":"text","text":"..."}` parts are read. Every other message, including earlier turns and any `assistant`/`tool` message, is ignored: the server builds history from its own store. Sending only the new message is enough |
| `resume` | yes | answers an interrupt (see "Resume") |
| `forwardedProps.reasoning` | yes | `"low"`, `"medium"` or `"high"` turns model reasoning on at that effort (then `REASONING_*` events stream before the text). Anything else, or absent: off |
| `forwardedProps.language` | yes | `"de"` or `"en"`: language of the retrieval prompt. Anything else, or absent: `"de"` |
| `tools`, `context`, `state`, `parentRunId` | **ignored** | Client-side tools are not offered to the model in this phase. The frontend "tools" of a dead end (below) are run by the client before it resumes, not declared here |

**Message validation** (new messages only, before anything is written or sent to a model): the same checks as the legacy chat.

| Status | Body |
|---|---|
| 400 | `{"error":"message exceeds maximum length of 32,000 characters"}`: the limit is 32 000 **bytes** of UTF-8 (as in the legacy chat; an umlaut counts twice) |
| 400 | `{"error":"message contains disallowed content"}`: the prompt-injection heuristic matched (for example "ignore all previous instructions") |

## Status codes before the stream

| Status | `error` | Cause |
|---|---|---|
| 401 | `Authentication required` | no or invalid token (auth middleware) |
| 403 | `Insufficient permissions` | no `view` role on the KB (KB middleware) |
| 404 | `Knowledge base not found` | unknown KB id (KB middleware) |
| 404 | `not_found` | `chat_agent_chat_enabled` is off for this KB |
| 400 | `bad_request` | body is not valid JSON, or larger than 1 MiB |
| 400 | `message exceeds maximum length of 32,000 characters`, `message contains disallowed content` | see above |
| 400 | `no_input` | no resume and no user message with text |
| 400 | `resume_requires_thread` | `resume` without `threadId` |
| 400 | `bad_resume` | the thread has an open interrupt, but `resume` names a different (or unknown) interrupt id |
| 404 | `thread_not_found` | `threadId` is not a chat the caller owns in this KB |
| 404 | `no_open_interrupt` | `resume` on a thread with nothing open. Also the answer to a **second** resume of the same interrupt: the first resume won, nothing is open any more (exactly-once) |
| 409 | `thread_has_open_interrupt` | a new message on a thread that is paused at a dead end. Resume (or cancel) the interrupt first, or start a new chat |
| 409 | `interrupt_expired` | the interrupt is older than 24 h. The server then closes it; a retry answers `404 no_open_interrupt`, and a new message on the thread works again |
| 409 | `interrupt_mismatch` | `resume` does not answer exactly the set of open interrupts. Unreachable with this flow (one interrupt per pause); listed for completeness |
| 409 | `thread_kb_mismatch` | defensive: a thread bound to another KB. In practice a foreign-KB thread is already refused as `404 thread_not_found` |
| 429 | `Too many requests from this IP, please try again later.` | rate limit |
| 500 | `internal_error` | anything else (logged server-side) |
| 200 | (SSE stream) | |

Only `POST` is routed. Other methods do not reach this handler.

## The stream

`200`, `Content-Type: text/event-stream`, `Cache-Control: no-cache`. Each event is one SSE frame: `id: <TYPE>_<unix-ms>` and `data: <json>`. Every JSON object carries `type` and `timestamp` (unix ms). The examples below omit `id:` lines and `timestamp`.

Event types this server sends:

| Event | Notes |
|---|---|
| `RUN_STARTED` | always first. `threadId` (the chat id, also for a new chat) and `runId` |
| `STEP_STARTED` / `STEP_FINISHED` | `stepName` is the workflow node. On a fresh turn the names are `agentchat/retrieve`, `agentchat/answer`, `agentchat/sources`, `agentchat/suggest`, `agentchat/dead_end`; on a resume they are `act` and the node after it (for example `web_answer`), without the `agentchat/` prefix. **Match on the last path segment**: `retrieve`, `answer`, `sources`, `suggest`, `dead_end`, `act`, `web_answer` |
| `TEXT_MESSAGE_START` / `_CONTENT` / `_END` | the assistant text, `role: "assistant"`. Streamed in deltas for model answers; fixed texts (dead ends, action outcomes) arrive as one delta |
| `REASONING_START` / `REASONING_MESSAGE_START` (`role: "reasoning"`) / `REASONING_MESSAGE_CONTENT` / `REASONING_MESSAGE_END` / `REASONING_END` | only with `forwardedProps.reasoning`; before the text |
| `TOOL_CALL_START` / `_ARGS` / `_END` / `TOOL_CALL_RESULT` | an answer-time tool call by the model (`kb_search`, `memory_read`, `memory_write`). `TOOL_CALL_RESULT.content` is a JSON **string**: `{"result":"<text>","chunks":[...]}` (chunks only for `kb_search` with hits) or `{"error":"<text>"}` |
| `CUSTOM` `justrag.tool_chunks.v1` | right after a `TOOL_CALL_RESULT` that carried chunks (see Custom events) |
| `CUSTOM` `justrag.sources.v1` | once per answered turn, in the `sources` step: the turn's numbered sources |
| `CUSTOM` `justrag.message.v1` | once per **completed** turn whose answer was stored: the stored message id. Comes right before `RUN_FINISHED` |
| `RUN_FINISHED` | `outcome: {"type":"success"}`, or `outcome: {"type":"interrupt","interrupts":[...]}` at a dead end |
| `RUN_ERROR` | `{"type":"RUN_ERROR","message":"run failed"}`. Ends the stream instead of `RUN_FINISHED` |

Not sent in this phase: `MESSAGES_SNAPSHOT`, `STATE_SNAPSHOT`/`STATE_DELTA`, and no follow-up-question, verification or confidence events: the legacy chat's post-response tasks do not run on this endpoint.

### 1. Normal answer

Recorded (text deltas shortened):

```
data: {"type":"RUN_STARTED","threadId":"a9670fd6-3162-41e6-b1c6-0438cc913c15","runId":"7b96267e-12b3-4eae-b021-916e39139a97"}
data: {"type":"STEP_STARTED","stepName":"agentchat/retrieve"}
data: {"type":"STEP_FINISHED","stepName":"agentchat/retrieve"}
data: {"type":"STEP_STARTED","stepName":"agentchat/answer"}
data: {"type":"TEXT_MESSAGE_START","messageId":"1be3c26f-2185-46a2-b53b-5156bf864738","role":"assistant"}
data: {"type":"TEXT_MESSAGE_CONTENT","messageId":"1be3c26f-…","delta":"Basierend auf dem Handbuch"}
  … 83 deltas in total …
data: {"type":"TEXT_MESSAGE_END","messageId":"1be3c26f-…"}
data: {"type":"STEP_FINISHED","stepName":"agentchat/answer"}
data: {"type":"STEP_STARTED","stepName":"agentchat/sources"}
data: {"type":"CUSTOM","name":"justrag.sources.v1","value":[{"index":1,"fileName":"urlaub.md","fileId":"077db233-f2bd-4b58-b12b-3ccb460f6a19","content":"# Handbuch Urlaubsregelung\n\nBeschäftigte haben Anspruch auf 30 Arbeitstage …","score":1,"pages":null,"nodeKind":"leaf"}]}
data: {"type":"STEP_FINISHED","stepName":"agentchat/sources"}
data: {"type":"CUSTOM","name":"justrag.message.v1","value":{"aiMessageId":"5e57aaa6-c5f2-4107-be00-f28b48d0e77a","chatId":"a9670fd6-3162-41e6-b1c6-0438cc913c15"}}
data: {"type":"RUN_FINISHED","threadId":"a9670fd6-3162-41e6-b1c6-0438cc913c15","runId":"7b96267e-12b3-4eae-b021-916e39139a97","outcome":{"type":"success"}}
```

The answer cites sources as `[n]`; `n` is `index` in `justrag.sources.v1`. If the model searches again mid-answer, `TOOL_CALL_*` + `justrag.tool_chunks.v1` appear inside the `answer` step, and the extra hits continue the numbering in the final `justrag.sources.v1` (a chunk seen twice keeps its first number).

With `forwardedProps.reasoning`, the `answer` step starts with the `REASONING_*` block (recorded: `REASONING_START`, `REASONING_MESSAGE_START`, 180 × `REASONING_MESSAGE_CONTENT`, `REASONING_MESSAGE_END`, `REASONING_END`), then the text.

### 2. Dead end with an interrupt

The `retrieve` step routes a turn to a dead end when the KB has no files (`no_files`) or retrieval finds nothing (`no_evidence`). The run then pauses with suggestions. Recorded on an empty KB, as the KB owner:

```
data: {"type":"RUN_STARTED","threadId":"a9670fd6-3162-41e6-b1c6-0438cc913c15","runId":"142708f3-9f92-4fd7-854a-d80c001490ae"}
data: {"type":"STEP_STARTED","stepName":"agentchat/retrieve"}
data: {"type":"STEP_FINISHED","stepName":"agentchat/retrieve"}
data: {"type":"STEP_STARTED","stepName":"agentchat/suggest"}
data: {"type":"STEP_FINISHED","stepName":"agentchat/suggest"}
data: {"type":"RUN_FINISHED", …}
```

The `RUN_FINISHED` in full:

```json
{
  "type": "RUN_FINISHED",
  "timestamp": 1791471065690,
  "threadId": "a9670fd6-3162-41e6-b1c6-0438cc913c15",
  "runId": "142708f3-9f92-4fd7-854a-d80c001490ae",
  "outcome": {
    "type": "interrupt",
    "interrupts": [{
      "id": "suggest-44544675-864c-4e50-a90d-76e7152116d3",
      "reason": "no_files",
      "message": "Diese Wissensbasis enthält noch keine Dateien. Was möchtest du tun?",
      "expiresAt": "2026-10-09T14:51:05Z",
      "metadata": {
        "reason": "no_files",
        "query": "Was steht im Handbuch zur Urlaubsregelung?",
        "actions": [
          {"id": "library", "tool": "library_add_to_kb", "frontendTool": "pick_library_file",
           "label": "Datei aus meiner Bibliothek hinzufügen", "sideEffect": "kb_write", "requiresRole": "edit"},
          {"id": "upload", "tool": "library_add_to_kb", "frontendTool": "upload_file",
           "label": "Datei hochladen", "sideEffect": "kb_write", "requiresRole": "edit"},
          {"id": "confluence", "tool": "confluence_import", "frontendTool": "choose_confluence_space",
           "label": "Confluence-Bereich importieren", "sideEffect": "kb_write", "requiresRole": "edit"}
        ]
      }
    }]
  }
}
```

Shapes:

```
Interrupt = {
  id: string,              // "suggest-<uuid>"; echo it in the resume
  reason: "no_files" | "no_evidence",
  message: string,         // German; show it as the assistant's text for this turn
  expiresAt: string,       // RFC 3339 UTC; 24 h after the pause
  metadata: { reason: string, query: string, actions: Action[] }
}
Action = {
  id: "web" | "library" | "upload" | "confluence",   // send back as actionId
  tool: string,            // informational; the server resolves the action by id, never by tool
  frontendTool?: "pick_library_file" | "upload_file" | "choose_confluence_space",
                           // present = the client must run this UI step before resuming
  label: string,           // German button label
  sideEffect: "external_read" | "kb_write",
  requiresRole: "view" | "edit",
  args?: object            // server-set (web: {query}); never needs to be sent back
}
```

Messages: `no_files`: "Diese Wissensbasis enthält noch keine Dateien. Was möchtest du tun?"; `no_evidence`: "Dazu habe ich in der Wissensbasis nichts gefunden. Was möchtest du tun?".

Which actions are offered (display them in this order):

| Action | `no_evidence` | `no_files` | Who sees it |
|---|---|---|---|
| `web` | yes | never (an empty KB is a setup problem, not a missing fact) | `view` and up, only if the deployment has the built-in `web_search` tool **and** web search is switched on and configured (`web_search_enabled` plus both Google search keys) |
| `library`, `upload` | yes | yes | KB role `edit` and up (they write to the KB) |
| `confluence` | yes | yes | KB role `edit` and up, only if Confluence is switched on (`confluence_enabled`) **and** the user has a Confluence connection |

The server checks availability when it pauses and again when the resume arrives. If an action stopped being available in between (web search switched off, connection deleted), choosing it answers "Diese Aktion kann ich hier leider nicht ausführen." like any action that was not offered.

When nothing is left to offer, there is **no pause**: the turn completes with the plain dead-end text. Recorded for a `view` member on an empty KB:

```
data: {"type":"RUN_STARTED","threadId":"91d9c823-8d13-4d4d-96a1-7a940df8875e","runId":"6e99d9bd-…"}
data: {"type":"STEP_STARTED","stepName":"agentchat/retrieve"}
data: {"type":"STEP_FINISHED","stepName":"agentchat/retrieve"}
data: {"type":"STEP_STARTED","stepName":"agentchat/suggest"}
data: {"type":"STEP_FINISHED","stepName":"agentchat/suggest"}
data: {"type":"STEP_STARTED","stepName":"agentchat/dead_end"}
data: {"type":"STEP_FINISHED","stepName":"agentchat/dead_end"}
data: {"type":"TEXT_MESSAGE_START","messageId":"13dbba3b-…","role":"assistant"}
data: {"type":"TEXT_MESSAGE_CONTENT","messageId":"13dbba3b-…","delta":"Diese Wissensbasis enthält noch keine Dateien."}
data: {"type":"TEXT_MESSAGE_END","messageId":"13dbba3b-…"}
data: {"type":"CUSTOM","name":"justrag.message.v1","value":{"aiMessageId":"5b13d048-3ca5-44d2-809c-06c9333ee23b","chatId":"91d9c823-8d13-4d4d-96a1-7a940df8875e"}}
data: {"type":"RUN_FINISHED","threadId":"91d9c823-…","runId":"6e99d9bd-…","outcome":{"type":"success"}}
```

So a `view` user gets: on `no_files` this plain text, on `no_evidence` only `web` (or plain "Dazu habe ich in der Wissensbasis nichts gefunden." when web search is not available).

### 3. Resume

A resume is a new POST on the same thread with `resume` and no new message:

```json
{
  "threadId": "a9670fd6-3162-41e6-b1c6-0438cc913c15",
  "messages": [],
  "resume": [{
    "interruptId": "suggest-44544675-864c-4e50-a90d-76e7152116d3",
    "status": "resolved",
    "payload": {"actionId": "upload", "args": {"userFileIds": ["e858f665-8145-442a-868a-f17a0495b1a7"]}}
  }]
}
```

- `status: "resolved"` + `payload: {actionId, args?}` runs the chosen action. **The click is the approval**: there is no second confirmation step.
- `status: "cancelled"` (or a payload `{"cancelled": true}`, or a payload without `actionId`) cancels: the turn ends with "Okay, ich habe nichts unternommen."
- Only these `args` keys are read; everything else is dropped: `library_add_to_kb`: `userFileIds` (array of library file ids); `confluence_import`: `spaceKey`, `rootPageId`; `web`: none.
- The server re-derives the offered actions from its own state and re-checks the caller's role at execution. An `actionId` that was not offered answers "Diese Aktion kann ich hier leider nicht ausführen." as a normal completed turn.
- A resume stores no user message and is not counted in the usage ledger again (the turn was counted when the question came in). The outcome text is stored as the turn's AI message.

Per action, what the client does first and what it sends:

| `actionId` | Client step before resuming | `payload` |
|---|---|---|
| `web` | none | `{"actionId":"web"}` |
| `library` | `pick_library_file`: let the user pick files from `GET /api/library/files` | `{"actionId":"library","args":{"userFileIds":["<id>", …]}}` |
| `upload` | `upload_file`: upload the file into the library with `POST /api/library/files` (see `docs/api-contracts/user-file-library.md`; a `200` deduplicated answer is fine), take the returned `id` | `{"actionId":"upload","args":{"userFileIds":["<id>"]}}` |
| `confluence` | `choose_confluence_space`: let the user pick a space (the existing Confluence UI/API of the KB settings) | `{"actionId":"confluence","args":{"spaceKey":"HRZ"}}` (`rootPageId` optional) |

Outcome texts (one `TEXT_MESSAGE` in the `act` step; the turn then completes):

| Action | Outcome | Text |
|---|---|---|
| `library` / `upload` | added | "Datei hinzugefügt – sie wird gerade verarbeitet. Frag gleich noch einmal." |
| | some added, some not | "N von M Dateien hinzugefügt – sie werden gerade verarbeitet. Die übrigen konnten nicht hinzugefügt werden. Frag gleich noch einmal." |
| | already in the KB | "Die Datei ist bereits in dieser Wissensbasis." |
| | none added: some already in the KB, the others failed | "Keine Datei hinzugefügt: N von M Dateien sind bereits in dieser Wissensbasis, die übrigen konnten nicht hinzugefügt werden." |
| | not added (not in the caller's library, KB full) | "Die Datei konnte nicht hinzugefügt werden." |
| | empty `userFileIds` | "Keine Datei ausgewählt – es wurde nichts hinzugefügt." |
| `confluence` | import started | "Import gestartet – die Confluence-Seiten werden im Hintergrund importiert. Frag gleich noch einmal." |
| | source created, first sync not queued | "Der Confluence-Import wurde angelegt, aber noch nicht gestartet. Bitte starte die Synchronisierung in den Einstellungen der Wissensbasis." |
| | user has no Confluence connection | "Keine Confluence-Verbindung — bitte zuerst in den Einstellungen verbinden" |
| | empty `spaceKey` | "Kein Confluence-Bereich ausgewählt – es wurde nichts importiert." |
| any | failure | "Das hat leider nicht geklappt. Bitte versuche es später noch einmal." |

The added files are ingested in the background. The question is **not** re-run automatically: the user asks again once the file is processed (poll `GET /api/kb/{id}/files` for `status` if you want to show progress).

Recorded resume with `upload`:

```
data: {"type":"RUN_STARTED","threadId":"a9670fd6-3162-41e6-b1c6-0438cc913c15","runId":"a4664f42-30d5-482c-878d-8bc25fc1c436"}
data: {"type":"STEP_STARTED","stepName":"act"}
data: {"type":"STEP_FINISHED","stepName":"act"}
data: {"type":"TEXT_MESSAGE_START","messageId":"9cdd6ab8-…","role":"assistant"}
data: {"type":"TEXT_MESSAGE_CONTENT","messageId":"9cdd6ab8-…","delta":"Datei hinzugefügt – sie wird gerade verarbeitet. Frag gleich noch einmal."}
data: {"type":"TEXT_MESSAGE_END","messageId":"9cdd6ab8-…"}
data: {"type":"CUSTOM","name":"justrag.message.v1","value":{"aiMessageId":"013c53d5-5648-4e1d-9454-7cb6772ca239","chatId":"a9670fd6-3162-41e6-b1c6-0438cc913c15"}}
data: {"type":"RUN_FINISHED","threadId":"a9670fd6-…","runId":"a4664f42-…","outcome":{"type":"success"}}
```

`library` and `confluence` look the same with their texts. `web` runs the search and then streams a model answer from the web results in a `web_answer` step (`STEP_STARTED act`, `STEP_FINISHED act`, `STEP_STARTED web_answer`, `TEXT_MESSAGE_*` deltas, `STEP_FINISHED web_answer`, `justrag.message.v1`, `RUN_FINISHED`). The answer says that it comes from the web, names the URLs, and has **no** `justrag.sources.v1` (web results are not KB sources). The web search runs on the paused turn's query; on a follow-up that is the standalone (condensed) rewrite of the question, not its literal text (see `metadata.query`). The web answer sees only that query and the web results, never the earlier turns of the chat.

**Do not auto-load remote content from agent answers.** Web results are attacker-controllable text, and any model answer can echo a link or a Markdown image. Render images in agent answers as links (or not at all) instead of fetching them, and do not prefetch or unfurl links; open a link only on a user click.

Sending the same resume twice: the second answers `404 {"error":"no_open_interrupt"}` (recorded).

### Tool-approval interrupts (generic shape, not sent in this phase)

The AG-UI layer can also pause for a tool approval:

```json
{"id":"<function-call id>","reason":"tool_approval","message":"Approve calling web_search?",
 "toolCallId":"<the tool call awaiting approval>","expiresAt":"…","metadata":{"request":{…}}}
```

answered with `{"interruptId":"…","status":"resolved","payload":{"approved":true}}` (`status: "cancelled"` or `approved: false` rejects). **The agent chat never sends one in this phase**: its answer model gets only approval-free tools (`kb_search`, `memory_read`, `memory_write`); web search is reachable only through the dead-end `web` action. Clients may still handle the shape generically.

### 4. Cancel

Two kinds:

- **Cancel a dead end** (the user dismisses the suggestions): resume with `status: "cancelled"`. Recorded:

  ```
  data: {"type":"RUN_STARTED","threadId":"fe7772fa-93a1-480e-bef4-893454e452d5","runId":"1566e855-…"}
  data: {"type":"STEP_STARTED","stepName":"act"}
  data: {"type":"STEP_FINISHED","stepName":"act"}
  data: {"type":"TEXT_MESSAGE_START","messageId":"34fceef0-…","role":"assistant"}
  data: {"type":"TEXT_MESSAGE_CONTENT","messageId":"34fceef0-…","delta":"Okay, ich habe nichts unternommen."}
  data: {"type":"TEXT_MESSAGE_END","messageId":"34fceef0-…"}
  data: {"type":"CUSTOM","name":"justrag.message.v1","value":{"aiMessageId":"eaf28cfa-…","chatId":"fe7772fa-…"}}
  data: {"type":"RUN_FINISHED","threadId":"fe7772fa-…","runId":"1566e855-…","outcome":{"type":"success"}}
  ```

  Afterwards the thread accepts new messages again.

- **Time budget**: when the deployment or KB sets `chat_turn_budget_seconds` (> 0), a turn that runs longer is stopped server-side and ends with `RUN_ERROR` (`"run failed"`); nothing more than the user message is stored, as for any failed run.

- **Stop a running answer**: abort the HTTP request (close the `EventSource`/`fetch`). The server stops the model, records the run as cancelled and stores **no** AI message; the user message stays. No `RUN_FINISHED` or `RUN_ERROR` reaches the client (the connection is gone). Recorded: a turn aborted after 1.5 s left the user message and a `cancelled` run, no answer. The thread accepts new messages immediately.

### 5. Error

A run that fails after the stream started ends with:

```
data: {"type":"RUN_ERROR","message":"run failed"}
```

instead of `RUN_FINISHED`. Open text/reasoning messages are closed first. Nothing more than the user message is stored, so on reload the turn shows the question without an answer. Causes: the model provider failed, the dead-end pause could not be recorded, or a **completed answer could not be stored**: then the text already streamed, but `RUN_ERROR` follows instead of `justrag.message.v1` + `RUN_FINISHED`. Treat the streamed text as not saved.

A failing answer-time tool call is not a run error: its `TOOL_CALL_RESULT.content` is `{"error":"tool failed"}` (or a policy message), and the model continues.

The model's tool calls in one turn are capped by `chat_answer_tools_max_rounds` (default 5, counted per call). A call past the cap does not run: its `TOOL_CALL_RESULT.content` is `{"error":"Werkzeug-Budget für diese Antwort erschöpft: beantworte die Frage jetzt mit dem vorhandenen Kontext."}`, and the model then answers without tools.

## Custom events

### `justrag.sources.v1`

Once per answered turn, value = `ChatSource[]` (the turn's numbered sources; `[]` is possible). Same shape the legacy chat persists in `messages.sources`, and the same array the stored AI message carries on reload.

```
ChatSource = {
  index: number,        // the n in the answer's [n]
  fileName: string,
  fileId: string,       // the KB file id
  content: string,      // chunk text
  score: number,
  pages: number[] | null,   // null when the file has no page info
  nodeKind?: "leaf" | "summary",
  treeLevel?: number,       // RAPTOR summary level, omitted for leaves
  createdAt?: string,       // RFC 3339; see note
  publishedAt?: string      // RFC 3339; see note
}
```

Note: `createdAt`/`publishedAt` are part of the shape but are **not filled on this endpoint in this phase** (the agent chat runs no source-date lookup), so they are always omitted. Do not depend on them here.

### `justrag.tool_chunks.v1`

After each answer-time tool result that carried chunks (today: a `kb_search` by the model), value `{toolCallId, chunks}`:

```json
{"toolCallId":"call_…","chunks":[{"id":"<chunk id>","file_id":"<file id>","file_name":"urlaub.md","content":"…","score":0.82}]}
```

The chunk keys are **snake_case** (the MCP tool-result shape). These are raw hits for showing tool activity; cite from `justrag.sources.v1`, not from here. The floor retrieval in the `retrieve` step emits no tool events.

### `justrag.message.v1`

Once per completed turn whose answer was stored, right before `RUN_FINISHED`: `{"aiMessageId":"<uuid>","chatId":"<uuid>"}`. `aiMessageId` is the stored AI message (use it for feedback via the existing `POST /api/kb/{id}/chats/{chatId}/messages/{messageId}/feedback`). Not sent for a dead-end pause (nothing stored), a cancelled or failed run.

## Open interrupts after a reload

An open interrupt cannot be fetched again: there is no endpoint that lists a thread's open interrupts, and `GET /api/chats/{id}/messages` shows only the question. A thread whose pause is lost to the client (reload, lost `RUN_FINISHED`) answers `409 thread_has_open_interrupt` to new messages until the interrupt expires (24 h). Until that escape exists, keep the last `RUN_FINISHED` interrupt of a thread client-side (for example in `sessionStorage`, keyed by chat id) so the suggestions can be shown again or cancelled, and offer "start a new chat" on `409 thread_has_open_interrupt`.

## Interrupt expiry

24 h after the pause (`expiresAt`). The first resume after that answers `409 interrupt_expired` and closes the pause; any later resume answers `404 no_open_interrupt` (also when the server's 15-minute sweep closed the pause first). The thread then accepts new messages (recorded: an expired pause, resume 409, retry 404, new message runs normally).

## Current limitations (phase 2)

- Separate endpoint; the legacy `POST /api/kb/{id}/chat` is unchanged and remains the default chat.
- No post-response tasks: no follow-up questions, no citation/factuality verification, no confidence or conflict events, no trajectory/agent-decision rows.
- One-click approval: choosing a suggestion executes it. There is no separate confirmation dialog server-side; put any "are you sure" in the UI.
- No web search during an answer, only via the dead-end `web` action (no tool-approval interrupts).
- Retrieval quality has not been evaluated against the legacy chat yet (eval gate deferred), and the sources carry no file dates.
- Retrieval: every search runs the production pipeline (`PrepareChatContext`) with the KB's configured system prompt, the current-date line (when `chat_date_awareness_enabled` is on) and the source-date lookup, and a follow-up's first search uses the standalone rewrite of the question over the chat's stored history (as the legacy chat does). **Not applied on this path yet:** knowledge-graph routing, the tabular (spreadsheet SQL) router, the recency lister ("Welche neuen Meldungen gibt es?"), query classification (so no HyDE / multi-query, no per-query-type tuning) and the session-memory / long-term-memory / tabular-guidance blocks the legacy chat adds to the system prompt. Answers on KBs that rely on those can differ from the legacy chat.
- Deleting the chat (`DELETE /api/chats/{id}`), leaving the KB, or deleting the KB also deletes the agent's stored conversation state for those chats.
- No listing of open interrupts (see above).
- Usage: each new question counts once in the usage ledger (`surface: web`); resumes do not count.
