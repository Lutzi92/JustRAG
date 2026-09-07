# Retrieval pipeline reference

**What this is:** the full mechanism + rationale + operational eval history for every stage of JustRAG's retrieval pipeline. Each section below corresponds to one tunable subsystem; the `site_config` knob names appear in the section bodies so you can grep for them. For the admin-UI knob reference (what to flip, when), see `docs/rag-tuning-guide.md`. For the project-level architecture map and toggle recipes, see `CLAUDE.md`.

**Read order:** the sections are roughly query-flow ordered (query enters → cache → query rewriting → vector/BM25/reranker → MMR → context assembly → answer-time validation), but each section stands alone. Skim the headings and dive in where relevant.

## MMR (top-k diversity)

Top-k chunks are selected with MMR (Maximal Marginal Relevance) for diversity. λ is configurable in the admin Agent panel (`mmr_lambda`, default 0.7). Set 1.0 to disable. Diversity is measured via Jaccard word overlap on chunk content (no extra DB fetch).

## Auto spell correction

Auto spell correction (`auto_spell_correct`, default **off** as of April 2026): when enabled, an LLM-based spell-correct pass runs on every query that doesn't carry an explicit `enhance` flag. The corrected text is used only for the vector/BM25 search — it is NOT surfaced back to the user via `result.EnhancedQuery`, so the UI renders the original query unchanged. This silent mode exists because mid-range LLMs tend to drift from pure typo correction into paraphrase, and surfacing "your query was rewritten" when the user never asked for an enhancement felt intrusive. Users who actively want visible query correction should pass `enhance: "spell"` per request — that path sets `EnhancedQuery` as before. Default was flipped from `true` to `false` so clean queries don't pay an extra LLM round-trip on every search.

## Step-back prompting

Step-back prompting (`step_back_enabled`, default off): when on AND the query is classified as complex_reasoning, an LLM call generates a single broader question (e.g. *"How do A and B differ?"* → *"What are A and B?"*). The broader query is run as an extra vector search whose results fold into the existing RRF fusion via the same `extraLists` path used by multi-query. Off for lookup/enumeration/unknown queries — abstraction harms named-entity retrieval. Fail-open: LLM errors and empty output proceed without the extra list. Outcomes recorded on `rag_stepback_decision_total{outcome=...}` (fired / skipped_route / skipped_disabled / llm_error / empty_output) plus `rag_stepback_seconds` for LLM-call latency. Cost: ~50ms on a small chat model.

## Semantic query cache

Semantic query cache (`query_cache_enabled`, default off): full `SearchResult` payloads are stored in a pgvector-backed `query_cache` table keyed on `(kb_id, shape_hash)` plus an HNSW index over the query embedding. Lookup runs before embedding/HyDE/multi-query/step-back: an exact `(kb_id, shape_hash)` match is sub-millisecond, semantic-paraphrase ANN lookup is ~5–15 ms. Threshold default 0.96 cosine (`query_cache_similarity_threshold`, [0–1]); TTL default 24 h (`query_cache_ttl_hours`, [1–720]). Writes are async via a bounded channel (256 entries) — overflow drops the write and increments `rag_query_cache_write_dropped_total`. `shape_hash` covers strictly request-scoped fields (Enhance, QueryType, FileIDs sorted, HyDE/MultiQuery/StepBack/Grade flags, GraderModel, ModelOverride, resolved TopN) plus the resolved **embedding model identifier** (2026-06-10: a same-dim embedder swap keeps the cache table but makes cosine matches against old query embeddings meaningless — without the model in the hash a swap silently served stale-semantics hits) and a 1-byte schema-version constant; deployment-wide config (alpha, RRF weights, MMR λ) is intentionally NOT in the hash — operators bump `queryCacheSchemaVersion` to force-invalidate. Per-KB nuke runs on every file mutation (ingestion completion, deletion, re-ingestion, KB/user deletion) — surfaced as `rag.query_cache.invalidate` event and `rag_query_cache_invalidations_total` counter. An hourly sweep deletes expired rows and refreshes `rag_query_cache_size`. Hit telemetry: `rag_query_cache_hit_total{tier="exact|semantic"}` (exact = cosine distance < 0.001, semantic = paraphrase). Retire the cache if multi-KB hit rate stays below ~5 %.

## Reranker score weighting

Reranker score weighting: when a cross-encoder reranker is active, the final score is `alpha * rerankScore + (1 - alpha) * normalizedRRF`. Configurable via admin Agent panel (`rerank_blend_alpha`, default 0.8 = 80/20 blend). Set 1.0 for pure reranker dominance, 0.0 to ignore reranker scores entirely. The default was raised from 0.5 to 0.8 on 2026-05-04 after switching the reranker from bge-reranker-v2-m3 to jina-reranker-v3 (see "Reranker deployment" below) and running an α-grid on the 89-question eval set. Result at α=0.8 vs α=0.5: overall recall 0.909 vs 0.905, MRR 0.901 vs 0.895; per-route the gains concentrate on enumeration (MRR 1.000 vs 0.964, nDCG 0.981 vs 0.955) and complex_reasoning (recall 0.873 vs 0.863, MRR 0.952 vs 0.926). Pure α=1.0 marginally improves complex_reasoning MRR (0.973) but craters precision (0.229 vs 0.290 at α=0.8), so 0.8 is the better global trade. The bge-era observation that α=1.0 cost ~10 pp lookup recall no longer reproduces with jina-v3 — lookup recall is α-invariant at 0.938 across 0.5/0.8/1.0, because the BM25 floor reinsertion recovers any exact-match chunks the reranker drops regardless of blend weight; what α controls now is mostly MRR/precision, not recall. The RRF half still carries BM25's exact-match evidence as ranking signal on named-entity lookup queries; it's no longer load-bearing for recall but does help lookup MRR (0.850 at α=0.5 vs 0.831 at α=0.8). Per-query-type overrides (`rerank_blend_alpha_lookup`, `rerank_blend_alpha_enumeration`, `rerank_blend_alpha_complex_reasoning`) remain available — α=0.5 is still strictly best for lookup if you want to chase the per-route optimum.

## Rerank candidate depth

`rerank_candidate_depth` (+ per-route `_lookup`/`_enumeration`/`_complex_reasoning`, per-KB overridable) resolves how many fused candidates `Search()` fetches per arm *before* reranking — the pool the reranker scores, distinct from `top_n_*` (the pool the caller gets back after reranking). Resolution ladder in `EffectiveRerankDepth` (`internal/vector/rerank_depth.go`): per-route key (sentinel 0 = inherit) → global key (sentinel 0 = legacy formula) → `max(4×top-N, 50)` with a reranker active / `max(2×top-N, 30)` without. The legacy formula is returned as-is, uncapped, matching pre-branch behavior exactly. The `[top-N, MaxRerankCandidateDepth]` (500) clamp applies only when an operator has supplied an explicit depth (global or per-route key) — a long-context query or a KB with a large `top_n_*` must not get a narrower pool than before this knob existed. Logged as `rerank_depth` on the `rag.search.stages` event. ⚠ The site_config value parser (`internal/vector/search_siteconfig.go`) only accepts `0` or `10–500` — the registry's declared `Min` is 0, but a value of 1–9 fails the parse and silently falls back to the legacy formula rather than erroring, so don't expect a "just above zero" setting to do anything.

A wider pool gives the reranker more candidates to discriminate among, which can lift recall on complex_reasoning queries whose relevant chunks rank outside the legacy pool's window — at the cost of reranker latency (watch `rag_pipeline_stage_duration_seconds{stage="rerank"}`, jina-v3 is roughly linear in candidate count). **Grid to run:** 50 / 80 / 120 / 200 per route (lookup / enumeration / complex_reasoning) against the production golden set, using the local A/B procedure in `docs/feature-recipes.md`'s "Scheduled eval + regression gate" recipe (`cmd/eval --baseline`). No numbers recorded yet on this branch — the knob shipped ungridded (Wave 1); **re-run the grid after Wave 2's BM25 changes land**, since the floor/RRF mix those changes touch interacts with how much of the pre-rerank pool actually matters.

## Per-query-type top-N overrides

Per-query-type top-N overrides (`top_n_lookup`, `top_n_enumeration`, `top_n_complex_reasoning`, all default 0 = inherit `default_top_k`): mirrors the `rerank_blend_alpha_*` per-route pattern. Resolves to the global `default_top_k` when 0 (sentinel). Lookup queries typically work better at a smaller pool (e.g. 10) — they have one canonical answer chunk and a wider pool just adds reranker latency and dilutes context. Complex_reasoning may benefit from a wider pool (e.g. 25-30) when the answer spans multiple sources. Resolved via `EffectiveTopN(siteCfg, opts.QueryType)` in `internal/vector/search.go`; logged on `rag.search.stages` as `top_n_route` (the resolved label: lookup/enumeration/complex_reasoning/global).

## BM25 floor

BM25 floor: after all scoring, dedup, MMR and the final `limit` trim, the pipeline reinserts the top-BM25 chunks (one per file, up to 4 distinct files) that got dropped along the way. Presence is checked at the **chunk ID level**, not the file level — a project profile with a team-list chunk (contains the named entity) AND a description chunk (doesn't) can have its description chunk kept by the reranker while the team-list chunk vanishes; file-level recall would falsely report "file present" and skip reinsertion, but the answer would still miss the mention. The lowest-scored slot in the final set is replaced per reinsertion (MMR's tail is its weakest pick). Reinserted chunks' scores are **boosted to the top score of the current final set**, not left at the reranker-demoted snapshot value — otherwise the subsequent score-sort + SandwichOrder places them in the lost-in-the-middle zone of the context and the LLM skips them despite the prompt correctly labeling them. Boosting sends them to the array boundaries where sandwich ordering puts them at the front/back for strong attention. This was originally added because the then-deployed reranker (Qwen3-Reranker-4B via vLLM pooling mode, since known to be miscalibrated — see "Reranker deployment") systematically misranked structural chunks (team lists, attribute tables). The current calibrated reranker (jina-reranker-v3) doesn't show that pathology, but the floor became *more* important after the 2026-05-04 default change to `rerank_blend_alpha=0.8`: at higher α, BM25 evidence contributes less to ranking, so the floor's role shifts from "ranking safety net" to "recall safety net". The α-grid showed lookup recall holding flat at 0.938 across α=0.5/0.8/1.0 specifically because the floor reinserts named-entity chunks the reranker downranks; without the floor, pure-α=1.0 lookup recall would likely fall. Surfaced as `bm25_floor_reinserted` in the `rag.search.stages` log event; retiring the floor is now a worse idea than it was under α=0.5, not better.

## Reranker deployment

Reranker deployment: the verified reranker configuration as of 2026-05-04 is `jina-reranker-v3` exposed via a Cohere-compatible `/rerank` endpoint. On the 89-question eval set it improves over the previous bge-reranker-v2-m3 baseline at the same blend (α=0.5): overall recall 0.905 vs 0.901 (+0.4 pp), precision 0.308 vs 0.285 (+2.3 pp), with the largest per-route gain on enumeration nDCG (+5.6 pp). The α-grid run on jina-v3 also showed it tolerates a higher blend weight than bge did (α=0.8 wins overall, see "Reranker score weighting" above) — interpret this as the new reranker's discriminative scores being trustworthy enough that the RRF/BM25 half can step back from ranking dominance, with the BM25 floor still recovering exact-match recall as a separate safety net. Earlier deployment notes preserved here for reference: bge-reranker-v2-m3 (BAAI) is also a good choice, served by vLLM 0.19+ (XLMRoberta cross-encoder, no `hf_overrides`, no `--trust-remote-code`, no `--task` flag — vLLM auto-resolves to pooling runner + classify pipeline); spot-check on a German named-entity query showed the literal-answer chunk at ≥ 0.99 vs. unrelated boilerplate ≤ 0.01. **Anti-pattern that cost a day of debugging: Qwen3-Reranker-4B (or any `classifier_from_token` + chat-template reranker — Gemma-based, Qwen2-based, Qwen3-VL variants) served via vLLM `/rerank` in pooling mode.** vLLM doesn't apply the trained chat template to `(query, doc)` inputs in that path, so the model never sees the format it was trained on, the `classifier_from_token` head reads logits off a last-hidden-state that represents "generic semantic pair similarity" instead of "yes/no relevance", and scores collapse to the 0.45–0.55 band with a ~0.01 margin on obvious relevance. The `rerank_use_chat_template` + `rerank_instruction` admin knobs and the code path in `internal/ai/rerank_qwen3.go` exist to work around this for deployments that serve such a reranker in `--runner generate` mode (so `/chat/completions` + `logprobs` is available and the template can be applied client-side) — keep them off for the current jina-v3 / bge-v2-m3 deployments; they're no-ops on those rerankers by design (model-family detection).

## Embedder choice (production default)

**Current production default (since 2026-06-01): `qwen3-embedding-8b`, 4096-dim, `document_chunks_4096`.** Switched back from `Octen/Octen-Embedding-8B`. Same dimension, so the active vector table is unchanged and the switch needed no re-ingest or dim migration. The embedder lives in admin `site_configs`, not in code — it is swappable at runtime, but vector tables are dim-keyed (`document_chunks_2560` for a 2560-dim model, `document_chunks_4096` for a 4096-dim one), so a *cross-dimension* switch does require a re-ingest.

### Current baseline: qwen3-embedding-8b (2026-08-18)

The first golden-set run on the current production embedder, closing the 78-day
gap that opened with the 2026-06-01 switch.

Config: `qwen3-embedding-8b` (4096-dim, `document_chunks_4096`) + `jina-reranker-v3`
at α=0.8, retrieval-only (no `--production-context`, so no CRAG / enumeration
pre-pass / orchestrator), k=10, `search_limit` 50, MMR λ=0.7, `hnsw_ef_search` 151,
parent-child OFF. Fixture `eval/golden/production-ppm-2026-08.jsonl`, 89 questions,
**0 errors**.

| Route | n | recall | precision | MRR | nDCG |
|---|---|---|---|---|---|
| **overall** | 89 | **0.911** | 0.328 | **0.919** | 0.918 |
| lookup | 43 | 0.948 | 0.337 | 0.899 | 0.906 |
| enumeration | 14 | 0.899 | 0.286 | 1.000 | 1.000 |
| complex_reasoning | 32 | 0.868 | 0.334 | 0.910 | 0.899 |

> ⚠️ **This is a new baseline, not a clean A/B against the older numbers.** The
> corpus was rebuilt between them: the fixture now runs against KB `PPM-Eval`
> (297 files, a fresh Confluence export of the Digital-PPM **and** "Neue Wege mit
> KI" spaces), where the pre-2026-06 numbers were measured on a smaller, differently
> cut corpus, and ground truth is now keyed on file **names** rather than UUIDs.
> Same 89 questions and the same route split (lookup 43 / enumeration 14 /
> complex_reasoning 32), so the routes stay comparable in shape — but read every
> delta below as indicative, not as an isolated embedder effect.

**On the complex_reasoning MRR question this section used to flag as open:** the
2026-05-04 A/B predicted `Qwen3-Embedding-8B` would cost −7.0 pp complex_reasoning
MRR against the 4B (0.952 → 0.882 predicted). Measured now: **0.910** — a −4.2 pp
gap to the 4B figure and −6.7 pp against Octen-8B's 0.977, so the regression is
real but milder than predicted. It is *not* proof that the 8B swap was harmless:
the corpus differs, and complex_reasoning remains the weakest route on both recall
(0.868) and nDCG (0.899). Enumeration MRR at a flat 1.000 says the reranker still
puts a correct chunk first on every list query; enumeration *recall* (0.899) is
what the long multi-file questions cost.

**Persistent failure cluster.** Four questions score recall 0: Q025, Q061, Q085,
Q087. Q037 (22 gold documents) scores 0.09. The Octen-8B baseline recorded the
cluster Q061/Q037/Q032/Q023/Q085 — four of those five still fail across an
embedder change *and* a corpus rebuild, which makes them a fixture/corpus property
rather than an embedder artifact. Q087's gold document is effectively empty in the
export (the "Kurzanleitung" page is a Confluence macro that did not survive), and
Q006/Q087 carry substitute ground truth (see the fixture header) — so treat Q087
as a data-quality row, not a retrieval row.

> **Historical figures below still do not describe production.** Every recall /
> MRR / nDCG number in this document dated before 2026-08-18 was measured on a
> different embedder, a different corpus, or both. Use the table above as the
> reference for a new knob's A/B; quote the older ones only as history.

### Historical: Octen-Embedding-8B baseline (2026-05-07 → 2026-06-01)

Measured on `Octen/Octen-Embedding-8B` (4096-dim) at α=0.8 + jina-v3, parent-child OFF, on the 89-question JLU set. **These numbers no longer describe production** — retained for the embedder-comparison history and the methodology.

Overall recall 0.928 (vs 0.909 on Qwen3-4B at the same α=0.8 + jina-v3), MRR 0.949 (vs 0.901), and complex_reasoning MRR 0.977 with no regression vs the 4B baseline — the first 8B embedder tried that AVOIDED the complex_reasoning ranking hit that had previously kept the deployment on 4B (Qwen3-Embedding-8B dropped complex_reasoning MRR by −7.0 pp; Octen-8B held it). Parent-child chunking was OFF on this baseline (the trade-off reversed under Octen-8B — see `project_rag_parent_child.md` memory); whether that still holds under Qwen3-8B is also unmeasured. The Q006 PPM-Team email failure that survived every Qwen3-4B configuration was resolved under Octen-8B. Full numbers and the persistent failure cluster (Q061/Q037/Q032/Q023/Q085) in `project_octen_8b_baseline.md`.

The historical Qwen3 4B-vs-8B analysis below remains the most load-bearing section on this page right now — it is the only measurement of the model family currently in production, and it is a cautionary tale that "8B beats 4B" is not generic; it is specific to a given 8B at this deployment scale.

## Qwen3 4B vs 8B (2026-05-04 A/B — the model family now in production)

> **Status note.** The "stay on 4B" decision recorded below was superseded twice: by Octen-8B on 2026-05-07, and by the switch back to `qwen3-embedding-8b` on 2026-06-01. **Production now runs the 8B side of this A/B.** Read the measured deltas as live risk, not as history — they are the best available prediction of the current config's behaviour until the pending baseline run replaces them.

`Qwen3-Embedding-4B` (2560-dim) vs `Qwen3-Embedding-8B` (4096-dim): on 2026-05-04 a clean A/B at fixed α=0.8 + jina-reranker-v3 showed the 8B variant trades real lookup gain for a substantial complex_reasoning ranking regression. Lookup: recall 0.938 → **0.961** (+2.3 pp), MRR 0.831 → **0.858** (+2.7 pp), nDCG 0.857 → **0.880** (+2.3 pp). Most striking, q006 ("Unter welcher E-Mail-Adresse ist das PPM-Team erreichbar?") — recall 0.000 in every bge-era and jina-era run with 4B at every α — finally surfaces the answer chunk under 8B. Complex_reasoning: MRR 0.952 → **0.882** (−7.0 pp), nDCG 0.923 → **0.869** (−5.4 pp), recall flat. Net overall: recall +0.9 pp, but MRR −1.2 pp and nDCG −0.7 pp, plus 1.6× the embedding storage / HNSW index size. **Mechanism (hypothesis):** the reranker is held constant, so the only delta is the candidate set it scores. Complex_reasoning queries have multiple semantically similar relevant chunks across the KB; the 8B's stronger embedder pulls more "near-peer" candidates into the pre-rerank pool, forcing the reranker into a harder discrimination task and pushing the right chunk from rank 1 to rank 2-3. Lookup queries don't have this problem — they have one canonical answer chunk and the 8B's broader recall just brings it into the top-N. **Decision at the time (2026-05-04, since superseded):** stay on 4B. The MRR loss is what matters under sandwich ordering (high-MRR chunks land at attention boundaries; mid-rank chunks land in the lost-in-the-middle band).

**What this means now that production runs Qwen3-8B:** the −7.0 pp complex_reasoning MRR figure is an *unretired* risk, not a closed one. Two things follow.

1. **The pending baseline run should report complex_reasoning MRR first.** If it lands near 0.88 rather than the 0.977 Octen-8B reached, this A/B explains why and the regression is real. If it lands high, something else in the pipeline (contextual enrichment's person-role prompt, the α=0.8 default, the BM25 floor) has since absorbed the effect — worth knowing which.
2. **Per-query-type embedder routing is now the obvious lever**, not a hypothetical one. This A/B says 8B is better for lookup (+2.3 pp recall, +2.7 pp MRR) and worse for complex_reasoning; the pipeline already routes α and top-N per query type, so routing the embedder the same way is a natural extension. It costs a second dim-keyed table and a dual re-ingest, so measure before building.

The cheaper recall-widening levers noted at the time (`hnsw_ef_search` ≥ 300, a larger pre-rerank `top_n`) were framed as "try these on 4B before paying for 8B"; that framing is moot now that 8B is deployed, but both knobs remain untested against the current config.

## Query-side embedding instruction

Query-side embedding instruction (`query_instruction`, Qwen3-Embedding asymmetric prefix): admin-configurable string fed to the query-side embedding as `"Instruct: {instruction}\nQuery: {q}"` (see `ai.EncodeQuery`, `ai.BuildQueryInput`). Document embeddings stay bare — that's the trained asymmetric behavior. **Measured net-negative when paired with a calibrated cross-encoder**: on 2026-04-24 the clean A/B at `rerank_blend_alpha=0.5` + bge-v2-m3 showed that toggling `query_instruction` from "Given a question, retrieve all relevant passages that provide information to answer it" → empty added +7 pp enumeration recall, +2 pp lookup recall, and cost ~4 pp on complex_reasoning (net +0.8 pp overall recall with tied MRR/nDCG). Mechanism: the instruction narrows the embedding-side candidate pool toward "web-search-style" matches; that filtering helps a weak reranker (the embedding has to carry more ranking weight) and hurts a strong one (the reranker benefits from a broader pool). The same conclusion applies under the current jina-reranker-v3 + α=0.8 default — if anything more strongly, since a higher α relies even more on reranker discrimination over a broad embedding pool. Default is empty. Keep it empty while the reranker is calibrated; turn it on if you ever fall back to a deployment where reranker discrimination is weak. Only English instructions activate Qwen3-Embedding's trained task heads — German phrasings behave as no-instruction in internal measurements.

## Corrective RAG (CRAG)

Corrective RAG (CRAG): when `crag_enabled` is true (admin Agent panel, default off), the search pipeline grades each returned chunk via a small LLM (`crag_grader_model`, empty = KB default) as `relevant` / `ambiguous` / `irrelevant` in a single batched call. The chat layer (`chat.PrepareChatContext`) then dispatches on the **first round**: ≥`crag_min_relevant_chunks` (default 3) relevant → proceed; ≥1 relevant AND all relevant chunks come from a single file → proceed (single-doc shortcut, see below); ≥1 relevant across multiple files → rewrite the query via `ai.RewriteQuery` and retry once with grading; ≥1 ambiguous → proceed (fail-open — `ai.GradeRelevance` maps parse/LLM errors to `ambiguous`, so an all-ambiguous bucket is indistinguishable from a degraded grader and must never suppress the answer); only the genuine all-irrelevant / zero-chunks case → abstain (the grader affirmatively said the KB lacks it). The **single-doc shortcut** prevents a known antipattern: queries that target ONE document ("how does process X in Richtlinie Y work?") typically surface only 1-3 relevant chunks — all from that document — surrounded by many off-topic chunks. The count-only `min_relevant` threshold would fire a rewrite in that case, and the rewritten query loses the target document. Detecting that all relevant chunks share a FileID is a strong "retrieval hit the right place" signal. The **retry round** (after rewrite) follows the same fail-open rule: ≥1 relevant OR ≥1 ambiguous → proceed; only 0 relevant AND 0 ambiguous abstains. The retry search deliberately **propagates** the caller's `Enhance` setting (`rewrite` / `expand` / `spell`): dropping it on the "already-transformed" argument measurably hurt recall in practice — users relying on `expand` lost the synonym/variant boost that originally surfaced chunks the plain rewrite doesn't find. The theoretical double-transform cost is accepted for the recall it restores. When the pipeline abstains, the system prompt gets `ChatAbstainNotice` and `ChatContext.Abstain=true`. Decisions are surfaced as `rag_crag_decision_total{action="proceed|rewrite|abstain"}` and logged as `rag.crag.decide` with relevant/ambiguous/irrelevant counts; a rewrite-then-proceed increments both `rewrite` (round-1 outcome) and `proceed` (round-2 outcome) so operators can measure the rewrite path's hit rate. Latency budget: ~300–500 ms with a small grader model; rewrite branch adds a second search + grade round. Adaptive routing (`adaptive_routing_enabled`, default off): when enabled, the chat layer skips CRAG grading entirely for `lookup` and `enumeration` queries (lookup: one canonical answer chunk so per-chunk relevance grading is overkill and just adds latency; enumeration: the BM25-seeded extraction pre-pass already enforces completeness via the seed-guarantee post-processing, so per-chunk CRAG grading is redundant and risks dropping otherwise-recoverable chunks). CRAG continues to run for `complex_reasoning` and unclassified queries where the multi-source relevance check still earns its keep.

## Citation validator

Citation validator: when `citation_validation_enabled` is true (default off), every chat answer runs through a deterministic per-citation check after generation. For each `[N]` (or `[1, 2]`) marker, the validator builds the local sentence window (the marker's sentence ±1) and looks for content-token n-gram overlap (4-gram, falling back to 3-gram, lowercased + stopword-filtered) with the cited source. Multi-cite is "any source overlap = verified for all cited Ns". Out-of-range Ns (`[3]` when only 2 sources exist) get the explicit `out_of_range` reason. Runs in `runPostResponseTasks` parallel to the LLM-based factchecker; results merge into the existing `verification` JSONB blob as a `citations: [{n, verified, reason}]` array — no migration, no new SSE event. Frontend (`MessageContent.tsx`) renders suspect `[N]` with an amber dashed underline and ⚠ glyph plus a hover tooltip explaining the reason. Validator is pure CPU (no LLM call, no I/O), deterministic, and can produce false positives on heavily paraphrased correct answers — the UI treatment is intentionally cautious (warn, don't reject).

**Span verification (Wave 3, `chat_citation_spans_enabled`, default off).** The n-gram/semantic check answers "is this citation plausible?" but cannot point at *what* in the source supports it — the UI can only show the first few hundred characters of the cited chunk and let the reader hunt. The optional span pass closes that: after the deterministic validator, one fast-tier call copies **one verbatim quote** per still-eligible citation out of the cited source, and the backend then matches that quote against the source body itself — normalised (Unicode NFC, lower-cased, whitespace runs collapsed, quote characters and edge punctuation trimmed) but otherwise exact. Design point: the model supplies *text*, never offsets; the offsets are computed from the match, so a hallucinated or paraphrased quote simply fails to match and the entry keeps its previous `method` untouched. On a match the entry becomes `method: "span"` with `span: {start, end}` — **rune** offsets (code points, `end` exclusive) into `sources[n-1].content`, a different domain from `internal/chat/citation_spans.go`'s byte offsets of the `[N]` marker inside the *answer*. NFC is applied through `norm.Iter` segment-wise, so a decomposed `u`+U+0308 in OCR'd content still matches a precomposed `ü` in the quote and still maps back to the correct original rune range. Summary and `community_summary` sources are excluded (a RAPTOR or community summary is not verbatim source text). Fail-soft in every direction: timeout, transport error, unparseable reply or malformed span all leave the deterministic verdict in place, and the frontend falls back from the highlight to the plain snippet. Cost: one extra fast-tier call per answer with at least one eligible citation, bounded by `chat_citation_spans_timeout_ms` (8000 ms default) — post-response work is synchronous, which is why the budget exists. The pass runs *before* the attribution metrics, so `span` appears as its own `method` label on `rag_citation_attributions_total` rather than being folded into `none`. Enablement block: `docs/feature-recipes.md` §"Span-verified citations". `internal/chat/citation_spans_verify.go`.

**Measuring answer-side quality.** The validator answers "is this citation grounded?", not "is this answer good?". For the latter, `cmd/eval --judge` grades faithfulness, answer relevance and context precision — and, since Wave 4, **coverage** against a golden row's optional `expected_points` (`mean_coverage` + `coverage_n`). On long-form synthesis answers the Likert answer-relevance judge saturates and is useless as a decision input; use coverage plus the offline **pairwise preference judge** (`--pairwise-a` / `--pairwise-b`, both orders, agreement required) instead, and always run a same-configuration control pair so the tie rate tells you the judge's discriminative power at that margin. Judge parsing is tolerant since Wave 4 (numeric-string scores clamped, mismatched boolean lists truncated/padded with a `judge_warnings` entry), so `n` per metric can be higher than in pre-Wave-4 runs even though well-formed responses score identically — the per-metric `*_n` counts in the aggregate are what make that visible. Both instruments are documented in `eval/golden/README.md` §§"Pairwise judge" and "Coverage judge".

## Conflict / supersession surfacing (post-retrieval, Wave 5)

`chat_conflict_surfacing_enabled` (default **off**, per-KB) adds one structured fast-tier call *after* the final chunk set is assembled, comparing the turn's own numbered sources for contradictions and supersessions and deciding direction from each source's date line. It is listed here only to record what it does **not** do: it is strictly post-retrieval, and the Wave-5 measurement demonstrates that directly — the flag-on and flag-off runs over the same 25 CERT questions are identical to three decimals on recall (0.668), precision (0.234), MRR (0.677) and nDCG (0.704). The only retrieval-side movement between two *same-flag* runs is the usual CRAG-grader non-determinism, and the on-vs-off delta sits inside that band.

The feature stays **off**: both pre-stated gates failed (0 of 8 CERT pairs flagged — a fixture property, since MMR never assembles both halves of the queried pair; 0.124 false-positive flag rate on the PPM set against a ≤ 0.10 bar, and that rate is an **upper bound**: it was measured before the same-file/mirrored-duplicate detector fix that ships in this release, and re-measuring the fixed detector is a roadmap item). Mechanism, wire shape and the full numbers: `docs/agent-orchestration.md` and `docs/feature-recipes.md` §"Conflict / supersession surfacing"; record in `eval/golden/cert-recency-de.acceptance.md`.

## Contextual Retrieval (Anthropic-style)

Contextual Retrieval (Anthropic-style): when `contextual_enrichment` is enabled (default), the ingestion pipeline asks an LLM to write a 1-sentence context prefix per chunk (e.g. *"This passage discusses §3 Kündigungsfrist of the Müller GmbH contract"*). Each per-chunk LLM call sends the **full source document** as the user-message prefix (truncated to ~8k cl100k_base tokens via `splitter.CountTokens`); the chunk goes last in `<chunk>...</chunk>` tags so OpenAI-compatible automatic prompt caching (OpenAI ≥1024-tok prefix, DeepSeek auto, vLLM with prefix caching, …) reuses the document prefix across every chunk of the same file — Anthropic's cookbook quotes ~90 % cost reduction at typical chunk counts. Document and chunk bodies are interpolated verbatim (not XML-escaped): escaping degraded the enrichment LLM's view of technical content (code, URLs, quoted strings), which poisoned the generated prefix and measurably hurt retrieval. The filename **is** escaped because it sits inside the `name="..."` attribute where a stray quote would actually break the wrapper. The system prompt explicitly instructs the LLM to mention named persons together with their role/title as stated in the document (e.g. "CIO Eberhard Kurz") and forbids inventing roles. This was added 2026-05-08 (Phase 2 of the post-Octen-8B roadmap) after a Q085 diagnostic against the dev KB showed only 2 of 20 chunks containing "Eberhard Kurz" had any role mention in the prefix; person-role lookup queries previously matched only the ~10% of chunks where the prefix happened to capture the role. Existing chunks keep their old prefixes until re-ingested.

## Contextual prefix at chat time

Contextual prefix at chat time: when `PrepareChatContext` assembles the LLM prompt, each retrieved chunk's `contextual_prefix` (when present) is emitted above the chunk body as a `Context:` line alongside the `[N] [Source: …]` annotation. Without this, structural chunks (team lists, attribute tables) whose identifying information lives in the surrounding document — not inside the chunk itself — reach the LLM as anonymous content. The cited-in-answer failure mode was reproducible: named-entity queries over templated project profiles surfaced all four matching files' team-list chunks as sources (verified via `bm25_floor_reinserted` and the sources panel) but the LLM only mentioned two projects in prose because it couldn't link the other two team lists to a project name. Surfacing the enrichment prefix repairs that link. Prefix is stripped from the UI-facing `MessageSource` (users still see original content only).

## Enumeration pre-pass

Enumeration pre-pass (two-pass pipeline for list-style queries): when the query is classified as enumeration-intent (German "In welchen…", "Welche…", "Wer…", "Alle…", "Liste/Nenne/Aufzählung…" and English equivalents; see `chat.IsEnumerationQuery`), `PrepareChatContext` runs a deterministic structured-extraction LLM call before building the final prompt. The extraction uses vLLM/OpenAI Structured Outputs (`response_format: {type: "json_schema", strict: true}`, falls back to `json_object` on older backends) to produce a guaranteed-valid JSON array of matches: `[{source_idx, entity, role_or_detail, quote}]`. That array is then embedded into the system prompt via `prompts.EnumerationVerifiedMatchesAddendum` — a hard completeness contract the prose pass must satisfy. Cost: one extra LLM call at ~2-3 s on a local Gemma-class endpoint. Payoff: verified by the Chroma 2025 "Context Rot" study and reproducible on this KB — mid-range self-hosted models (Gemma 4, Qwen3-class) reliably drop entries when asked to produce prose enumerations over 10-30 semantically similar chunks; the per-chunk yes/no decision that structured extraction requires is far more consistent at that model scale. Fail-open: on extraction error the chat continues with the pre-enumeration system prompt unchanged. Logged as `rag.enumeration.extracted` (LLM timing + match count) and `rag.enumeration.decision` (classification outcome + match count + context size).

## BM25-seeded extraction with deterministic post-processing

BM25-seeded extraction with deterministic post-processing: the distinct file IDs BM25 returned (`SearchResult.KeywordMatchFileIDs`, in BM25-rank order) are mapped to their 1-based source indices in the final chunk list and passed to the extraction call as "text-match candidates" plus a parallel `SeedMeta` map (file name + chunk content per candidate). After Gemma's LLM output returns, three deterministic post-processing stages run in `ai.ExtractEnumerationMatches` before the list reaches the prose pass:

1. **Candidate-only filter** — drop any match whose `source_idx` is not in the BM25 seed list. Gemma treats the "consider only these blocks" instruction as a preference and reliably adds 1-2 non-candidate matches anyway; these are the main source of false positives on queries where the entity's name appears in multiple roles (point of contact, external mention, boilerplate cross-reference).
2. **Dedup by `source_idx`** — Gemma occasionally emits the same chunk twice under different entity/role framings. Keep the first.
3. **Seed guarantee** — every BM25 seed MUST end up in the result. For seeds Gemma dropped (observed 1-3 per query on Gemma-31B when the model is in a conservative mood), synthesize a match using `SeedMeta` (file name as entity, first 150 chars of chunk as quote). The prose pass still gets to drop the match if the quote contradicts the query criterion.

This layering accepts that Gemma-31B is unreliable at nuanced per-chunk judgment (oscillates between over- and under-include across identical runs) and replaces that unreliability with BM25's deterministic recall floor. Gemma's job reduces to providing high-quality quotes when it can; the pipeline guarantees completeness regardless. Logged as `non_candidate_dropped`, `duplicate_dropped`, `seed_synthesized` in the extraction event. Cache-hit telemetry (OpenAI `prompt_tokens_details.cached_tokens`, DeepSeek `prompt_cache_hit_tokens`) is logged as `ai.contextual_prefix.cache_hit` when the provider reports a non-zero hit. The prefix is stored in its own column `contextual_prefix` (migration `0008`), folded into the embedding input as `prefix + "\n\n" + content`, and folded into the BM25 `tsvector` as `to_tsvector(<lang>, COALESCE(contextual_prefix, '') || ' ' || content)`. Display still uses the original `content` only. Cross-file dedup hashes only the original content so it stays deterministic. Optional model override via `contextual_enrichment_model` (empty = KB default chat model).

## Context truncation

Context truncation: when retrieved chunks exceed the LLM token budget, the highest-scored chunks are kept (real tiktoken counts via cl100k_base; survivors retain original ordering for downstream sandwich layout).

## ECoRAG evidentiality compression

ECoRAG evidentiality compression (`chat_context_compression_enabled`, default off; `_min_chunks` default 15; `_threshold` default 0.3; `_model` falls through to `model_tier_fast`): one fast-tier LLM call between reranking and prompt assembly scores every post-rerank chunk on whether it provides DIRECT evidence to answer the query (distinct from topical relevance, which the reranker already captured). Chunks scoring below threshold drop before prompt assembly. **Why:** the reranker optimises for topical-similarity-to-query, which is necessary but not sufficient — a chunk can be highly topical and still not contain the specific evidence needed to answer. The evidentiality classifier closes that gap by asking a yes/no question the reranker isn't trained to answer.

A defensive "never drop everything" fallback restores the pre-filter pool if the classifier surfaces zero evidence (handles e.g. classifier hallucination + edge-case prompts). Skipped under T2-1 long-context mode (would re-narrow the wide pool). Runs between `SandwichOrder` and prompt assembly. Cost: one fast-tier LLM call per matching turn. Telemetry: `rag_context_compression_decision_total{outcome}`, `rag_context_compression_dropped`.

## Late chunking (Jina-style)

Late chunking (`late_chunking_enabled`, default off; `late_chunking_max_input_tokens` default 8192): ingestion-side only — no DB migration (vector dim unchanged). When on, each file's chunks are embedded in one call (per window) with `late_chunking: true`; the provider concatenates inputs, encodes once with a long-context model, and returns one mean-pooled vector per chunk that carries cross-chunk document context. **Why:** standard chunking embeds each chunk in isolation, so a chunk that says "the same applies to X" loses the antecedent of "the same" at embedding time. Late chunking pushes attention across the full document before pooling, so the vector captures the context the chunk was written within.

The embedding **cache is bypassed** on this path because late-chunked vectors depend on document context (the `(model, text)` cache key would conflate different files). Re-ingest existing files to benefit.

**Orthogonal to `contextual_enrichment`:** enrichment can stay on, the LLM prefix is still stored on the chunk row and feeds BM25 (the per-row tsvector COALESCEs `contextual_prefix` into the input — see `internal/vector/chunks.go`) and chat-time prompts, but is **not** concatenated into the late-chunked embedding input (concatenating would corrupt the document's natural attention flow). When both flags are off, behaviour is unchanged.

**Provider compatibility:** most OpenAI-compatible servers silently ignore the `late_chunking` field and return standard embeddings. Verify by inspecting the request body or checking the provider's docs before flipping the flag in production. Jina's `/v1/embeddings` is the reference implementation.

## RAPTOR hierarchical indexing

RAPTOR hierarchical indexing (`raptor_enabled`, default off; `raptor_min_chunks` default 25; `raptor_max_levels` default 4; `raptor_branching_factor` default 5; `raptor_summary_model` falls through to `model_tier_fast` → KB chat model; `raptor_clustering_algorithm` default `kmeans`; `raptor_leiden_resolution` default 1.0): per-file hierarchical summary tree built at ingest. Mutually exclusive with `parent_child_enabled` (skipped at ingest if both on). Migration **0046** required (`node_kind`, `tree_level`, `raptor_parent_id` columns).

**Why:** a single chunk can answer a fact question but not a synthesis question that spans the whole file. RAPTOR builds level-1 summaries over chunk clusters, level-2 summaries over level-1 clusters, etc., up to `_max_levels`. At retrieval time, leaves and summaries compete in the unchanged BM25 + vector + reranker pool — the right level surfaces naturally per query (lookup queries pull leaves, synthesis queries pull summaries). Zero query-time cost beyond the larger index size.

Builder runs after the chunk loop and after KG extraction in `ProcessFile`. Cost is LLM-bound at ingest: ≈ `ceil(N/5)` summary calls per level under K-means at `branching=5`, ~31% extra rows on a 1000-chunk file. **Citation validation:** the validator resolves summary citations against their descendant leaves via a recursive CTE on `raptor_parent_id` (a summary "supports" a claim if any of its descendant leaves does). Eval ablation: `./cmd/eval/eval --node-kind leaf` vs `summary` vs `""` (both). Telemetry: `rag_raptor_*` (`build_total`, `build_seconds`, `tree_depth`, `llm_calls_total`, `retrieved_total{node_kind}`, `cited_total{node_kind}`). To backfill trees on an existing KB, re-ingest each file (RAPTOR runs only on the fresh ingest path; lazy-build is a follow-up).

**T2-2 Leiden clustering** (`raptor_clustering_algorithm=leiden`): per-level clustering uses modularity-based community detection over a k-NN cosine-similarity graph instead of fixed-k K-means. Cluster count is determined by graph topology, not pre-specified — heterogeneous documents yield more, tighter clusters; homogeneous ones yield fewer, larger ones. `raptor_branching_factor` is ignored on this path. Cited Frontiers 2025 paper: +20 % QuALITY accuracy on heterogeneous documents. Implementation in `internal/processor/raptor/leiden.go` (single-level Louvain-style optimisation; the recursive RAPTOR tree already provides the multi-level structure).

## Quoted-phrase boosting in keyword search

Quoted-phrase boosting in keyword search: substrings inside `"..."` are required exact-phrase matches via `phraseto_tsquery`, ANDed with the rest of the query. Useful for legal citations (`"§323c StGB"`), code identifiers (`"gpt-4o"`), and exact terminology — falls back to current `websearch_to_tsquery` behaviour when no quotes are present.

## RAG-Fusion (per-alt-query BM25)

Per-alt-query BM25 (P4): when `rag_fusion_enabled` is on, every alternative-phrasing query the multi-query / step-back / sub-queries paths emit gets a BM25 keyword search in addition to its existing vector search. The resulting ranked lists fold into RRF as separate keyword-tier lists — `assembleRRFWeights` applies `rrf_weight_bm25` to them, while the existing per-alt vector lists keep `rrf_weight_vector`. Default off because flipping it shifts the BM25/vector ratio in the RRF pool; operators re-tune the two weight knobs against the golden set after enabling. Cost per chat turn: one extra BM25 query per alt phrasing (~1–5 ms each), capped by the upstream `GenerateMultiQueriesWithModel` count (typically 3). Failures are best-effort like the existing multi-query path: a failed BM25 call drops that one list and continues.

## Keyword arm scoring: ts_rank vs BM25 (2026-09)

`bm25_scoring_mode` (`ts_rank` | `bm25`, default `ts_rank`) selects how the keyword arm scores the shared WHERE-clause candidate set (`buildKeywordCandidateClause` — phrase/email extraction, websearch/OR-tokens/proper-noun composition — is computed exactly once and spliced verbatim into either scoring variant, so the candidate *set* never differs between modes; only the ranking does). `ts_rank` is Postgres's built-in two-argument `ts_rank()` (term frequency only, no IDF, default normalisation 0 — no document-length or cover-density/proximity weighting). `bm25` computes Okapi BM25 (`ln(1 + (N−n+0.5)/(n+0.5)) · Σ (tf·(k1+1)) / (tf + k1·(1−b+b·dl/avgdl))`) per arm (`lang`/`simple`) from per-KB, per-dimension stats tables (`bm25_kb_stats_<dim>`, `bm25_term_stats_<dim>`) that the worker's `bm25_stats_refresh` maintenance loop recomputes every 15 minutes (also `cmd/eval --refresh-bm25-stats`, which additionally creates the stats tables idempotently if a dev DB predates them). `dl` (document length) and `avgdl` are both `length(tsvector)` — **distinct lexeme count**, not raw token count (W2-R4) — computed identically by the refresher and at query time, so no new chunk column or re-ingest is needed. `k1`/`b` defaults 1.2/0.75, clamped 0.5–3.0 / 0–1. A KB/dimension with no stats row falls back to `ts_rank` per-query and increments `rag_bm25_mode_fallback_total{reason="no_stats"}` (fail-soft; never an error).

**Tokeniser-divergence caveat**: the WHERE-clause candidate floor (`buildOrTokensExpr`) and `to_tsvector` (which `bm25`'s `qlex` CTE independently re-tokenises the raw query text through — see `keyword_sql.go`'s `bm25ArmCTE`) can segment the same input differently — e.g. `Stud.IP` may pass the OR-token floor as a unit but split into `stud` + `ip` under `to_tsvector`. A candidate admitted by the floor can therefore score exactly 0 under `bm25` (its independent re-tokenisation finds no matching lexeme in the candidate's `tsvector`) while `ts_rank` still gives it a small positive score — it scores directly off the same composed tsquery the floor already admitted the candidate under (term-frequency only; `ts_rank`'s default normalisation is 0, so this is not cover-density/proximity weighting, just reusing a match the floor already found) rather than re-tokenising the query from scratch. This is a scoring-only edge case (the candidate is never dropped, RRF still sees it in the keyword-tier list, just at rank-anything-goes rather than a meaningful position).

**Grid** (production fixture `eval/golden/production-ppm-2026-08.jsonl`, PPM-Eval KB, dim 4096, 1815 chunks, 89 questions, `--production-context --top-k 10`, no judge; stats pre-refreshed: lang 16,787 terms / simple 20,642 terms):

| Cell | Mode | Tiered boost | Overall recall | Overall MRR | Overall nDCG | Lookup recall | Lookup MRR | Lookup nDCG | Complex recall | Complex MRR | Complex nDCG | Enum recall | Enum MRR | Enum nDCG | Wall time |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| A (baseline) | ts_rank | off | 0.802 | 0.891 | 0.895 | 0.858 | 0.872 | 0.875 | 0.701 | 0.870 | 0.875 | 0.860 | 1.000 | 1.000 | 21m22s |
| A2 (noise repeat) | ts_rank | off | 0.816 | 0.904 | 0.906 | 0.887 | 0.895 | 0.896 | 0.701 | 0.875 | 0.879 | 0.860 | 1.000 | 1.000 | 21m08s |
| B | ts_rank | on | 0.786 | 0.857 | 0.860 | 0.835 | 0.849 | 0.852 | 0.687 | 0.820 | 0.821 | 0.860 | 0.964 | 0.974 | 20m30s |
| C | bm25 | off | 0.838 | 0.877 | 0.887 | 0.909 | 0.895 | 0.904 | 0.702 | 0.800 | 0.819 | 0.932 | 1.000 | 0.994 | 20m21s |
| D | bm25 | on | 0.832 | 0.886 | 0.898 | 0.912 | 0.913 | 0.923 | 0.711 | 0.799 | 0.820 | 0.860 | 1.000 | 1.000 | 20m13s |

**Delta vs A (percentage points)**:

| Cell | Overall recall | Overall MRR | Lookup recall | Lookup MRR | Complex recall | Complex MRR | Enum recall | Enum MRR |
|---|---|---|---|---|---|---|---|---|
| A2 (noise band) | +1.4 | +1.3 | +2.9 | +2.3 | +0.0 | +0.5 | +0.0 | +0.0 |
| B | −1.6 | −3.5 REG | −2.3 REG | −2.3 | −1.4 | −4.9 REG | +0.0 | −3.6 REG |
| C | +3.6 | −1.4 | +5.1 | +2.3 | +0.1 | −7.0 REG | +7.1 | +0.0 |
| D | +3.0 | −0.6 | +5.4 | +4.1 | +1.0 | −7.0 REG | +0.0 | +0.0 |

(REG = `cmd/eval --baseline`'s own regression gate tripped on that route+metric, default thresholds recall 2pp / MRR 3pp; all three of B/C/D runs exited 3 — B on four independent route+metric trips, C and D each on one, `complex_reasoning.mrr`.)

**Noise band** (A2 vs A, single repeat — no variance estimate beyond this): overall recall 1.4pp / MRR 1.3pp; lookup recall 2.9pp / MRR 2.3pp; complex_reasoning recall 0.0pp / MRR 0.5pp; enumeration 0.0pp both metrics. The lookup-MRR noise band (2.3pp) is what the decision rule below tests against.

**`keyword_mode` evidence**: every `rag.search.stages` log line in the C and D runs carries `"keyword_mode":"bm25"` (confirmed at the first, middle, and last occurrence of each run); `rag_bm25_mode_fallback_total` never fired (grep count 0 in both logs) — the KB's stats rows were present and fresh throughout, so bm25 mode ran for real on every query, not via fallback.

**Latency**: no visible per-search latency difference between modes at this KB size — mean `rag.search` `duration_ms` across all in-run searches: A 8327ms, A2 8316ms, B 8268ms, C 8115ms, D 8101ms (n≈123–129 searches per run; differences are within noise). Total wall time was also flat across cells (20m13s–21m22s for 89 questions). The Task-6 review's O(candidates×lexemes) concern about the `bm25` scoring CTEs did not manifest as a measurable latency regression at 1815 chunks / ~120 keyword candidates per query — worth re-checking at a KB an order of magnitude larger.

**Tiered boost interaction**: `bm25_tiered_boost_enabled` was designed as an IDF proxy for `ts_rank` (a coarse CASE-based multiplier approximating "rare term → bigger boost" without real corpus statistics). With real BM25 scoring already carrying an IDF term, the boost stacks on top rather than substituting for it — cell D (bm25 + boost) doesn't clearly beat cell C (bm25, no boost) on lookup MRR (0.913 vs 0.895) or complex_reasoning (both regress by the same ~7pp), and cell B (ts_rank + boost) regresses on every route relative to A (ts_rank, no boost) by more than the noise band, including a −3.5pp overall MRR regression. The boost's IDF-proxy rationale weakens once real IDF is available; it is not a clean win in either mode on this fixture.

**`bm25_tiered_boost_enabled` is deprecated (2026-09, Wave 3 Task 7).** The A/B above is the whole case: under `ts_rank` it regressed every route beyond the noise band (cell B), and under `bm25` it added nothing the real IDF term doesn't already provide (cell D vs C). No deployment should turn it on, and the admin-UI help text now says so (`Veraltet — im 2026-09 A/B auf allen Routen negativ; Kandidat für Entfernung. Standardmäßig aus.`). The key, its `site_config` entry and `buildBoostExpr` **stay in place for now** — removal is its own release (a deployment that has it on would silently change ranking on upgrade, and `--bm25-tiered-boost on|off` is still the way to reproduce the measurement above). Treat it as frozen: no new tuning, no new call sites.

**Decision**: the brief's literal rule — *the default stays `ts_rank` unless C or D beats A on lookup MRR by more than the noise band on that metric, without losing recall on any route by more than the noise band* — evaluates as follows. C's lookup-MRR delta (+2.3pp) equals the noise band exactly, not more than it, so **C does not clear the bar**. D's lookup-MRR delta (+4.1pp) exceeds the noise band (2.3pp), and D loses recall on no route (complex_reasoning +1.0pp, enumeration +0.0pp, lookup +5.4pp — all ≥0) — **by the letter of the rule, D clears the bar**.

However, this recall-only guard misses a real and consistent side effect: both C and D regress `complex_reasoning` MRR by ~7.0pp — roughly 14× that route's own 0.5pp noise band, and more than double `cmd/eval`'s own default MRR regression threshold (3pp), tripping the regression gate (exit 3) on every C/D/B run. Recall is essentially unchanged on that route (+0.1pp / +1.0pp), so the right chunks are still retrieved — they just rank lower after CRAG's multi-round grading and the RRF fusion. The most likely cause is scale mismatch: `rrf_weight_bm25` (currently 1, tuned against `ts_rank`'s output range) is applied unchanged to `bm25`'s IDF·TF-saturation scores, which live on a different numeric scale, especially once multiple sub-query lists get fused for `complex_reasoning`'s plan-execute path.

**This wave keeps the default at `ts_rank`** (no code or config change lands in this task, per the global constraints — Task 9 owns flipping any default). The literal per-metric rule favors flipping to `bm25` (uncapped, no tiered boost), but this doc recommends Task 9 treat that reading with the complex_reasoning caveat squarely in view rather than flip on the lookup-MRR number alone — either re-tune `rrf_weight_bm25` for the `bm25` scale range first, or land the flip with an explicit note that complex_reasoning ranking quality is accepted as a known regression pending that re-tune. **Read this paragraph together with the Wave-3 retune grid below, which measured −0.3 pp on that route at the same weights — but with orchestrator dispatch OFF, i.e. on the standard path only, whereas this A/B ran with dispatch ON and routed `complex_reasoning` through plan-execute. The two are not the same experiment: the ~7 pp is a plan-execute finding, now measured directly on that path by the "Dispatch-on rerun" below — complex_reasoning MRR under bm25 comes out −4.7 pp vs ts_rank against a 2.6 pp band, confirming the direction at roughly two-thirds the original magnitude; the scale-mismatch explanation remains a plausible but untested mechanism.**

### Retune grid: `rrf_weight_bm25` × α under `bm25` (Wave 3 Task 7, 2026-09-06)

The Wave-2 follow-up above — *"re-run the α-grid under the mode that ships as
the default"* — ran as a full grid: baseline **A** (`ts_rank`, live weights),
its same-flag repeat **A2**, then `bm25` × `rrf_weight_bm25` ∈ {0.5, 0.75, 1.0}
× `rerank_blend_alpha` ∈ {0.6, 0.8}. Same fixture and settings as the Wave-2
table (89 questions, k=10, `--production-context --orchestrator-dispatch=false`,
`--refresh-bm25-stats` on every cell, no judge, no `site_configs` mutation —
every knob is a `cmd/eval` overlay flag). Full record, including the
per-cell evidence that `bm25` mode actually ran (93–96 searches per cell with
`"keyword_mode":"bm25"`, zero `rag_bm25_mode_fallback_total` events):
`eval/golden/bm25-retune.acceptance.md`.

| Cell | Mode | w<sub>bm25</sub> | α | overall R/MRR/nDCG | lookup R/MRR/nDCG | enumeration R/MRR/nDCG | complex R/MRR/nDCG | wall |
|---|---|---|---|---|---|---|---|---|
| **A** (baseline) | ts_rank | 1.0 | 0.8 | 0.828/0.906/0.906 | 0.887/0.895/0.898 | 0.864/1.000/1.000 | 0.733/0.878/0.876 | 16m49s |
| **A2** (noise repeat) | ts_rank | 1.0 | 0.8 | 0.806/0.888/0.888 | 0.841/0.849/0.852 | 0.864/1.000/1.000 | 0.733/0.891/0.887 | 17m39s |
| C1 | bm25 | 0.5 | 0.6 | 0.856/0.916/0.918 | 0.909/0.895/0.904 | 0.899/1.000/1.000 | 0.765/0.906/0.902 | 16m29s |
| C2 | bm25 | 0.5 | 0.8 | 0.850/0.916/0.919 | 0.909/0.895/0.904 | 0.899/1.000/1.000 | 0.749/0.906/0.902 | 16m38s |
| C3 | bm25 | 0.75 | 0.6 | 0.828/0.899/0.900 | 0.886/0.884/0.890 | 0.903/1.000/0.993 | 0.718/0.875/0.872 | 16m18s |
| C4 | bm25 | 0.75 | 0.8 | 0.844/0.904/0.908 | 0.886/0.872/0.881 | 0.899/1.000/1.000 | 0.764/0.906/0.902 | 16m44s |
| C5 | bm25 | 1.0 | 0.6 | 0.846/0.904/0.907 | 0.909/0.895/0.904 | 0.899/1.000/1.000 | 0.738/0.875/0.869 | 16m34s |
| C6 | bm25 | 1.0 | 0.8 | 0.843/0.899/0.900 | 0.886/0.884/0.890 | 0.935/1.000/0.994 | 0.744/0.875/0.871 | 16m44s |

**Deltas vs A** (percentage points, recall / MRR):

| Cell | overall ΔR | overall ΔMRR | lookup ΔR | lookup ΔMRR | enum ΔR | enum ΔMRR | complex ΔR | complex ΔMRR |
|---|---|---|---|---|---|---|---|---|
| A2 (**noise band**) | −2.2 | −1.8 | −4.7 | −4.7 | +0.0 | +0.0 | +0.0 | +1.2 |
| C1 (0.5 / 0.6) | +2.8 | +1.0 | +2.2 | +0.0 | +3.6 | +0.0 | +3.2 | +2.8 |
| C2 (0.5 / 0.8) | +2.2 | +1.0 | +2.2 | +0.0 | +3.6 | +0.0 | +1.6 | +2.8 |
| C3 (0.75 / 0.6) | +0.0 | −0.7 | −0.1 | −1.2 | +3.9 | +0.0 | −1.5 | −0.3 |
| C4 (0.75 / 0.8) | +1.6 | −0.1 | −0.1 | −2.3 | +3.6 | +0.0 | +3.1 | +2.8 |
| C5 (1.0 / 0.6) | +1.8 | −0.1 | +2.2 | +0.0 | +3.6 | +0.0 | +0.5 | −0.3 |
| C6 (1.0 / 0.8) | +1.5 | −0.7 | −0.1 | −1.2 | +7.1 | +0.0 | +1.1 | −0.3 |

**Decision: no cell wins; the default stays `ts_rank` and no weight/α default
changes.** W3-R13's rule is *"flip only if a cell beats the baseline on lookup
MRR beyond the noise band **and** loses no route's recall or MRR beyond
noise"*. The second half is satisfied by four of the six cells — but the first
half fails everywhere, and not narrowly: **no `bm25` cell moved lookup MRR at
all** (best delta +0.0 pp, worst −2.3 pp) against a lookup-MRR noise band of
**4.7 pp**.

**The noise band is the headline of this run.** A2 is a byte-identical repeat
of A, and it came out 4.7 pp lower on lookup recall *and* lookup MRR — enough
to trip `cmd/eval --baseline`'s own default gate (exit 3) on three route+metric
pairs. Every one of the six `bm25` cells exited **0** against the same gate.
Two consequences:

1. **Wave 2's headline regression does not appear on the standard path — but
   this grid did not test the path it was measured on.** The two runs are not
   the same experiment: Wave 2's A/B ran with orchestrator dispatch at its
   **default (on)**, which routed 27–29 of the 89 questions — including the
   whole `complex_reasoning` route — through **plan-execute**; this grid ran
   `--orchestrator-dispatch=false`, i.e. the standard `PrepareChatContext`
   path only. So cell C6 is *not* a rerun of Wave-2's cell C, and the −7 pp is
   neither reproduced nor refuted here. What this grid supports is narrower
   and still useful: **at identical weights (1.0 / α 0.8), `bm25` costs −0.3 pp
   of `complex_reasoning` MRR on the standard path**, inside that route's
   1.2 pp band. Wave 2's causal story ("`rrf_weight_bm25` is off-scale for
   `bm25`") is a mechanism no experiment has probed directly — the Wave-4
   rerun below measures the *effect* on the plan-execute path, not the
   mechanism — and it gains no support here: down-weighting BM25 does not monotonically improve
   complex_reasoning on the standard path (C1/C2/C4 +2.8 pp, C3/C5/C6 −0.3 pp,
   across all three weights). **Measured directly (Wave 4 Task 6, see
   "Dispatch-on rerun" below):** rerunning with
   `--orchestrator-dispatch=true` (dispatch on, 3 repeats/cell) puts
   complex_reasoning MRR under bm25 at 0.807 ± 2.1 pp vs ts_rank
   0.854 ± 2.6 pp — a −4.7 pp difference against a 2.6 pp band, confirming
   the Wave-2 −7 pp in direction on the plan-execute path, at a smaller
   magnitude.
2. **This fixture cannot resolve effects below ~5 pp on `lookup` with one run
   per cell.** CRAG is enabled on the PPM-Eval KB, so an LLM call sits inside
   the retrieval path of every question — that, not the keyword arm, is the
   most likely dominant variance source. Any future retune of these weights
   needs repeated runs per cell (or CRAG forced off, `--crag off`) before a
   3–5 pp effect means anything.

**What the grid does show** is that `bm25` **lifts recall in most cells, but
not universally**: enumeration recall is up in all six (+3.6 to +7.1 pp) and
overall recall is up or flat in all six (+0.0 to +2.8 pp), but lookup recall
is −0.1 pp in three cells, and **C3 (0.75 / 0.6) loses 1.5 pp of
`complex_reasoning` recall** — the one route-recall loss in the grid, and the
reason C3 is flagged as a rule violation on the second half of W3-R13 as well
as the first. Ranking (MRR) is flat to slightly better everywhere except C4's
lookup (−2.3 pp). The recall direction agrees with Wave 2's; the magnitude is
smaller and the exceptions are real.

**Documented operating point** (for an operator who opts a KB into `bm25`, not
a new default and explicitly **not** a change for `ts_rank` deployments, whose
weights stay at 1.0/1.0 and α 0.8): **`rrf_weight_bm25 = 0.5`, α unchanged at
`0.8`** — cell C2, the best-behaved cell that keeps the live α, with the
largest overall MRR (+1.0 pp) and no route below baseline on either metric.
C1 (the same weight at α 0.6) is equivalent within noise; nothing in this grid
justifies moving α. Re-run `--refresh-bm25-stats` and bump
`queryCacheSchemaVersion` when flipping the mode, per the mode's own note
above, and read the cost check below first if the KB is large.

### Dispatch-on rerun (Wave 4 Task 6, 2026-09-06)

Re-ran the ts_rank-vs-bm25 comparison with `--orchestrator-dispatch=true`
(dispatch default ON, the setting Wave 2's A/B actually used — the retune
grid above ran with dispatch off), 3 repeats per cell: A (ts_rank) ×3, C
(bm25, default weights) ×3, C5 (bm25 + `--rrf-weight-bm25 0.5`) ×3, same
fixture/args as the retune grid otherwise (`--production-context --top-k 10
--bm25-tiered-boost off --refresh-bm25-stats`). 25–29 of 89 questions routed
through `plan_execute` per run (dispatch classification is itself
non-deterministic; every one of the 9 runs landed in that range). Noise band
= max spread across the three A runs per route/metric, floored at 1.0 pp.

| route | metric | A (ts_rank) | C (bm25) | C5 (bm25, w=0.5) | band |
|---|---|---|---|---|---|
| overall | recall | 0.802 ± 0.1 | 0.827 ± 0.0 | 0.818 ± 2.6 | 1.0 |
| overall | MRR | 0.889 ± 1.0 | 0.874 ± 0.7 | 0.859 ± 1.7 | 1.0 |
| lookup | recall | 0.865 ± 1.9 | 0.886 ± 0.0 | 0.877 ± 5.1 | 1.9 |
| lookup | MRR | 0.880 ± 2.3 | 0.884 ± 0.0 | 0.872 ± 3.5 | 2.3 |
| enumeration | recall | 0.860 ± 0.0 | 0.932 ± 0.0 | 0.896 ± 0.0 | 1.0 |
| complex_reasoning | recall | 0.691 ± 2.5 | 0.702 ± 0.0 | 0.705 ± 0.4 | 2.5 |
| complex_reasoning | MRR | 0.854 ± 2.6 | 0.807 ± 2.1 | 0.801 ± 0.0 | 2.6 |

(n=3 per cell; means ± max-spread across the 3 repeats, in percentage
points; nDCG and the full per-question breakdown are in
`eval/golden/bm25-retune.acceptance.md` §"Dispatch-on rerun (Wave 4,
2026-09-06)".)

Neither `bm25` cell clears W4-R8's bar: lookup MRR gain stays inside the
band for both (C +0.4 pp, C5 −0.8 pp, vs a 2.3 pp band), so the flip
condition never triggers — and both cells also lose beyond their bands
elsewhere (overall MRR −1.5/−3.0 pp, complex_reasoning MRR −4.7/−5.2 pp).
**`bm25_scoring_mode` stays `ts_rank`** by the letter of the rule, without
even needing the recall-loss guard to decide it.

This settles the Wave-2 claim: on the plan-execute path (dispatch on),
complex_reasoning MRR under bm25 is 0.807 ± 2.1 pp (n=3) vs ts_rank
0.854 ± 2.6 pp (n=3) — a −4.7 pp difference against a 2.6 pp band. The
Wave-2 −7.0 pp is **confirmed in direction** (the loss is real and exceeds
the band) but not in full magnitude (−4.7 pp measured here vs −7.0 pp on
Wave-2's single run). Five questions, all `complex_reasoning`, account for
most of the swing (ranked by |Δ mean reciprocal rank| between the 3-repeat
A-mean and the 3-repeat C-mean): Q058 (Stanford/Oxford AI-services
question, RR 1.000→0.111, Δ 0.889), Q025 (Δ 0.500), Q023 (Δ 0.444), Q056
(the one question that moves in bm25's favor, RR 0.667→1.000, Δ 0.333),
Q029 (Δ 0.153) — full table and the script that produced it in
`eval/golden/bm25-retune.acceptance.md`. `bm25` still lifts recall (lookup
+2.1 pp beyond its 1.9 pp band, enumeration +7.1 pp beyond its 1.0 pp band
under C), consistent with the standard-path grid above — the plan-execute
path adds a complex_reasoning ranking cost the standard-path grid didn't
have.

**Documented operating point unchanged.** This rerun does not move the
standard-path operating-point recommendation above (`rrf_weight_bm25 =
0.5`, α unchanged, for an operator opting a KB into `bm25`): C5 (weight
0.5) performs no better than C (default weight) on the plan-execute path,
and neither clears the bar, so there is no new operating point to
document for dispatch-on deployments. Artifacts:
`.superpowers/sdd/2026-09-06-rag-sota-wave4/t6-{A1..A3,C1..C3,C51..C53}.json`
(+ matching `.log`, workspace-only, gitignored), analysed by
`eval/fixtures/bm25-scale/analyse-dispatch-grid.py`.

### Cost at corpus scale (Wave 3 Task 7, 2026-09-06)

Wave 2 measured no latency difference between the modes and flagged the
`bm25` scoring CTEs' O(candidates × lexemes) shape as "worth re-checking at a
KB an order of magnitude larger". It is real. A synthetic 99 825-chunk KB
(ruling W3-R14: 55 salted copies of the PPM-Eval corpus in
`document_chunks_768`, seeded and dropped by
`eval/fixtures/bm25-scale/seed-scale-kb.sh`, stats written by the production
refresher) was EXPLAINed with both builders' real SQL — rendered by the new
`cmd/eval --print-keyword-sql "<q>" --kb-id <uuid>`, so the profiled
statement cannot drift from the one the query path sends — at three query
shapes, 3 runs each, LIMIT 50:

| Query shape | Mode | Candidates 1.8k | Candidates 100k | Exec ms 1.8k | Exec ms 100k | Growth | Plan ms 1.8k | Plan ms 100k | GIN 1.8k | GIN 100k | Buffers 100k (shared hit) | cand CTE 100k |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| rare | `ts_rank` | 817 | 44935 | 7.7 | 206.8 | 27× | 1.6 | 1.4 | yes | no | 909,982 | — |
| rare | `bm25` | 817 | 44935 | 52.3 | 2648.7 | 51× | 2.8 | 2.4 | yes | no | 1,207,096 | 6.3 MB (Memory) |
| common | `ts_rank` | 1766 | 97130 | 18.2 | 282.0 | 16× | 1.7 | 1.7 | no | no | 1,344,949 | — |
| common | `bm25` | 1766 | 97130 | 113.1 | 5623.2 | 50× | 2.8 | 2.9 | no | no | 2,653,993 | 17.2 MB (Memory) |
| phrase | `ts_rank` | 64 | 3520 | 1.0 | 26.2 | 25× | 1.3 | 1.3 | yes | yes | 28,638 | — |
| phrase | `bm25` | 64 | 3520 | 4.5 | 207.4 | 46× | 2.2 | 2.6 | yes | yes | 56,017 | 0.5 MB (Memory) |

Reading it: the candidate set grows exactly 55× (the copy factor) on every
shape, `ts_rank` execution grows 16–27×, `bm25` grows 46–51×. The
`bm25`/`ts_rank` ratio therefore *widens* with corpus size — 6.8× → 12.8×
(`rare`), 6.2× → 19.9× (`common`), 4.3× → 7.9× (`phrase`).

**Part of that widening is lost parallelism, not extra work.** At 100k the two
low-selectivity `ts_rank` plans go parallel (`Gather Merge`, 2 workers
launched + the leader = 3 processes, `loops=3` on the `Parallel Seq Scan`)
while the `bm25` plans stay **serial** — the materialised `cand` CTE blocks
parallelism. Counting the `ts_rank` side's extra workers as CPU time, the
ratio is roughly **6.6× (`common`) and 4.3× (`rare`)** rather than 19.9× and
12.8×. The `phrase` shape is serial on both sides at both sizes, so its 7.9×
is a clean like-for-like comparison. The wall-clock numbers are still what a
user waits for, and the operational conclusion below is unchanged — but the
CPU-side gap is smaller than the wall-clock gap, and a Postgres configured
with more parallel workers would widen the wall-clock ratio further without
`bm25` doing anything worse.

The reason the work scales at all is that it is per-candidate (`unnest` the candidate's tsvector, join it
against the query lexemes, `LEFT JOIN bm25_term_stats_<dim>` per matched
lexeme, once **per arm**; the simple arm doubles it). The worst case measured
is one keyword arm taking **5.6 s** (`bm25`, the low-selectivity `common`
shape, 97 130 candidates) against 282 ms for `ts_rank` on the identical
candidate set — a whole turn's latency budget inside one of two retrieval
arms, and the arm runs once more per alternative phrasing when
multi-query/RAG-Fusion/sub-queries are on. At the 1815-chunk production
fixture the same pair is 113 ms vs 18 ms, which is why the Wave-2
measurement saw nothing.

Memory: the materialised `cand` CTE reports `Storage: Memory` with a maximum
of 471 kB / 6.3 MB / 17.2 MB (phrase / rare / common) at 100k; no sort spilled
(`top-N heapsort`, 31 kB) and no plan in the set wrote a temp file. The
footprint scales with the candidate count, not the corpus, and stays inside
`work_mem` at this size — but it is per concurrent query.

Index usage is identical in both modes (they share the candidate WHERE clause
byte-for-byte): the GIN tsvector indexes are used for the selective `phrase`
shape at both sizes; `rare` (45 % of the corpus matches, because the
OR-token recall floor is deliberately generous) uses GIN at 1.8k and falls to
a sequential scan at 100k; `common` (97 % match) never uses GIN. That is the
planner behaving correctly, and it is a property of the shared candidate
clause, not of the scoring mode.

**Operational reading:** `bm25` is affordable at the KB sizes this
deployment runs today and is not affordable, unchanged, at 100k chunks with
unselective queries. Before enabling `bm25_scoring_mode = bm25` on a large
KB, either bound the candidate set (the OR-token floor is the driver: a
tighter floor is the single biggest lever) or accept seconds of keyword-arm
latency on broad queries. Caveats — synthetic near-duplicate text, a KB that
is the sole occupant of its dim table (so `kb_id` has no selectivity), dim
768 rather than production's 4096, warm cache, single client — are recorded
in `eval/golden/bm25-retune.acceptance.md`.

## Raw-query lane (rewrite ⊕ raw, multi-turn)

`chat_condense_keep_raw_enabled` (default off, per-KB): a multi-turn follow-up first gets condensed against conversation history (has been since before this wave — "the answer LLM was single-turn until 2026-06", see `chat_answer_history_*` in `CLAUDE.md`); condensation can paraphrase away a named entity or literal phrase the rewrite doesn't preserve, the classic multi-turn lookup-recall failure mode. When this flag is on, the chat layer forwards the user's **verbatim** last-turn utterance alongside the condensed query, in `vector.SearchOptions.RawQuery`. `Search()`'s `effectiveRawQuery` guard (`internal/vector/raw_query.go`) trims both strings and returns empty when the raw utterance is blank or identical to the final (condensed) query — so a first-turn question, which was never condensed, adds nothing. When it fires, the raw utterance runs as **one extra list on both arms** (vector + BM25 — independent of `rag_fusion_enabled`, this is not the RAG-Fusion mechanism above) and folds into the same RRF pool as the condensed query's lists; `rag_raw_query_list_total{outcome}` counts it and `raw_query_lists` appears in the `rag.search.stages` log event. `RawQuery`'s presence is part of the query-cache shape hash (`internal/vector/query_cache_shape.go`) — a cache entry built with the raw lane on is never served for a request where it's off, and vice versa.

Threaded on the standard `PrepareChatContext` path and the Supervisor path (`RetrieverAgent` + `EnumeratorAgent`, via `agents.Input.RawQuery`). **Not** wired into public API, OpenAI-compat, MCP server, agent teams, plan-execute, agentic, DRIFT, or `RunDeepChat` (the legacy 2-step default orchestrator for `complex_reasoning` when no orchestrator flag is on) — none of those paths carry the condensed/raw-history distinction the standard and Supervisor paths do.

**Measured (Wave 2, Task 4/9): stays off.** `eval/golden/multi-turn-de.jsonl` (18 conversations / 45 turns, KB `PPM-Eval`, derived from the JLU-internal production set → gitignored; `eval/golden/README.md` §"Multi-turn set") exercises `chat.CondenseFromHistory` per `turn_kind`: `corpus` (18, opening turns — no history, no condensation, unaffected by this flag by construction), `pronoun_ref` (12 — pronoun/ellipsis follow-ups on the opener's subject; the kind the raw lane is meant to help), `topic_shift` (6 — an unrelated second question dropped in after the opener), `answer_ref` (6 — retrieval-free reformat, bypasses condensation entirely per ruling W2-R3, `condensed_query = null` on every row), `post_abstain` (3, 2-turn conversations testing an unanswerable opener + a normal follow-up).

Per-`turn_kind` recall/MRR (k=10, `--production-context`, three runs: `--keep-raw off` (baseline), `--keep-raw on` (candidate), `--keep-raw off` again (same-flag noise repeat) — full table in `eval/golden/multi-turn-de.acceptance.md`):

| kind | n | recall off | recall on | recall off2 | MRR off | MRR on | MRR off2 |
|---|---|---|---|---|---|---|---|
| `corpus` | 18 | 0.889 | 0.889 | 0.889 | 0.889 | 0.889 | 0.889 |
| `pronoun_ref` | 12 | 0.833 | 0.806 | 0.806 | 0.833 | 0.767 | 0.833 |
| `topic_shift` | 6 | 0.748 | 0.748 | 0.748 | 1.000 | 1.000 | 1.000 |
| `answer_ref` | 6 | 1.000 | 1.000 | 1.000 | 1.000 | 1.000 | 1.000 |
| `post_abstain` | 3 | 0.833 | 0.833 | 0.833 | 1.000 | 1.000 | 1.000 |
| **overall** | 45 | 0.866 | 0.859 | 0.859 | 0.911 | 0.893 | 0.911 |

Every kind except `pronoun_ref` scores byte-identical across all three runs (expected for `corpus`/`answer_ref`; evidently immaterial for `topic_shift`/`post_abstain` on this fixture). `pronoun_ref` is the only kind whose condensed rewrite plausibly varies between draws, and it's the one the decision rule tests: `on − off` recall = 0.8056 − 0.8333 = **−2.8 pp** (i.e. keep-raw ON scored *worse*, not better), exactly equal in magnitude to the OFF-vs-OFF noise band (`|off2 − off|` = 2.8 pp) — the rule ("flip ON only if `on − off` exceeds the noise band, with no kind losing more than the noise band") fails outright since the delta is negative. MRR sharpens the verdict: `pronoun_ref` MRR off 0.833 → on 0.767 (**−6.7 pp**), against 0.0 pp noise (off2 = off exactly) — a real, noise-exceeding regression under keep-raw ON. The MTRAG "rewrite ⊕ raw" gain did not replicate on this fixture.

**Default stays off.** Caveats: single run per cell (the noise band is one repeat, not a variance estimate), a 12-question `pronoun_ref` bucket, and LLM-condensation non-determinism (temperature > 0 on the condenser call) as a confound alongside the flag itself — see the "Rewrite ⊕ raw last turn" recipe in `docs/feature-recipes.md` for the enablement toggle and the full acceptance record.

## Recency prior (similarity ⊕ freshness)

Recency prior (`recency_boost_enabled`, default off): after the rerank blend and the feedback boost (stage 10c), each doc gets `recency_boost_weight · 2^(−ageDays/recency_half_life_days)` added to its score, then the pool re-sorts. Age is the source file's **effective date**, `COALESCE(published_at, created_at)` (see "Effective date" below); for RSS/Confluence each item is its own file row, so the ingest timestamp already approximates publish time — that's the intended corpus type. One main-DB query per search over the fused pool's distinct file IDs; fail-open. Defaults pre-seeded at the literature operating point (weight 0.1, half-life 14 d — arXiv 2509.19376). Antipattern: enabling on static document KBs — gains nothing, and re-uploading a file resets `created_at`, churning rankings. `internal/vector/recency_boost.go`.

**Now measurable (Wave 2, Task 8/9):** `eval/golden/cert-recency-de.jsonl` (25 questions; synthetic, fully fictional German CERT-advisory corpus — 12 fictional products, 26 fictional WID-SEC advisories, 14 issued as NEU then followed by an UPDATE, ages 1–120 days; `eval/fixtures/cert-advisories/**` + `eval/fixtures/seed-cert.sh`, both committed) is the first golden set with time-sensitive questions. A/B (`--recency-boost on|off`, standard path forced via `--orchestrator-dispatch=false` to isolate the mechanism from the LLM query-type classifier's own non-determinism — full tables including the production-like dispatch-on run in `eval/golden/cert-recency-de.acceptance.md`): `recency_boost_enabled=on` raised overall recall 0.651 → 0.696 against a 4.0 pp same-flag noise band (two off-only runs: 0.651 / 0.611), and raised the UPDATE-outranks-NEU win rate across the 8 NEU/UPDATE pairs from 3/8 to 5/8 — a marginal but directionally positive, noise-exceeding effect on n=25 questions. **Default stays off**; recommended specifically for RSS/CERT-style KBs. Recency-listing (`chat_recency_listing_enabled`, see "Date-aware chat" in `docs/feature-recipes.md`) was exercised on the same fixture and confirmed to resolve explicit windows and the name-marker arm correctly when the question reaches the standard path — it does not fire when orchestrator dispatch routes the same question to `plan_execute`/`supervisor`/`agentic` instead, a pre-existing production interaction the fixture surfaces rather than introduces. Note for future `cmd/eval` users: prior to this task no eval adapter set `CurrentDateLine`/wired a recency lister at all, so every earlier `--production-context` run silently diverged from production date-awareness; `--orchestrator-dispatch=false` is the right setting for isolating standard-path-only features like this one.

### Effective date (`COALESCE(published_at, created_at)`)

Every date-keyed read in the pipeline goes through one expression. `effectiveDateExpr` in
`internal/vector/recency_boost.go` is `COALESCE(published_at, created_at)` and is used by the
recency boost's `fileCreatedTimes` lookup, the `SearchOptions.CreatedAfter` window filter, both
`recent_documents` queries (`RecentDocuments` and `NameMarkerDocuments`, which keep a
byte-identical package-local copy of the constant — `internal/mcp/builtin` importing
`internal/vector` would be a new dependency edge for one string), the admin KB overview's
`oldestFileAt` / stale-share aggregates and the KB-card LATERAL. Each package carries a source-text
drift test that fails if a bare `created_at` reappears in its SQL, and both tests pin the same
literal, so changing one fails the other.

`files.published_at` (migration 0071) is populated by the three origins that carry a content date:
RSS items, from the feed entry's `PublishedParsed`; Confluence **pages**, from the page's current
version timestamp (Wave 4, W4-R10 — attachments keep NULL, because the REST shape carries no
attachment date and the parent page's version date is the *page's* content date); and git files,
from the HEAD commit's **committer** time, shared by every file of that shallow-clone sync (W4-R11
— `Depth: 1` means there is no per-file history to read, so this is deliberately a repository-level
date). All three write through the one clamp `files.ClampPublishedAt` (a future date becomes `now`;
nil stays nil), so a skewed source clock cannot park a file permanently at the top of a recency
window. Uploads, the crawler and every other origin leave it NULL and therefore keep keying on
ingest time. There is **no backfill**: files ingested before 0071 — and Confluence/git files not
re-synced since — stay NULL until re-polled, re-synced or re-ingested. Consequence for the recency
prior on an existing RSS KB: nothing changes until the feed next delivers, and then newer items
start being aged by their own publication date rather than by when the poller happened to fetch
them — which is what makes "re-uploading a file resets `created_at`" survivable for RSS corpora
specifically. Confluence has no update path (a changed page is deleted and re-created), and a git
sync whose HEAD has not moved creates no files, so on those two origins a re-sync is what moves the
date forward.

The date-window semantics the chat layer exposes (`kb_search`'s `date_from`/`date_to`, the
recency-listing window, `recent_documents`) are documented in `docs/feature-recipes.md`
§"Date-aware chat"; the freshness/staleness reporting built on the same expression is
§"Freshness surface".

## Ingestion deduplication

Pre-embed chunk deduplication: within an ingestion job and across files in the same KB, chunks whose normalized content (lowercased, whitespace-collapsed) hashes to an already-stored value are skipped before embedding — saves API calls + storage. Migration `0007_chunk_content_hash.sql` adds `content_hash` + `(kb_id, content_hash)` index to the default 1536-dim chunk table; non-default-dim tables (e.g. `document_chunks_4096` for qwen3-embedding-8b) get the same column + index automatically on every worker startup via `EnsureVectorTables` (idempotent `ALTER TABLE ADD COLUMN IF NOT EXISTS`). Skipped batches log a `processor.dedup` event with `kept`/`dropped`/`total`.

**2026-09-05 fix: cross-file dedup was a silent no-op on any non-1536-dim table.** The hash lookup that finds an already-stored chunk (for the "skip, this content is already in the KB" path) queried the dim-keyed table by a **hardcoded** `1536`, regardless of which table the KB's embedder actually writes to. On the production `document_chunks_4096` table (qwen3-embedding-8b, live since 2026-06-01) the lookup queried a table with no rows for the KB, so no duplicate was ever found — every ingestion silently re-embedded and re-stored content already present elsewhere in the KB. `Processor.dedupDimensions(ctx, kbID)` (`internal/processor/processor.go`) now resolves the KB's real embedding dimension from its AI config (`cfg.EmbeddingDimensions`), falling back to the old constant (renamed `legacyDedupDim = 1536`) only when the model declares no dimension. The integration fixture (`internal/processor/integration_test.go`) was deliberately moved off the 1536 default to `embedDim = 768` so this class of bug can't reproduce silently in CI again.

## Ingest prompt-injection screening (Wave 5)

`ingest_screening_enabled` (default **ON**, a kill switch) runs one `promptsafety.ScreenText` pass over a file's parsed text **before chunking**, for the four externally sourced origins (`rss`, `confluence`, `git`, `crawl`) only. Note what "only" does and does not cover: with the flag on, the **origin lookup** (one single-column SELECT, `GetFileOrigin`) runs for *every* ingested non-spreadsheet file — the origin is read from the store rather than threaded through `ProcessFileInput`, because a missed construction site would disable screening with no symptom. It is the **regex pass** that is limited to the four origins; a user upload costs the SELECT and nothing more. It is a **flag, not a filter**: nothing about chunking, embedding, storage or retrieval changes when it fires. What changes is `files.injection_flag` / `files.injection_detail` (migration **0072**) and one metric, `rag_ingest_injection_flag_total{origin}`. User uploads are never screened (a user's own content), and spreadsheets never are (row records are not prose).

The pattern set is deliberately **not** `LooksLikeInstruction`: that heuristic includes an `https?://` alternative, and an external document without a URL barely exists, so screening on it would flag everything — informationally identical to flagging nothing. Screening therefore uses its own `screenRules` (the same alternatives minus the URL one) with a per-rule *name* that is persisted and read by the UI, which is why the rules are a list of named patterns rather than one alternation whose submatch index would silently renumber when an alternative is added.

Windows slide with a step of half the window (`ingest_screening_window_runes`, default 600, range 100–5000) so a phrase straddling a boundary still matches; the first hit wins. `ScreenText` is allocation-light on purpose — a parsed document can be several MB, so it keeps one byte-offset anchor per step instead of materialising a `[]rune`.

`injection_detail` carries **three** states, and the difference is load-bearing for the UI: `NULL` = never screened; `{"screened_at": …}` alone (flag false) = screened and clean; a detail carrying `"rule"` (flag true) = flagged, with `position` as a **rune** offset and `snippet` capped at 300 runes. A clean re-screen therefore clears a stale finding rather than leaving a permanent badge, and that UPDATE is conditional on the verdict changing so a poll does not rewrite unchanged rows. Enablement block and surfaces: `docs/feature-recipes.md` §"Ingest prompt-injection screening". `internal/promptsafety/screen.go`, `internal/processor/screening.go`.

## Spreadsheets

The 2026-09-04 spreadsheet-ingest rework is a hybrid design: Phase 1/2 built `sheetsource` (a streaming, typed reader over xlsx/xls/ods/csv), `tabular/profile` (a sheet profiler that finds header blocks, regions, and per-column roles), and `tabular/ingest`, which drives both a materializer (one native-typed Postgres table per table region, `internal/tabular`, plus a `tabular_column_values` distinct-value index for fuzzy lookup) and a key:value hybrid text renderer (`tabular/render`) that folds a profile card per region into the normal chunk/embed pipeline. Phase 3 adds a deterministic **tabular router** on top of that materialized schema, so a lookup/aggregation question can be answered with one validated, executed SQL statement instead of (or alongside) the chunk-level free-text search.

**The router's steps** (`chat.NewTabularRouter`/`TabularRouter.Run`, `internal/chat/tabular_router.go`), run once per turn on both the standard `PrepareChatContext` path and the Supervisor path, before retrieval:

1. **Flag gate** — the AND of the master tabular flag and `chat_tabular_router_enabled`, resolved from the per-KB overlaid config; disabled short-circuits everything below.
2. **KB gate** — `HasDataForKB` (60s-cached), so a KB with no ingested spreadsheet never pays for the rest of the pipeline.
3. **Cues** — `DetectTabularCues` classifies the question (aggregation words, filter/comparison words, quoted/id/span/number literal candidates); an id-shaped literal that matches a stored value fires the router on its own even without an aggregation/filter cue.
4. **Stored-value lookup** — `Catalog.LookupValues` matches each candidate literal against `tabular_column_values` (exact → prefix → substring), so the SQL-generation call gets verbatim column=value pins instead of asking the LLM to guess casing/spelling.
5. **Compact schema** — `tabular.CompactSchema` renders a token-budgeted (`chat_tabular_router_schema_max_tokens`) summary of the KB's tabular.\* tables, ranked by relevance to the question, with `AllowedTables` carrying every table name the validator may accept regardless of what made the cut.
6. **One structured SQL call** — a fast-tier LLM call (`ai.GenerateTabularSQL`, Structured Outputs) proposes exactly one SELECT (or declines with `sql: null` when the listed tables can't answer the question — a terminal, non-repairable outcome).
7. **Validate → execute** — `tabular/sqlcheck.Validate` runs the proposal through a fail-closed AST walker (`cockroachdb-parser`): only `AllowedTables`, an allowlisted function/relation set, a top-level-literal tokenizer backing the read-only gate (no writes, no locking clauses, no catalog access), and a LIMIT is enforced — injected via a wrapping subquery when the proposal omitted one. A validated statement then runs through `tabular/sqlexec.Executor` in a `BEGIN READ ONLY` transaction with `SET LOCAL statement_timeout` (`chat_tabular_router_timeout_ms`) and a row cap (`chat_tabular_router_max_rows`).
8. **Bounded repair loop** — a DB error, an empty result, or an all-NULL aggregate feeds the failure text back to the LLM for one more attempt, up to `chat_tabular_router_max_repairs` (default 3) rounds; a clean result is never re-reviewed, and a validator rejection or an unanswerable decline is terminal (no repair spent chasing a structurally invalid statement).
9. **KV injection** — a successful result is rendered as a system-prompt addendum (`prompts.TabularRouterAddendum`): compact `- col: value` lines for ≤ 20 rows, a markdown table beyond that, a row count, a capped notice, and source lines (file › sheet). Every rendered cell passes through `promptsafety.LooksLikeInstruction` first — a spreadsheet cell is untrusted content, not a trusted instruction channel, the same threat model as the sheet-profiler prompt.

The whole router is **fail-open by construction**: every failure mode (disabled flag, catalog error, no cue, empty schema, LLM error, repairs exhausted, a cancelled turn) degrades to either a non-fired result or an "attempted but no usable result — rely on retrieved context" addendum, and `Run` never returns an error. A cancelled turn (client gone, turn budget spent) is checked before every LLM call and every DB round trip so an abandoned turn can't spend either.

**Retrieval hints, independent of firing:** once the KB gate passes, the router always rewrites the search query with `PromoteIdentifierPhrases` (an id-shaped literal becomes an exact quoted phrase for the BM25 arm) and always forces the simple BM25 arm on (`SearchOptions.ForceBM25SimpleArm`) — a KB that has tabular data at all benefits from exact-phrase keyword matching on cell-shaped tokens even on turns where the router itself doesn't fire.

**SQL log:** every turn the router had an opinion on is recorded to `tabular_query_log` after the response is sent, keyed by the KB, the AI message id, the question, the final SQL (NULL when never fired), the row count, and the outcome (`Catalog.InsertQueryLog`) — with two exceptions (R59): `skipped_disabled` and `skipped_no_tables` are NOT logged, because with the router wired on every KB those two outcomes fire on essentially every turn deployment-wide (feature off, or a KB with no spreadsheet at all) and would grow the table unboundedly for zero analytical value. Every other `skipped_*` reason, and every fired outcome, is logged.

**Metrics** (`GET /metrics`, admin-protected): `rag_tabular_router_total{outcome}` (one of `fired_ok`, `fired_empty`, `sql_error`, `llm_error`, `validator_rejected`, `cancelled`, or `skipped` — every `skipped_<reason>` trace outcome collapses to the single `skipped` label to keep cardinality bounded; the specific reason still lives on the `tabular_router_skipped` trajectory event), `rag_tabular_router_rows` (histogram, rows returned by a `fired_ok` result), `rag_tabular_router_repairs_total` (counter, incremented by the repair count on `fired_ok` and on the repairs-exhausted terminal outcomes only — not on `llm_error` or an immediate unanswerable decline, neither of which spends a repair round).

**Eval:** `cmd/eval`'s trajectory/production-context report gains two rates over the golden set's eligible questions — `tabular_router_fire_rate` (fraction that fired) and `tabular_sql_error_rate` (fraction of fired attempts whose terminal outcome was `sql_error` or `validator_rejected` — never resolved to a usable result; `llm_error` is not counted in this rate). The denominator is the golden set's `lookup` + `complex_reasoning` questions by default, or (Ruling R75) the explicit `tabular_expected: true` questions when a golden set annotates that field. Phase 4's acceptance run against `eval/golden/spreadsheets-de.jsonl` (40 questions, all `tabular_expected`-annotated; see `eval/golden/spreadsheets-de.acceptance.md`, "Run 3") landed `fire_rate = 1.000` (30/30, clears the ≥0.90 bar) and `sql_error_rate = 0.000` (0/28, clears the ≤0.05 bar) once the eval adapter itself was fixed to wire `TabularRouter`/`TabularRouterConfig` into the Supervisor path's `chat.RunSupervisorChat` call (the earlier "Run 2" gap: 7 of 30 eligible questions dispatched to Supervisor and never got a chance to fire the router at all) and column labels were rendered as `(label: …)` documentation rather than literal identifiers (the earlier `sql_error` on a slash-bearing display label used verbatim as a column name). Judged correctness clears 0.85 on the mean-of-means proxy (0.960) but narrowly misses on a stricter per-question-paired variant (0.840) tied to a handful of judge JSON-parsing failures, not router or retrieval quality — see the acceptance record for the full threshold table and the primary-vs-strict discussion.

**Known limits:**
- Numeric result values always render as their canonical Postgres decimal-string text (never a Go `float64`), which is exactly right for citation-safe display but means the answer LLM sees `"700.5"`, not a typed number — fine for prose, not for further arithmetic downstream.
- The kill switch is per-KB (the overlaid config reader), not global-only — one KB can run the router while another on the same deployment has it off.
- `table_query` (the MCP tool, distinct from the router) still describes shadow `_num` columns and id columns the same way it did before Phase 3; the router and the tool share the catalog but not a code path.
- Open-ended (no explicit end row/column) regions size themselves from the xlsx sheet's own `<dimension>` element, guarded against a stale single-cell dimension stub some spreadsheet authoring tools leave behind after a delete-and-shrink edit.

**Phase 4 (ops):** three mechanisms sit outside the router's per-turn path but shape what data is available to query. (1) `GET /api/kb/{id}/files/{fileId}/tabular` — the "Tabellen" file-detail panel — serves the persisted `files.parse_report` joined with the file's `tabular_catalog` rows (`tabular.FileTabularDTO`), so an operator or KB member can inspect exactly what the profiler/materialiser saw for one file without writing SQL; same instruction-filtering as the router's addendum applies to descriptions/samples/value sets. (2) The large-file gate (`internal/processor/large_file_gate.go`) bounds how many spreadsheets above `tabular_large_file_bytes` one worker process materialises at once (`tabular_large_file_concurrency`, read once at worker startup — a restart is needed to change it); a waiting file's `stage_detail` reports it is waiting for a slot. (3) `internal/tabular.OrphanSweeper` (the `tabular_orphan_cleanup` maintenance loop, every 6h) drops materialised `sheet_*` tables (and their `tabular_column_values` rows) whose owning `files` row no longer exists — a table whose file still exists but whose `tabular_catalog` row went missing is left alone, since the next ingest just recreates it. Full operator detail (sizing, alerting, the 413 upload gotcha, the grant checklist, acceptance run) in `docs/runbooks/spreadsheet-ingest-ops.md`.

## Retry & fail-open policy (intentional)

Load-bearing embedding calls retry; enhancement calls fail open without retry.

| Call | Policy | Rationale |
|---|---|---|
| Embeddings (batch + single/query) | 3 attempts, 2s/4s/8s backoff | Retrieval cannot proceed without the query vector or chunk vectors. |
| Rerank | Fail-open → RRF fallback, no retry | A missing rerank degrades ranking, not correctness. |
| Relevance grade (CRAG) | Fail-open → ambiguous verdict, no retry | CRAG is corrective; an absent grade defaults to keeping context. |
| Multi-query expansion | Fail-open → empty alternatives, no retry | Expansion is additive; the base query still runs. |
| BM25 / keyword | Vector arm fatal, keyword arm fail-open (errgroup) | Keyword is one fusion arm; its failure must not sink the search. |

`GenerateEmbedding` (single/query path via `EncodeQuery`) now delegates to `GenerateEmbeddings` (the batch path) so both share the same 3-attempt retry loop with 2s/4s/8s exponential backoff. The wire format for a single-text call is `"input": ["text"]` (a one-element array) rather than the historical bare string — OpenAI-compatible providers accept both forms. Implementation: `internal/ai/embedding.go`, functions `GenerateEmbedding` and `GenerateEmbeddings`.
