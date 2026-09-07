# Agent orchestration

The *why* behind every chat-pipeline orchestrator and feature. The flag list, recipes, and migration table live in [`../CLAUDE.md`](../CLAUDE.md); this doc is the depth reference for mechanism and design decisions. Retrieval-pipeline subsystems (BM25, vector, reranker, MMR, CRAG, citation validator, …) live in [`retrieval.md`](retrieval.md) — open this doc instead when the question is "how does the chat layer route a turn through retrieval and back out as an answer?".

When CLAUDE.md says "see `docs/agent-orchestration.md` for the full rationale", that's a pointer to a section in this file.

## Read order

1. **Trajectory streaming** — how SSE events surface every decision; required mental model for everything else.
2. **The orchestrators** — Long-context, Supervisor, Plan-and-Execute, Agentic, Standard fallback. Priority order and dispatch.
3. **Feature index** — each chat-pipeline feature, why it exists, how it composes with the orchestrators.

## Trajectory streaming

Every orchestrator emits structured `agent_*` SSE events alongside the answer stream: `hop`, `iterate`, `plan`, `decision`, `agent_dispatch`, `agent_tool_call`, `answer`. The frontend renders these as a live decision log; the admin metrics panel persists them into `agent_decisions` (migration 0042) for offline analysis.

**Why:** debuggability is the dominant cost in agentic systems. Without per-decision tracing, a wrong final answer is unbisectable — you can't tell if the planner chose bad sub-questions, the search arm missed the chunks, or the answer LLM hallucinated past available evidence. Trajectory events make every orchestrator reproducible from the SSE log alone.

Implementation: `internal/chat/trajectory.go` (event encoder), `internal/chat/agentic_chat.go`, `internal/chat/plan_execute_chat.go`, `internal/agents/supervisor.go` (emit sites). The events stream alongside content tokens on the same SSE channel.

## The orchestrators

Streaming chat for `complex_reasoning` queries dispatches through the first orchestrator whose flag is on. The ladder itself lives in `internal/chat/orchestrator_select.go` (`SelectOrchestrator`); `internal/chat/http_send.go` (`Handler.tryDeepChat`) only resolves its inputs and runs the winner. Full precedence, highest first:

| # | Orchestrator | Flag(s) | Shape |
|---|---|---|---|
| 0 | Comparison / Team / Corpus-table | explicit user intent | attachment comparison, an explicitly selected agent/team, corpus-comparison table |
| 1 | DRIFT | `chat_drift_enabled` + global-synthesis | primer → follow-ups → light searches → synthesise |
| 2 | Long-context | `chat_longcontext_enabled` + global-synthesis | wide retrieval (≈200 chunks) → the `chat_longcontext_mode` consumer (`map_reduce` by default since Wave 5, or `flat`) |
| 3 | Supervisor | `chat_supervisor_enabled` | classification → specialist → search → answer |
| 4 | Plan-and-Execute | `chat_plan_execute_enabled` (+ `_dag`, …) | plan → iterate → generate |
| 5 | Agentic | `chat_agentic_enabled` | hop-1 → critique → optional follow-up hops |
| 6 | Standard | (always-on fallback) | the legacy 2-step research path |

**The eval harness mirrors this ladder** so `cmd/eval --production-context --orchestrator-dispatch=true` measures what production would do. `internal/eval.SelectOrchestrator` gained the **DRIFT arm in Wave 4** (W4-R3) at the same position production uses — directly above long-context — and `OrchestratorDispatchAdapter.Search` dispatches it through the real `chat.RunDriftChat`, resolving `chat_drift_model` / `_max_followups` / `_primer_top_k` / `_search_top_k` per question so a per-KB overlay is honoured; the report records `agent.orchestrator = "drift"` with `dispatch_reason = "complex_reasoning_drift_gate"`. Until then a DRIFT-enabled deployment silently evaluated its global-synthesis questions through long-context instead. Comparison / Team / Corpus-table remain deliberately un-mirrored — they are explicit-intent routes, not complex-lane dispatch — and `--trajectory --orchestrator …` is a separate, unaffected mechanism. Any new orchestrator must set `FinalChunks` on the returned `*ChatContext` or its eval recall reads zero.

### Per-query orchestrator policy (`chat_orchestrator_policy`)

**What it is not:** a new rung on the ladder above. It is a hook evaluated INSIDE `SelectOrchestratorWithPolicy` (`internal/chat/orchestrator_select.go`), after the three always-first arms (Comparison / Team / Corpus-table — a rule can never hijack those, W6-R16) and before the flag ladder itself. The ladder table above is unchanged, and is pinned unchanged by a test that freezes a byte-for-byte copy of the pre-Wave-6 ladder body and compares both the exported `SelectOrchestrator` and the policy-aware `SelectOrchestratorWithPolicy` against it with an empty policy (W6-R10).

**Why a hook and not a rung:** the ladder only ever dispatches `complex_reasoning` turns (`in.complexAndUnenhanced()`). An operator wanting "route every `lookup` about KB X through the supervisor" cannot express that as a flag — flags are deployment-wide and the ladder's precondition excludes non-complex turns outright. The policy table is keyed on the same signals a rule can name (`query_type`, `global_synthesis`, `enumeration`, `recency_listing`, `has_file_selection`, `history_turns_gte`, `kb_ids`), evaluated by the leaf package `internal/chatpolicy` (stdlib-only, imported by both `chat` and `eval` so the two share one definition of what a rule means).

**Shape of a rule:** `{"when": {...}, "orchestrator": "drift"|"longcontext"|"supervisor"|"plan_execute"|"plan_execute_dag"|"agentic"|"standard", "mode": "force"|"prefer"}`. The document is a JSON array (`chat_orchestrator_policy`, GLOBAL-ONLY, default `[]`), rules evaluated in order, **first match wins**. Every `when` field is optional and absent means "not tested" — an empty `when: {}` matches every turn. `query_type` is one of the classifier's own labels (`lookup`, `enumeration`, `complex_reasoning`); `global_synthesis` is the additional signal `IsGlobalSynthesisQuery` produces on a `complex_reasoning` turn.

- **`force`** selects the named orchestrator regardless of its feature flag — the operator overriding the ladder on purpose. If that orchestrator's dependencies are missing (e.g. it needs KG community summaries that were never built), the attempt fails and falls back through the **existing** orchestrator-error → `PrepareChatContext` path; the fall-through is made visible with a `chat.orchestrator_policy.fallthrough` warn log and a second `orchestrator_policy` trajectory event whose `Decision` is `"fallthrough"` — an operator who forces a route with missing dependencies must see why they got an ordinary answer, not silently get one.
- **`prefer`** selects the orchestrator only when its feature flag is already on; otherwise the turn falls through to the ordinary flag ladder. This is still visible, not silent: a matched-but-unapplied `prefer` rule emits the same `orchestrator_policy` trajectory event as a failed `force` (`Decision: "fallthrough"`, `Reason: "prefer rule matched but <orchestrator> is disabled"`) — the only thing it does NOT do is set `agent_decisions.policy_rule`, since the ladder, not the rule, actually answered the turn.
- **`standard`** names the ladder's own no-orchestrator outcome: legacy deep chat (`RunDeepChat`) on a turn the complexity classifier already marked complex, or `PrepareChatContext` otherwise. A `force standard` rule on a non-complex turn does **not** push a turn that would have gone through `tryDeepChat` onto `PrepareChatContext` — the corpus-table arm is resolvable only inside the deep-chat dispatch (it needs an optional LLM confirmation call), so pulling a forced-`standard` complex turn out of `tryDeepChat` would silently skip that arm, which W6-R16 forbids. Concretely: the entry gate `shouldTryDeepChat` (`internal/chat/http_send.go`) widens to admit a turn for any *non*-`standard` forced/applied rule, but never for `standard` itself — a `force standard` rule on a lookup turn is recorded on the standard path (`chatResponseParams.policyRule` via `standardPathPolicyRule`), and on a complex turn it is recorded where the deep-chat dispatch's own `recordAgentDecision` already runs.
- **`plan_execute_dag`** has no `chat.Orchestrator` value of its own — it resolves to `OrchPlanExecute` with the DAG forced on for that turn (`PolicyDecision.ForceDAG`), OR'd with the existing `chat_plan_execute_dag` flag so a deployment that already runs DAG mode keeps it regardless of the policy.

**Once per turn, not once per candidate orchestrator:** `resolveTurnPolicy` (`internal/chat/http_send.go`) reads `chat_orchestrator_policy` exactly once in `SendMessage`, above the complexity gate. For an empty/unparseable policy (the default, and the fail-soft outcome of a broken stored value) it returns immediately without building the signal bag, so a deployment without a policy pays one `site_config` read and neither of the two regex classifiers (`IsEnumerationQuery`, `IsRecencyListingQuery`) it would otherwise need to resolve `Signals`. `tryDeepChat` re-runs `SelectOrchestratorWithPolicy` with the same policy and signals as the authoritative decision — `resolveTurnPolicy`'s own `gate` field only answers "may this turn reach the deep-chat dispatch at all", using `policyEnabledFromConfig`, the reader-sourced twin of `OrchestratorInputs.policyEnabled()` (the two are pinned equal by a test).

**Visibility.** An **applied** rule (force always; prefer only when its flag is on) is recorded three ways: the trajectory event `orchestrator_policy` with `PolicyRule` (a pointer, so rule 0 survives `omitempty`), the wire field `agent.policy_rule` on the eval report, and `agent_decisions.policy_rule` (migration **0073**, nullable smallint, no backfill — NULL means the flag ladder decided: no rule matched, a `prefer` rule's flag was off, or the policy is empty). A **matched-but-not-applied** `prefer` rule (its orchestrator's flag is off) is visible only in the trajectory event, never in the `agent_decisions` row — the row would otherwise claim a route that never ran.

**Admin UI.** A JSON textarea (`web/src/components/admin/policyPreview.ts` mirrors the Go validator in TypeScript, stdlib-only) shows inline validation errors as you type and, once the document parses, a **policy preview** table: which rule (if any) each of the four canonical turns — `lookup`, `enumeration`, `complex_reasoning`, `complex_reasoning + global_synthesis` — would hit, with the resulting orchestrator and mode. The tab's Save button is disabled while either JSON editor (this one, or the answer-tools-by-route one below) has an error.

**Validation.** `chat_orchestrator_policy` and `chat_answer_tools_by_route` are the first two GLOBAL-ONLY keys validated at save time by a dedicated hook, `siteconfig.ValidateGlobalValues` (`internal/siteconfig/global_validate.go`), run in `Handler.UpdateSiteConfig` right after the existing `ValidateConflicts` check — unknown orchestrators/modes/`when` fields, an unknown route or tool name, or more than 32 rules are a save-time 400, never a silently ignored policy. Both keys have **no `kbConfigRegistry` row** (like `kb_stale_days`) — they are global, not per-KB. The readers (`chat.ChatOrchestratorPolicy`, `chat.ChatAnswerToolsByRoute`) stay fail-soft on top of that: an unparseable stored value (a pre-validation row, or a bug) logs a warning and falls back to empty rather than 500ing a chat turn.

**Measuring it.** `cmd/eval --policy '<json>'` overlays `chat_orchestrator_policy` for one run, validated with the same `chatpolicy.ValidateOrchestratorPolicyJSON` the save path uses (an invalid document is a usage error, exit 2 — never a silently ignored flag); `--chat-overlay chat_orchestrator_policy=…` is rejected in favour of it. The eval mirror `eval.SelectOrchestrator` evaluates the same document through `chatpolicy.Decide`, ahead of its own `queryType != complex_reasoning` early return (so a `force lookup → supervisor` rule is measurable) — documented in-code as a deliberate difference from `--trajectory`, which carries no retrieval metrics and cannot measure cost the same way. The `query_type × orchestrator → recall / MRR / cost` table, the noise band, and the recommended (documentation-only — the shipped default stays `[]`) policy for the PPM fixture live in `eval/golden/orchestrator-policy.acceptance.md` (W6-R7 / W6-R7a).

### 0. Long-context (`chat_longcontext_enabled` + `chat_longcontext_mode`)

**Shape:** one wide retrieval pass (`LongContextMode`, top-k `chat_longcontext_top_k` ≈ 200, no MMR / score-drop / parent-child swap) → the consumer selected by `chat_longcontext_mode` (**`map_reduce` by default since Wave 5**, W5-R1; `flat` remains the fallback for an unrecognised value). See the [Long-context routing](#long-context-system-2-routing-t2-1-chat_longcontext_enabled--_max_tokens--chat_longcontext_mode) section below for the mechanism, both consumer modes and the cost argument.

**Why it is an orchestrator (Wave 3, W3-R5):** the route used to be a branch INSIDE `PrepareChatContext`, which the streaming chat never reaches for `complex_reasoning` turns — every one of those goes through `tryDeepChat`. So the route was configured, documented, metered and unreachable for exactly the query class it targets. It now sits directly below DRIFT (which is the more specific global-synthesis answer when KG community summaries exist) and above the supervisor; the `PrepareChatContext` branch remains for the non-streaming surfaces and drives the same consumer.

### 1. Supervisor (`chat_supervisor_enabled`)

**Shape:** one LLM classification call → dispatch to a specialist agent (`RetrieverAgent` or `EnumeratorAgent`) → that agent runs its own single search → answer streams from the retrieved context.

**Why:** the enumeration pre-pass and standard retrieval pipeline want different prompts, different top-k, and different post-processing. A single classifier up front routes each turn to the specialist whose prompt template and tool kit match the query intent. Cheaper than Plan-Execute (one classification call vs N sub-question expansions) but more deliberate than the standard fallback because the specialist agent owns its full prompt.

Implementation: `internal/agents/supervisor.go` (the `Supervisor.Run` method delegates to one of the agents in `internal/agents/{retriever,enumerator}.go`).

### 2. Plan-and-Execute (`chat_plan_execute_enabled` + `_dag`, `_dag_iterative`, `_tool_aware`)

**Shape:** plan → iterate → generate. A planner LLM decomposes the query into sub-questions or a DAG of steps; an executor walks the plan (parallel where the DAG allows); the final answer LLM synthesizes from accumulated evidence.

**Why:** complex multi-hop queries benefit from explicit decomposition — a single retrieval pass for "how does feature X interact with feature Y under condition Z" loses to one search per (X, Y, Z) axis. The DAG shape lets independent sub-questions run in parallel.

- `_dag` upgrades flat decomposition to a directed graph (later steps can depend on earlier results).
- `_dag_iterative` adds an inter-level critic LLM that re-plans after each level using accumulated findings.
- `_tool_aware` shows the planner the tool catalog so it can include `graph_search`, `keyword_search`, `calculator`, etc. in the plan.

Implementation: `internal/chat/plan_execute_chat.go` (`RunPlanExecuteChat`), `internal/chat/dag_executor.go`, `internal/chat/dag_critic_adapter.go`.

### 3. Agentic (`chat_agentic_enabled` + `_plateau_stop`, `_max_hops`)

**Shape:** initial retrieval → critique LLM scores sufficiency → if insufficient, generate a follow-up sub-query and run another hop. Stops at `_max_hops`, when the critic declares "sufficient", or when consecutive hops add no new chunks (plateau stop).

**Why:** the planner-up-front cost of Plan-Execute is wasteful when most queries resolve in one search. Agentic gives the average query the cheap path while preserving multi-hop capability for queries that genuinely need it. The plateau-stop guard prevents pathological loops on tangential queries that the critic keeps marking "insufficient" without adding signal.

Implementation: `internal/chat/agentic_chat.go` (`RunAgenticChat` at line 63; testable variant at line 76).

### 4. Standard `PrepareChatContext` (always-on fallback)

**Shape:** the legacy 2-step path — CRAG grading → optional rewrite → search → optional enumeration pre-pass → contextual-prefix prompt assembly → sandwich-ordered chunks → answer LLM. No multi-hop, no planner.

**Why:** the cheap default. Most queries (lookups, single-fact retrieval) don't need an orchestrator's overhead; the standard path is the calibrated baseline that all retrieval improvements have been tuned against (see [`retrieval.md`](retrieval.md)).

Implementation: `internal/chat/service.go` and `internal/chat/deep_chat.go`.

---

## Feature index

Each subsection: the WHY behind the feature, how it composes with the orchestrators, and the implementation entry point.

### Admin agent-metrics panel

Persists every orchestrator decision into `agent_decisions` (migration 0042) and surfaces aggregates in the admin UI. **Why:** without offline aggregation, you can't measure whether enabling Plan-Execute on a deployment actually changed answer quality vs. just spending more LLM tokens. The panel groups by orchestrator and shows hop counts, decision outcomes, and tool-call distributions.

`agent_decisions.policy_rule` (migration **0073**, nullable smallint) is the newest column: the 0-based [`chat_orchestrator_policy`](#per-query-orchestrator-policy-chat_orchestrator_policy) rule index that pinned this turn's orchestrator, or NULL when the flag ladder decided instead. It is set only when the policy **applied** — a matched-but-unapplied `prefer` rule, or a `force` rule that fell through because its orchestrator's dependencies were missing, both leave the row NULL (the row must never claim a route that did not actually answer the turn); those two cases are visible in the trajectory stream instead, not in this column.

### MCP tool registry (`chat_use_mcp_tools`)

The shared registry that backs every tool-calling code path (planner-time, answer-time, agent specialists). Built-in tools live in `internal/mcp/builtin/`; admin-registered MCP servers add to the same registry. **Why:** a single registry means orchestrators don't each carry their own tool wiring — adding a new tool surfaces it in every code path that opted into tools.

### Session memory (`chat_session_memory_enabled`)

Per-chat scratch memory the LLM can write to mid-turn and read back next turn. **Why:** lets the model accumulate intermediate calculations or partial findings across turns without forcing them into the visible answer text. Distinct from long-term memory (per-user, cross-chat).

### Answer-time conversation history (`chat_answer_history_enabled` + `_messages`, `_max_chars`; default ON)

Inserts the recent conversation turns (default last 6 messages, each capped at 4000 chars) between the system prompt and the current user message on EVERY answer path — standard stream/JSON, all orchestrators, answer-tools (`ai.BuildAnswerMessages`; loaded once per send in `internal/chat/answer_history.go`). **Why:** until 2026-06 the answer LLM was single-turn — only the `CondenseFollowUp` search-query rewrite ever saw prior messages — so follow-ups referencing the previous answer ("kannst du das als Tabelle erstellen?") had nothing to refer to and the model truthfully claimed it had no previous conversation. The multi-turn illusion rested entirely on query condensation for retrieval, which works for new content questions but not for answer-referencing ones. Default ON because it is a correctness fix; the flag is a kill switch. Token cost is bounded by the per-message cap (≈ a few thousand tokens worst case).

### Transform follow-up route (`chat_transform_followup_enabled`; default ON)

Detects follow-ups that ask to *transform the previous answer* (reformat as table, summarize, shorten, translate, bullet-point — `IsTransformFollowUpQuery`, conservative DE/EN keyword gate mirroring the corpus-table classifier) and answers them WITHOUT retrieval: the system prompt embeds the previous AI answer verbatim (capped at 24k runes) with a strict "transform only this content" instruction (`prompts.TransformFollowUpSystem`), and the previous answer's sources are carried over. Checked before `CondenseFollowUp`, so a positive also saves the condense LLM call and takes priority over every orchestrator including corpus-table. **Why:** re-retrieving on such turns is wrong twice over — a fresh corpus search redefines the scope (e.g. tables ALL events in the KB instead of the filtered list just shown), and the condensed query ("Tabelle aller …") can even hijack the corpus-table map-reduce. Misroutes degrade gracefully: the prompt instructs the model to say so briefly when the request cannot be fulfilled from the previous answer. Emits a `transform_followup` trajectory decision + `agent_decisions` mode; dispatch log line `rag.transform_followup.dispatch`. Implementation: `internal/chat/transform_followup.go`, route wiring in `internal/chat/http_send.go`.

### Factuality verifier (`chat_factuality_verifier_enabled` + `_always_run`, `_model`)

A post-answer LLM that scans the generated answer for claims and grades each against the retrieved context: supported / unsupported / contradicted. **Why:** the answer LLM, even with citations, sometimes paraphrases past what the chunks actually say. The verifier is the line-by-line audit that catches "supported-by-vibe-but-not-by-text" claims.

Implementation: `internal/chat/post_response.go` (gates the verifier call), `internal/ai/` (verifier prompt).

### Factuality refine gate (`chat_factuality_gate_enabled` + `chat_refine_model`)

When the verifier flags ≥1 unsupported/contradicted claim, the gate triggers a second answer-generation pass with the flagged claims and a "remove or qualify" instruction. **Why:** detection without remediation is just an audit log. The gate closes the loop — every flagged answer either gets rewritten or carries an "unverified" annotation in the SSE diff.

### Refine SSE diff

When the gate fires, the SSE channel streams a diff between the original and refined answer instead of replacing the message wholesale. **Why:** users see *what changed and why* (which claim was unsupported), not just a silently swapped answer. Trust-building for the verifier itself.

Implementation: `internal/chat/refine_diff.go` (`computeRefineDiff` at line 40).

### Sufficient-context abstention gate (`chat_sufficient_context_enabled` + `_model`)

One fast-tier call between context assembly and generation asking whether the assembled chunk set as a WHOLE suffices to answer; on "insufficient" the existing abstain plumbing fires (notice in the system prompt, `ChatContext.Abstain`). **Why:** CRAG grades chunks independently — a set of individually "relevant" chunks can still be jointly insufficient, which is exactly the regime where models hallucinate instead of abstaining (Google ICLR 2025 "sufficient context": a small answer-vs-abstain intervention recovers 2–10 % correct-answer fraction, measured on Gemma-class models among others). Wired in the standard path AND the supervisor orchestrator (the production hot path; flags arrive pre-resolved via `SupervisorChatParams`). Fail-open. Recipe in `docs/feature-recipes.md`.

### Structured Outputs on the fast tier (always-on, no flag)

Every fast-tier JSON call (CRAG grader, KB router, longmem extractor + conflict classifier, query decomposer, flat + DAG planner, DAG critic, factuality / Self-RAG verifier, evidentiality scorer, KG extractor, iterate-action, agentic critique, HyPE/golden-set question generator, sufficient-context gate) sends a strict `json_schema` response_format with enums where the prompt fixes a vocabulary, auto-downgrading to `json_object` when the backend rejects it; tolerant parsing remains the last line of defense. **Why:** on Gemma-class models most of the tool-calling/classification deficit is malformed output, not wrong intent — server-side grammar enforcement removes that failure mode for one-time plumbing cost. Also makes the calls deterministic (temperature 0; previously 0.2 on several sites). Sole exception: the tool-aware DAG planner, whose free-form per-tool `args` object is incompatible with strict mode's mandatory `additionalProperties: false` (pinned by `TestPlanQueriesDAGToolAware_StaysUnstructured`). `ai.GenerateCompletionStructured`, `internal/ai/structured_contract_test.go`.

### Spotlighting rail (always-on, no flag)

`ChatSystemPrompt` rule 15 (EN + DE) marks retrieved context blocks as quoted reference data, never instructions — the standard architectural defense against indirect prompt injection via poisoned documents / RSS / crawled content (Microsoft Spotlighting). Matters most when `chat_answer_tools_enabled` is on: an injected chunk steering `sql_query`/`web_search` calls is the realistic attack. Regression-guarded by `TestChatSystemPrompt_SpotlightsContextAsData`.

### Turn budget (`chat_turn_budget_seconds` / `_tokens` / `_tool_calls`)

A per-turn ceiling on wall-clock seconds, total tokens, and tool calls. Checked at the top of every tool-loop iteration and every orchestrator hop. **Why:** unbounded agentic loops are the dominant production-cost risk. The budget guarantees a worst-case ceiling per turn regardless of how confused the model gets.

Implementation: `internal/chat/budget.go`.

### Sub-KB router (`chat_kb_router_enabled` + `chat_kb_router_min_confidence` + `?route=auto`)

When the user query arrives with `?route=auto`, an LLM classifier inspects `kb.description` for each KB the user has access to and picks the best-matching one before retrieval. **Why:** multi-KB users (e.g. a course KB + a personal-notes KB + a docs KB) shouldn't have to manually route every question. The classifier reads KB descriptions as labels and picks the most likely target; below `_min_confidence` it falls back to the user's selected KB.

Implementation: `internal/chat/kb_router.go`.

### Retrieval-tier tools (`keyword_search`, `chunk_read`, `document_outline`)

Registered alongside `kb_search` in the MCP registry. **Why:** the dense+sparse+rerank pipeline behind `kb_search` is the right default but sometimes wrong: `keyword_search` lets the model fall back to literal BM25 when it's hunting a specific identifier; `chunk_read` lets it fetch a known chunk_id verbatim for citation grounding; `document_outline` lets it scope reads to one file's structure before drilling in.

### Non-retrieval tools (`calculator`, `sql_query`, `code_exec`)

`code_exec` is gated behind `chat_code_exec_enabled` and requires gVisor; `calculator` and `sql_query` are always-available. **Why:** numeric or tabular queries should not be answered by an LLM doing mental arithmetic over retrieved text. The calculator is cheap insurance; `sql_query` lets the model run aggregations over structured KB metadata; `code_exec` is the escape hatch for queries needing arbitrary computation.

### Tool-aware DAG planner (`chat_plan_execute_tool_aware`)

When set, the Plan-Execute planner sees the MCP tool catalog and can include tool calls (not just sub-questions) in the plan. **Why:** for queries like "How many courses use feature X?" the planner can directly schedule a `sql_query` step instead of issuing a paragraph-style sub-question that gets retrieved-against. Falls back gracefully to the legacy DAG planner on LLM error.

Implementation: `internal/chat/dag_executor.go` (executor handles both retrieval-step and tool-step DAG nodes).

### Tool-mix telemetry

Every `MCPDispatcher.Dispatch` call records into `agent_decisions.tool_calls` JSONB (migration 0043). **Why:** without per-tool dispatch counts, you can't tell whether `code_exec` is being called productively or whether the model is spamming `calculator` for trivial arithmetic. The JSONB shape supports admin-panel pivots by tool name.

### Answer-time tool calling (`chat_answer_tools_enabled` + `chat_answer_tools_max_rounds`)

Orthogonal to the tool-aware planner. When on, the answer-generation LLM receives every registered MCP tool (except `code_exec`) via native OpenAI function calling and may call them mid-stream. **Why:** some tool needs only surface during answer drafting — the model starts to write a number and realizes it needs the calculator; it starts to cite a fact and realizes it needs `memory_recall`. The planner can't anticipate these.

**Orchestrator coverage:** every orchestrator routes its final answer stream through `RunAnswerWithTools` when the flag is on — the lookup/enumeration SSE path (`writeStreamingResponse`) and the deep-chat SSE path (`tryDeepChat`, which also serves agentic / plan-execute / supervisor after each builds its `ChatContext`). The plan-execute / supervisor *plan-time* tool-aware DAG planner (`chat_plan_execute_tool_aware`) is orthogonal: it picks tools up-front, while answer-tools fires at answer time. They compose — the planner can still gather context via tools, and the answer LLM can additionally call tools (typically `calculator`, `count_mentions`, or `memory_*`) mid-stream. With both flags on, a single turn can issue tool calls at both moments; watch `rag_answer_tool_loop_rounds`.

Each call emits an `agent_tool_call` trajectory event and persists into `agent_decisions.tool_calls` JSONB (migration 0043, automatic via the existing `MCPDispatcher.Dispatch` recorder path). Both `kb_id` and `chat_id` are injected into tool args by the loop (`injectInjectedIDs`); the turn budget (`chat_turn_budget_seconds` / `_tokens` / `_tool_calls`) is consulted at the top of every round and triggers an early forced-finish on exhaustion. The model that backs the KB's chat completion endpoint MUST support `tools` + `tool_calls` natively (verified on gemma-4-26b-A4B-it via vLLM/LiteLLM; if you change models, probe tool_calls round-trip before enabling the flag).

**Known limitation:** the answer-tools path does NOT strip `<think>...</think>` reasoning tags from streamed content; it relies on the provider emitting reasoning via the `reasoning_content` field (vLLM/LiteLLM does this). If you switch to a model that emits reasoning inline via think-tags, those tokens will leak into the visible answer text and may cause tool-call rounds to be misclassified as content rounds. The legacy `ai.StreamCompletion` path (flag off) handles think-tags correctly.

**Metrics:** `rag_answer_tool_loop_rounds` (histogram, buckets `0..10`) and `rag_answer_tool_loop_exhausted_total` (counter). Per-tool dispatch counts continue to feed `rag_mcp_tool_call_total` via the existing dispatcher.

Implementation: `internal/chat/answer_tools.go` (`RunAnswerWithTools`).

**Per-route tool sets (`chat_answer_tools_by_route`, Wave 6, GLOBAL-ONLY, default `{}`).** Orthogonal to `chat_answer_tools_enabled` — this key only narrows what an already-on tool loop offers, it does not turn the loop on. The document maps a route (`lookup`, `enumeration`, `complex_reasoning`, `global_synthesis` — the last a cross-cutting key that wins over the query type's own entry when the turn is a global-synthesis one) to the tool names the catalog is filtered to on that route; 14 **built-in** names only (`chatpolicy.KnownAnswerTools`, cross-checked against the MCP registry by a test) — a remote or per-KB MCP tool's name is deployment data, not something a save-time validator can check, so remote tools cannot be route-scoped in v1.

Both call sites that assemble the answer-time catalog (`tryDeepChat`'s streaming block and `writeStreamingResponse`) consult `ChatAnswerToolsByRoute(ctx, reader).Allowlist(queryType, isGlobalSynthesis)` right after building the full catalog. `Allowlist`'s second return distinguishes "the document says nothing about this route" (no restriction) from "the document names this route with an empty list" (a real restriction — no tools at all); a route restriction that empties the catalog **skips the tool loop entirely** rather than calling `RunAnswerWithTools` with zero tools.

**Enforced at both the catalog projection and the dispatch boundary**, mirroring `RestrictedDispatcher`'s existing catalog+dispatch pairing for per-agent allowlists: `routeRestrictedDispatcher` (`internal/chat/restricted_dispatcher.go`) wraps the `ToolDispatcher` interface (not the concrete `*MCPDispatcher`, so it composes over an already agent-restricted dispatcher too — wrapping one yields the intersection of both allowlists, most-restrictive-wins) and refuses a `Dispatch` call for a tool outside the allowlist even if that tool were never listed in the model's catalog. Hiding a tool from the catalog is a hint to the model, not a control — a prompt-injected model can still emit a call for a tool it never saw.

An `answer_tools_route` trajectory event is emitted whenever a restriction applies, naming which route key actually resolved it (`lookup` vs. `global_synthesis`, mirroring `Allowlist`'s own precedence so the two can never disagree) — or `"unknown"` when the turn itself has no classified query type.

**Unclassified turns (fix round 2).** A turn whose query type is empty — today, a reformat/transform follow-up, which skips retrieval and classification entirely and so was never given a route — is not left unrestricted just because it matches no configured key. `resolveAnswerToolsRoute` (`internal/chat/route_tools.go`) treats it as **fully restricted** (an empty allowlist) whenever `chat_answer_tools_by_route` configures at least one route at all, regardless of which routes: an operator who locks down any route intends "no free-form tool use except where I said so", and an unclassified turn must not be a classification-based escape hatch around that. An entirely empty document (no routes configured) is unaffected — this only ever narrows, and only when there is something configured to narrow against.

`rag.completion`'s `answer_tools_path` log field changed meaning alongside this: it now means **"the tool loop actually ran"**, not merely "tools were configured/enabled" — a route restriction, or the unclassified-turn case above, can leave `chat_answer_tools_enabled` true while this field reads false, because the catalog came back empty and the caller fell through to a plain streaming answer instead of calling `RunAnswerWithTools`. A dashboard reading it as a simple mirror of the enable flag will see it dip once any route restriction is configured.

**Not measured on dev** (W6-R8a): the dev fixture's answer tools are off and the eval harness never runs the answer-tools loop, so `agent_decisions.tool_calls` before/after cannot be compared in a repeatable eval run here — ship with the unit tests at both boundaries and this documented operator procedure instead:

```sql
SELECT mode, jsonb_array_length(tool_calls) AS tool_call_count
FROM agent_decisions
WHERE created_at > '<before enabling the restriction>'
GROUP BY mode;
```

run once before and once after flipping the key, per KB.

### Knowledge-graph extraction (`kg_extraction_enabled` + `kg_extraction_model`)

At ingest, every chunk feeds an LLM that emits `(entity, relation, entity)` triples into `kg_entities` + `kg_edges` (migration 0044). **Why:** retrieval can find a chunk that mentions an entity; the graph can find the chain of relations that connect two entities mentioned in different chunks. The two signals compose — neither is sufficient alone for multi-hop questions.

Implementation: `internal/processor/kg_extractor.go` (extractor) + `kg_store.go` (writer).

### Graph search tool

A `graph_search` MCP tool that lets the answer LLM (or the planner) query the KG directly. **Why:** "What entities is X connected to via relation R?" is a graph query, not a retrieval query — forcing it through BM25+vector is the wrong tool.

### Graph-routing heuristic (`chat_graph_routing_enabled`)

Diagnostic gate that emits a trajectory event whenever the query's entities match KG entities; intended to surface "would graph traversal help here?" decisions before flipping the heavier injection flag.

### Graph-routing chunk injection (`chat_graph_routing_inject_chunks` + `chat_graph_routing_max_chunks`)

When the heuristic fires, the subgraph's chunks fold into the RRF candidate pool via `extraLists` (the same path SubQueries / MultiQuery / StepBack already use). **Why:** the cheapest way to get graph signal into the answer is to widen the retrieved chunk pool with graph-relevant chunks; the existing RRF + reranker pipeline does the rest. Plan-Execute and Agentic only inject into the *initial* search to avoid contaminating focused follow-up hops.

Implementation: `internal/chat/graph_routing.go` (`ResolveGraphChunksIfEnabled`).

### Graph-routing traversal modes (T1-4 / T1-5: `chat_graph_routing_path_mode`)

When the heuristic fires AND chunk injection is enabled, the traversal mode picks how the matched entities are expanded into a chunk pool. Three modes:

- **`neighbors`** (default): legacy depth-1 BFS via `LookupSubgraph` against the top-1 matched entity. Cheapest, lowest variance.
- **`ppr`**: Personalized PageRank seeded from *every* matched entity; the top-K highest-scored neighbour entities (excluding seeds) feed the chunk projection. **Why:** wins on multi-hop queries where the answer entity is 2+ hops from any single seed — the random-walk distribution surfaces entities that aren't direct neighbours of any one seed but are reachable through the union of seed neighbourhoods. Implementation in `internal/kg/pagerank.go` (in-memory iteration over the edge list, undirected, weight-aware, L1-norm convergence). Tuning: `chat_graph_routing_ppr_damping` (default 0.85), `_ppr_max_iter` (20), `_ppr_top_entities` (10). Telemetry: `rag_kg_ppr_seconds`, `rag_kg_ppr_converged_iterations`.
- **`paths`**: PathRAG-style enumeration of relational paths between every pair of matched entities; chunks score by sum of containing-path scores (a chunk that sits on three short paths beats one on a single path). **Why:** when the question implies a relation between multiple known entities, the connecting paths *are* the answer signal — surfacing chunks along those paths gives the LLM the bridging evidence directly. Falls back to `neighbors` when only one entity matched. Implementation in `internal/kg/paths.go` (BFS over paths, not nodes — cycle-free per-path, multiple paths surface). Tuning: `chat_graph_routing_paths_max_len` (3), `_paths_max_paths` (5). Telemetry: `rag_kg_paths_seconds`, `rag_kg_paths_found`.

**Fail-open:** any error or empty result in `ppr` / `paths` falls back to `neighbors` so a misconfigured tuning never silently drops graph signal.

### Long-term per-user memory (`chat_longmem_enabled` + `_min_salience` + `_recall_top_k` + `_decay_days`)

A persistent per-user fact store (migration 0045). On every turn, an extractor LLM proposes facts; high-salience facts persist; on subsequent turns, a recall layer prepends the top-K most relevant facts to the prompt. **Why:** ChatGPT-style memory — the model should remember that the user is a Go developer working on JustRAG without being told every turn. Decay (`_decay_days`) prevents stale facts from dominating the recall pool indefinitely.

DSGVO requirement: privacy drawer (per-entry delete + bulk clear + JSON export) must ship before this is enabled on EU deployments — see CLAUDE.md recipe.

Implementation: `internal/longmem/longmem.go`.

### Long-context (System 2) routing (T2-1: `chat_longcontext_enabled` + `_max_tokens` + `chat_longcontext_mode`)

When the gate is on AND the query is `complex_reasoning` AND the keyword classifier (`IsGlobalSynthesisQuery` — EN+DE triggers like "summarise all", "across every", "Fasse alle … zusammen") fires: `SearchOptions.LongContextMode` is set. `Search()` then raises top-k to `LongContextTopK=200`, skips MMR / score-drop / parent-child swap; the chat pipeline replaces the standard 120k token budget with `chat_longcontext_max_tokens` and skips ECoRAG compression + multipass extraction (both would re-narrow the wide pool). Since Wave 3 this is the `OrchLongContext` orchestrator (`RunLongContextChat`), not just a `PrepareChatContext` branch — see the orchestrator table above.

**Consumer modes (`chat_longcontext_mode`, W3-R6).** One shared consumer (`internal/chat/longcontext_consume.go`) serves the orchestrator and the `PrepareChatContext` branch:

- `flat` — the token-budgeted pool goes to the answer LLM raw, sandwich-ordered. The **default until Wave 5**, and still the fallback for an unrecognised value. Byte-identical to the pre-Wave-3 behaviour: the same assembler (`assembleFlatFromParts`) now builds the standard path's prompt tail too, so the two cannot drift.
- `map_reduce` (**the default since Wave 5**, W5-R1 — see the decision below) — the pool is grouped in score order into `chat_longcontext_map_group_size` (default 8) chunk groups, each rendered with its ORIGINAL `[N]` headers. One fast-tier structured call per group (`ai.ExtractLongContextFindings`, `chat_longcontext_map_model`, up to `chat_longcontext_map_concurrency` in flight, 45 s per group) returns `{source_idx, claim, quote}` findings. The answer prompt then carries `FINDINGS (grouped by source)` plus the source headers — no chunk bodies. `Sources` and `FinalChunks` stay the full pool, so `[N]` citations, the citation validator and eval recall keep working. Consequence, accepted for a synthesis route: a chunk no finding surfaced cannot be cited. In `PrepareChatContext` the map stage is **skipped on abstain** (there is nothing to synthesise and the abstain notice belongs to the flat tail) — the same rule ECoRAG compression, multipass extraction and the sufficient-context gate already follow. Flat mode is unaffected.
- Fail-soft (W3-R7): a group whose extraction fails — including a panic, recovered into a per-group error via `safego.RecoverError` — contributes its chunks' first 600 runes as fallback findings, so evidence is never silently dropped. An all-empty map degrades to `flat`.

Trajectory events: `longcontext_route` (mode + query), `longcontext_map` (per group, emitted for failed groups too), `longcontext_reduce` (total findings).

**Measured (Wave 3 Task 4): the default stayed `flat` at the time — superseded by the Wave-5 decision two paragraphs down.** Judge A/B on `eval/golden/global-synthesis-de.jsonl` — 12 German global-synthesis questions on the PPM-Eval KB, gitignored; full tables and per-question anomalies in `eval/golden/global-synthesis-de.acceptance.md`. Three runs (flat, map_reduce, flat repeat), 12/12 questions dispatched to `longcontext` in each, 0 errors:

| Run | answer relevance | faithfulness | context precision | ctx-assembly latency | wall |
|---|---|---|---|---|---|
| flat | 1.000 | 0.613 | 0.409 | 10.2 s | 563 s |
| map_reduce | 1.000 | 0.565 | 0.600 | 63.4 s | 954 s |
| flat (repeat) | 1.000 | 0.597 | 0.420 | 10.3 s | 568 s |

The decision rule was "adopt `map_reduce` iff mean answer relevance improves beyond the noise band AND faithfulness does not drop beyond it". Its primary arm was **unsatisfiable on this route**: the Likert answer-relevance judge sees only question + answer and scores a 3000–5000-character structured German synthesis 5/5 by construction, in every run and both modes — a property of the instrument, not a finding about the modes. Faithfulness moved −4.8 pp, 0.34 × the ≈0.14 standard error of a 12-question mean (individual questions swing up to a full point between two runs of the *identical* configuration, so the 1.6 pp gap between the two flat means is cancellation, not stability) — "no detectable difference", not "worse". The single noise-exceeding effect is **context precision 0.409 → 0.600 (+19.1 pp, 3.5 × SE)**: the findings block doing exactly what W3-R6 designed it to do. That metric was explicitly de-scoped for this route, so it is recorded as grounds for keeping `map_reduce` available, not for defaulting to it. Measured cost: 6.2× the context-assembly latency (63.4 s vs 10.2 s per question) and **25 extra fast-tier calls per turn** on a 200-chunk pool at the default group size — bounded per turn, not per deployment, so set `AI_MAX_CONCURRENT_REQUESTS` before enabling it broadly. Observed live: on one question two groups hit the 45 s per-group budget and took the W3-R7 fallback path (207 findings instead of the usual 33–156) — designed degradation, no error surfaced, no evidence dropped.

**Re-measured with those judges (Wave 4 Task 5): the default still stayed `flat` then — again, superseded in Wave 5.** Wave 4 built what the Wave-3 verdict called for — a **coverage** judge (fraction of a golden row's `expected_points` the answer states) and an offline **pairwise preference** judge (`--pairwise-a` / `--pairwise-b`; each pair judged in both orders, counted only when both orders agree, so a position-bias flip becomes a tie instead of a false win) — then ran flat ×2 and map_reduce ×2 over the same 12 questions (all 12 dispatched to `longcontext` in every run, 0 errors) plus three pairwise comparisons. `map_reduce` raised mean coverage in **both** cross pairs (0.5625 → 0.6319, +6.94 pp; 0.5458 → 0.5736, +2.78 pp) against a 1.67 pp flat-vs-flat band, and took **16 of 20 pooled decisive pairs** (0.800, Wilson [0.584, 0.919]). The pre-registered rule (W4-R7) is nevertheless **not met**: it requires a Wilson lower bound above 0.50 on *each* cross pair, and pair 1 lands at 0.434 (8 of 11 decisive wins where 9 were needed) while pair 2 passes at 0.565. At 12 questions a pair yields only 9–11 decisive comparisons, so the per-pair bar is decided by one win — the set is under-powered, not the effect borderline; pooling after seeing pair 1 miss would be post-hoc rule-softening, so the pooled read is recorded as directional evidence only. `map_reduce` is therefore **measured favourably but not the default**, at 1.65× wall time (938 s vs 570 s per 12 questions). The control pair behaved as a control should: 3 wins / 6 ties / 2 losses with a 0.545 tie rate, i.e. two identical configurations are indistinguishable and the judge's discriminative power at this margin is modest. Full record, including three both-orders disagreement excerpts and the map-stage trajectory stats (300 groups per run, 3 and 1 failed groups, 0 dropped findings), in `eval/golden/global-synthesis-de.acceptance.md` §2. Two observations worth carrying forward: answer relevance was **not** uniformly saturated this wave (0.9167–1.0000 across the four runs), and one flat run produced a degenerate answer — ~15400 runaway underscore characters before a correct, complete table — which the pairwise judge explicitly flagged as a readability defect. That is a generation-layer bug, independent of the mode.

**Re-measured a third time under a pre-registered POOLED rule (Wave 5 Task 10): the default flips to `map_reduce`.** Wave 4 ended with two named remedies — grow the set, or re-register the rule on the pooled pairs *first*. Wave 5 did both, in that order. **W5-R1 was written on 2026-09-06, before the extended set was authored and before any of these runs existed**: over N ≥ 24 questions, pooled map_reduce wins / (wins + losses) ≥ 0.60 with the pooled Wilson lower bound (z = 1.96) > 0.50, pooled mean coverage not below flat's by more than the flat-vs-flat band, and the flat-vs-flat control win rate inside [0.35, 0.65] (outside it the judge is unstable and the run is inconclusive). Cost is reported, never a veto. The set then grew to **24** questions (G01–G12 byte-identical, G13–G24 authored with the same discipline — a documented DE trigger verbatim, `must_cite_file_names` from ground truth, 6 `expected_points` each verified against a source chunk fragment).

Four judged runs (flat ×2, map_reduce ×2) — every one with 0 errors, 24/24 questions dispatched to `longcontext` and 24/24 coverage present, so nothing had to be discarded or repeated — plus the same three pairwise comparisons, pooled with the new `--pairwise-pool`:

| Pair | decisive | map_reduce win rate | Wilson [lo, hi] | tie rate |
|---|---|---|---|---|
| pw1 (flat1 vs mr1) | 21 | 0.9524 | [0.773, 0.992] | 0.125 |
| pw2 (flat2 vs mr2) | 15 | 0.9333 | [0.702, 0.988] | 0.348 |
| control (flat1 vs flat2) | 11 | — (A side: **0.3636**) | — | 0.542 |

Pooled: **34 of 36 decisive pairs (0.9444), Wilson lower bound 0.8186**; coverage map_reduce 0.5837 vs flat 0.5333 = **+5.03 pp** against a **1.39 pp** flat-vs-flat band; control **0.3636**, inside the window. All four sub-criteria pass, so `chat_longcontext_mode` defaults to `map_reduce` — the route itself is still gated by `chat_longcontext_enabled` (off by default). Both cross pairs would also clear a per-pair bar this time, so the pooled and per-pair reads agree; that is post-hoc supporting evidence, not part of the rule. **Cost: 1.28× wall time** (1797 s vs 1401 s per 24 questions) — the lower ratio versus Wave 4's 1.65× comes from one unusually fast flat run, not from a cheaper map stage (still 25 fast-tier calls per question). Diagnostics that are deliberately *not* decision inputs: faithfulness came out marginally lower for map_reduce (0.461 / 0.533 vs 0.464 / 0.569 — a findings block is a lossy intermediate), and answer relevance saturated at 1.000 again. One pairwise pair (G06) and two faithfulness judge calls failed to parse on the known code-fenced-JSON failure mode and were excluded per the existing convention; none of the four criteria moves. No degenerate answer occurred this wave (longest answer 6638 runes) — the Wave-4 runaway-underscore defect now has a guard of its own, `chat_answer_degenerate_run_limit`, and its trajectory marker was searched for and not found in any of the four runs. Full record: `eval/golden/global-synthesis-de.acceptance.md` §4.

**What the flip does and does not change.** An **unset** key now reads `map_reduce`; an **unrecognised** value still normalises to `flat` and logs a warning, so a typo can never buy the expensive mode. Pin the previous behaviour with an explicit `chat_longcontext_mode = flat`.

Reproduce with `cmd/eval --production-context --longcontext on --longcontext-mode flat|map_reduce` (the explicit `flat` is now the one that overrides the live default), then compare two judged reports with `cmd/eval --pairwise-a A.json --pairwise-b B.json --pairwise-out out.json` and pool two such comparisons with `cmd/eval [--pairwise-out pooled.json] --pairwise-pool a.json b.json` — **flags before the positional paths**, and the same configuration on side A in both inputs (the command warns, it cannot check). Both `--longcontext*` flags overlay the site_config for that run only. The map/reduce counts are trajectory events, not report fields — scrape them from the `rag.longcontext.map_reduce` log lines.

**Why:** global-synthesis queries ("what does every document say about X?") have a System-1 failure mode — top-k=10 with strong reranker discrimination is exactly wrong for them, because the *coverage* of the chunk pool is what determines whether the synthesis can be accurate. The long-context route trades per-turn LLM cost (~30× when the gate fires) for a much wider, less-filtered evidence pool.

**Scope note:** this is "wide-retrieval mode" rather than full retrieval bypass. The pipeline still BM25/vector-searches against the query (so relevance ranking still applies to the wider pool); chunks reach the LLM raw, no post-filter. A true `FetchAllChunks` path that ignores query relevance would need new Searcher methods and is deferred.

The classifier is keyword-only in this cut; an LLM-based classifier behind a sub-flag is a follow-up. Telemetry: `rag_longcontext_route_total{outcome,mode}` — watch `fired` against the `considered` denominator (gate on, turn eligible — `complex_reasoning`, no `Enhance` — but the classifier did not match) before broad rollout. **Upgrade note:** the label set gained `mode`, and `outcome` gained `considered` and `map_empty`, so any dashboard or alert keyed on the pre-Wave-3 label set breaks. Both emitting surfaces (the orchestrator in `http_send.go` and the `PrepareChatContext` branch) apply the same eligibility test, so `fired/considered` means the same thing on both; the one accepted imprecision is that an orchestrator error falls through to `PrepareChatContext`, which re-evaluates the same turn and contributes a second `considered` (or `fired`) — named in the metric's own help text. The shape hash (`internal/vector/query_cache_shape.go`) includes `LongContextMode` so cached normal-mode results don't collide with long-context entries.

### Self-RAG verifier (`chat_self_rag_enabled` + `chat_self_rag_model`)

Mutually exclusive with the factuality verifier; replaces it with a unified verifier that emits both per-claim grades and an overall "ISREL/ISSUP/ISUSE" classification per the Self-RAG paper. **Why:** the factuality verifier checks "does the chunk support this claim"; Self-RAG additionally checks "was retrieval even relevant in the first place". The combined signal catches a failure mode where the verifier rubber-stamps unsupported claims because no retrieval was attempted.

### Iterative DAG critic (`chat_plan_execute_dag_iterative` + `_model`)

Inserts a critic LLM between DAG levels: after level N completes, the critic reviews accumulated findings and may re-plan level N+1. **Why:** the up-front planner makes decisions before seeing any retrieval results. The critic incorporates evidence into the plan as it materializes — the difference between "scheduled-then-executed" and "actually iterative".

Implementation: `internal/chat/dag_critic_adapter.go`.

### Eval regression gate (local / manual)

There is **no** GitHub Actions eval workflow. The golden sets are private (gitignored under `eval/golden/`) and the eval needs live LLM + embedding infra that GitHub-hosted runners lack, so regression-gating is manual: run `cmd/eval` against the golden set and diff the result against the committed config snapshots in `eval/golden/snapshots/` (see that directory's README). **Why a gate at all:** retrieval regressions are silent — a one-line change to the BM25 query builder can drop nDCG by 5pp without any test failing. CI (`.github/workflows/test.yml`) covers vet / govulncheck / race tests / benchmark smoke only.

### Online faithfulness metric

When the factuality verifier or Self-RAG ran, the answer's faithfulness score (fraction of claims supported) emits as a Prometheus metric. **Why:** unlike the offline eval (manual, periodic), the online metric tracks faithfulness on real production traffic in real time — early warning for prompt drift or provider regressions that the golden set wouldn't catch.

Implementation: `internal/chat/online_faithfulness.go` (`computeFaithfulnessScore` at line 24).

---

### Conflict / supersession surfacing (`chat_conflict_surfacing_enabled`, default OFF)

One structured fast-tier call after the final chunk set is assembled — on the standard `PrepareChatContext` path **and** on the Supervisor path, mirroring the tabular router's dual wiring and running directly after it — comparing the turn's own numbered sources for pairs that contradict each other or supersede one another. **Why:** a corpus that keeps both a NEU advisory and its UPDATE, or two versions of a policy, will happily hand both to the answer LLM with nothing marking one as stale; the reader cannot tell which sentence is current. Direction (`newer`) is decided from each source's date line **alone** — the prompt explicitly forbids guessing — and the dates come through the existing `chat.FileDateLookup`, reusing whatever a `ChatSource` already carries and issuing at most one batched query for the rest.

Gates, in order: the flag; the turn is not already abstaining; and — checked on the **capped** list (`chat_conflict_max_chunks`, default 12), because capping can collapse a two-file set into a one-file one — at least two distinct files. The gate short-circuits ahead of the date lookup as well as the model call. Output: a system-prompt addendum (placed after the tabular addendum, before `CONTEXT:`, by the one shared flat assembler so the standard path and `OrchLongContext` flat mode cannot drift), a `conflicts` array on the persisted message, and an SSE frame immediately after `sources`.

**One wire shape everywhere** — `conflicts` is the bare array of `{claim, sourceA, sourceB, kind, newer, fileA, fileB}` on the frame, in the non-streaming body, in `messages.conflicts` and on reload, omitted entirely when empty; `sourceA`/`sourceB` are the turn's `[N]` citation indices. The detector itself **fails closed** (unlike the sufficient-context judge, which fails open): a badge is an assertion about the corpus, so an unparseable reply produces no badge rather than a guess. Every returned row is validated against the input numbering, and the whole pass is fail-soft on timeout (`chat_conflict_timeout_ms`, default 6000) with a trajectory event. Metric `rag_conflict_surfacing_total{outcome}`; `none` exists so a flag rate has a denominator.

`internal/publicapi`, `internal/openaicompat` and `internal/mcpserver` reach `PrepareChatContext` too, so with the flag on they get the **addendum only** — no frame, no persisted blob — and thread no `FileDateLookup`, so direction there is always `unknown`.

**Measured in Wave 5, and it stays off.** Both pre-stated gates failed: 0 of 8 CERT NEU/UPDATE pairs flagged (a fixture property — MMR never assembles both halves of the queried pair; on pairs it did see, 12 of 37 opportunities hit, direction 12/12 correct, zero invented pairs) and a 0.124 false-positive flag rate on the PPM set against a ≤ 0.10 bar — **an upper bound**, since two of the 13 entries paired a file with itself. Cost +631 / +268 ms per turn; retrieval identical to three decimals. Record: `eval/golden/cert-recency-de.acceptance.md` § "Conflict surfacing (Wave 5)". `internal/chat/conflicts.go`, `internal/ai/source_conflicts.go`.

### Degenerate-answer guard (`chat_answer_degenerate_run_limit`, default 400)

A generation-layer guard, independent of orchestrator and of retrieval. **Why:** Wave 4's flat long-context run produced an answer containing ~15400 unbroken underscore characters before an otherwise correct table — the model collapsed into repetition, the client rendered it, and nothing in the pipeline noticed. While an answer streams, the assembled text is watched for the maximal region periodic with period p ∈ [1, 4] (one repeated rune, or a repeated 2–4-rune pattern), counted **continuously across chunk boundaries** via an explicit carry — a provider always splits such a run across many chunks, so a per-chunk check would never fire.

On a trip the guard cancels the child context the *completion* runs under (so the provider stops generating, and billing) while the request context stays live for the SSE writes, the persist and the post-response tasks; it then stops forwarding, strips the run from the buffered answer, appends a one-line notice in the answer language and streams exactly that as the final content frame. The turn ends normally. A guard cancel is distinguished from a real error by the guard, never by the error value (`err != nil && !guard.tripped()`), so the client never sees an error frame.

Every answer surface is covered: web (both streaming paths and, post hoc, the non-streaming one), the public API and OpenAI-compat (each has its own stream loop, so both abort rather than only stripping afterwards), and `ask_kb` post hoc. All four streaming trips go through one exported forced variant, `chat.GuardStreamedAnswer(buffered, sent, limit, lang, surface)`: forced because the tracker already tripped (the answer is truncated whether or not a second detection over the buffer re-finds the run), on the tracker's own limit (no second site_config read, and never a limit other than the one that fired), and returning the content the client is still owed — the trip chunk is buffered but not forwarded, so any legitimate text ahead of the run inside it is streamed back before the notice. That is what keeps the client's assembled text equal to the guarded answer, and hence what makes OpenAI-compat's closing-chunk citation annotations, computed over the **guarded** text, line up with what the client holds (pinned by `TestAnnotationOffsetsMatchClientTextAfterADegenerateTrip`). Reasoning tokens are deliberately not guarded. Trajectory `answer_degenerate_guard{limit, run_length}`; metric `rag_answer_degenerate_total{surface}` — alert on any non-zero rate: the guard contains the symptom, it does not fix the model. `0` disables it everywhere (which is why `publicapi`/`openaicompat` gained a site-config reader at all); a real limit clamps to [50, 100000]. 400 sits above any realistic Markdown rule (~300) and three orders of magnitude below the observed failure. `internal/chat/degenerate_guard.go`.

## Sections referenced by CLAUDE.md but not yet expanded

Each of these is marked "(`docs/agent-orchestration.md` not yet written)" in the CLAUDE.md feature index. The flag-level operational reference in CLAUDE.md is the source of truth until these subsections land here:

- **Long-term memory ANN recall** (T1-2) — see CLAUDE.md `chat_longmem_recall_semantic` caveat (depends on the dim migration).
- **Long-term memory conflict resolution** (T1-3) — see CLAUDE.md `chat_longmem_conflict_resolution` recipe (Mem0-style `{create_new, supersede, skip_redundant}` classifier).
- **Sub-question decomposition** (T1-1) — see CLAUDE.md "Sub-question decomposition (DecomposeRAG)" recipe.

These features are *implemented and operational*; they just don't yet have the mechanism-and-rationale write-up that the older features above have.
