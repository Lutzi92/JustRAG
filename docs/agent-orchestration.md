# Agent orchestration

The *why* behind every chat-pipeline orchestrator and feature. The flag list, recipes, and migration table live in [`../CLAUDE.md`](../CLAUDE.md); this doc is the depth reference for mechanism and design decisions. Retrieval-pipeline subsystems (BM25, vector, reranker, MMR, CRAG, citation validator, …) live in [`retrieval.md`](retrieval.md) — open this doc instead when the question is "how does the chat layer route a turn through retrieval and back out as an answer?".

When CLAUDE.md says "see `docs/agent-orchestration.md` for the full rationale", that's a pointer to a section in this file.

## Read order

1. **Trajectory streaming** — how SSE events surface every decision; required mental model for everything else.
2. **The four orchestrators** — Supervisor, Plan-and-Execute, Agentic, Standard fallback. Priority order and dispatch.
3. **Feature index** — each chat-pipeline feature, why it exists, how it composes with the orchestrators.

## Trajectory streaming

Every orchestrator emits structured `agent_*` SSE events alongside the answer stream: `hop`, `iterate`, `plan`, `decision`, `agent_dispatch`, `agent_tool_call`, `answer`. The frontend renders these as a live decision log; the admin metrics panel persists them into `agent_decisions` (migration 0042) for offline analysis.

**Why:** debuggability is the dominant cost in agentic systems. Without per-decision tracing, a wrong final answer is unbisectable — you can't tell if the planner chose bad sub-questions, the search arm missed the chunks, or the answer LLM hallucinated past available evidence. Trajectory events make every orchestrator reproducible from the SSE log alone.

Implementation: `internal/chat/trajectory.go` (event encoder), `internal/chat/agentic_chat.go`, `internal/chat/plan_execute_chat.go`, `internal/agents/supervisor.go` (emit sites). The events stream alongside content tokens on the same SSE channel.

## The four orchestrators

Streaming chat for `complex_reasoning` queries dispatches through the first orchestrator whose flag is on. Dispatch happens in `internal/chat/http_send.go` (`Handler.tryDeepChat`, around line 289).

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

### Long-context (System 2) routing (T2-1: `chat_longcontext_enabled` + `_max_tokens`)

When the gate is on AND the query is `complex_reasoning` AND the keyword classifier (`IsGlobalSynthesisQuery` — EN+DE triggers like "summarise all", "across every", "Fasse alle … zusammen") fires: `SearchOptions.LongContextMode` is set. `Search()` then raises top-k to `LongContextTopK=200`, skips MMR / score-drop / parent-child swap; the chat pipeline replaces the standard 120k token budget with `chat_longcontext_max_tokens` and skips ECoRAG compression + multipass extraction (both would re-narrow the wide pool).

**Why:** global-synthesis queries ("what does every document say about X?") have a System-1 failure mode — top-k=10 with strong reranker discrimination is exactly wrong for them, because the *coverage* of the chunk pool is what determines whether the synthesis can be accurate. The long-context route trades per-turn LLM cost (~30× when the gate fires) for a much wider, less-filtered evidence pool.

**Scope note:** this is "wide-retrieval mode" rather than full retrieval bypass. The pipeline still BM25/vector-searches against the query (so relevance ranking still applies to the wider pool); chunks reach the LLM raw, no post-filter. A true `FetchAllChunks` path that ignores query relevance would need new Searcher methods and is deferred.

The classifier is keyword-only in this cut; an LLM-based classifier behind a sub-flag is a follow-up. Telemetry: `rag_longcontext_route_total{outcome}` — watch the `fired` count vs traffic before broad rollout. The shape hash (`internal/vector/query_cache_shape.go`) includes `LongContextMode` so cached normal-mode results don't collide with long-context entries.

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

## Sections referenced by CLAUDE.md but not yet expanded

Each of these is marked "(`docs/agent-orchestration.md` not yet written)" in the CLAUDE.md feature index. The flag-level operational reference in CLAUDE.md is the source of truth until these subsections land here:

- **Long-term memory ANN recall** (T1-2) — see CLAUDE.md `chat_longmem_recall_semantic` caveat (depends on the dim migration).
- **Long-term memory conflict resolution** (T1-3) — see CLAUDE.md `chat_longmem_conflict_resolution` recipe (Mem0-style `{create_new, supersede, skip_redundant}` classifier).
- **Sub-question decomposition** (T1-1) — see CLAUDE.md "Sub-question decomposition (DecomposeRAG)" recipe.

These features are *implemented and operational*; they just don't yet have the mechanism-and-rationale write-up that the older features above have.
