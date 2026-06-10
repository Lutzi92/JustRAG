# Admin-Einstellungen: Vollständige Referenz

Diese Datei listet alle im Admin-UI tunbaren `site_config`-Parameter von JustRAG mit Default-Wert, gültigem Wertebereich, Wirkung und Tuning-Richtung. Sie ist als Operator-Referenz gedacht: jede Zeile beantwortet "was passiert, wenn ich den Wert hochdrehe / runterdrehe / einschalte / ausschalte".

Quelle der Wahrheit ist immer der Go-Code:
- `go-backend/internal/chat/siteconfig.go` — Chat- und Orchestrator-Knobs
- `go-backend/internal/vector/search_siteconfig.go` + `vector/config.go` — Retrieval-Pipeline
- `go-backend/internal/vector/query_cache_config.go` — Query-Cache
- `go-backend/internal/chat/service.go` — CRAG + adaptive Routing
- `go-backend/internal/processor/processor.go` — Ingestion (Enrichment, KG, RAPTOR)
- `go-backend/internal/app/worker.go` — Docling-Wiring

**Konvention für ungültige Werte:** Out-of-range oder unparsbare Werte werden in der Regel still auf den Default zurückgesetzt; die ausführliche Logik steht in `readBool` / `readInt` / `readFloat` in `chat/siteconfig.go`. Operatoren sehen einen verworfenen Wert nicht im UI — Sanity-Check über `cmd/eval` oder im Log.

**Modell-Resolutionskette (Fast-Tier):** Bei jedem `*_model`-Knob gilt: per-Task-Wert → `model_tier_fast` → leer (Caller fällt auf das KB-Chat-Modell zurück). Das heißt: `model_tier_fast` einmal setzen reicht, um sechs+ Fast-Tier-Tasks gleichzeitig auf ein kleines Modell zu pinnen.

---

## Inhaltsverzeichnis

1. [Chat-Orchestrierung (`chat_*`)](#1-chat-orchestrierung-chat_)
2. [Faktualitäts- und Refine-Gates](#2-faktualitäts--und-refine-gates)
3. [Turn-Budgets (AP-A3)](#3-turn-budgets-ap-a3)
4. [KB-Router (AP-A4)](#4-kb-router-ap-a4)
5. [Tools, MCP, Session-Memory](#5-tools-mcp-session-memory)
6. [Langzeit-Memory (`chat_longmem_*`)](#6-langzeit-memory-chat_longmem_)
7. [Knowledge-Graph (`kg_*`, `chat_graph_*`)](#7-knowledge-graph-kg_-chat_graph_)
8. [Retrieval-Pipeline (Vector, BM25, MMR, Reranker)](#8-retrieval-pipeline-vector-bm25-mmr-reranker)
9. [Query-Cache (`query_cache_*`)](#9-query-cache-query_cache_)
10. [CRAG + Adaptive Routing](#10-crag--adaptive-routing)
11. [Ingestion: Docling, Enrichment, Parent-Child, RAPTOR](#11-ingestion-docling-enrichment-parent-child-raptor)
12. [Validierung: Citation + Factcheck](#12-validierung-citation--factcheck)
13. [Observability + Sampling](#13-observability--sampling)
14. [Modell-Tier (`model_tier_fast`)](#14-modell-tier-model_tier_fast)
15. [Schnellrezepte: Phase-Aktivierung](#15-schnellrezepte-phase-aktivierung)

---

## 1. Chat-Orchestrierung (`chat_*`)

Die vier Orchestratoren werden in Prioritätsreihenfolge geprüft (Supervisor → Plan-Execute → Agentic → Standard). Der erste aktivierte gewinnt. Default-Deployment ist Standard (legacy `RunDeepChat`).

| Knob | Typ | Default | Bereich | Wirkung |
|---|---|---|---|---|
| `chat_supervisor_enabled` | bool | **false** | — | Aktiviert Supervisor-Orchestrator (1 Klassifikations-LLM-Call → Spezialist-Agent → Search → Answer). **On**: schnellster Multi-Agent-Pfad, gut bei klar trennbaren Query-Klassen. **Off**: andere Orchestratoren oder Standard-Pfad. |
| `chat_plan_execute_enabled` | bool | **false** | — | Plan-and-Execute: LLM zerlegt Frage → iterative Sub-Suchen → Generate. **On**: bessere Coverage bei Multi-Hop-Fragen, +1,5–3 s Latenz. **Off**: kein Planning-Overhead. |
| `chat_plan_execute_max_sub_queries` | int | **3** | 1–5 | Max. Anzahl an Sub-Fragen pro Plan-Run. **Hoch**: mehr Coverage, mehr LLM-Kosten. **Niedrig (1)**: degeneriert zu Single-Hop, kein Multi-Hop-Vorteil. |
| `chat_plan_execute_max_iterations` | int | **3** | 1–5 | Max. Iterations-Runden (Replan + Re-Search). **Hoch**: mehr Tiefe bei komplexen Fragen, höhere Latenz und Kosten. **Niedrig**: schneller, riskiert vorzeitigen Abbruch. |
| `chat_plan_execute_token_budget` | int | **8000** | 2000–32000 | Gesamt-Token-Cap pro Plan+Iterate+Generate. **Hoch**: längere Pläne und mehr Iterationen möglich. **Niedrig**: Hartstopp schützt vor Kostenexzess, kann aber Antwort frühzeitig abkappen. |
| `chat_plan_execute_model` | string | **""** | — | LLM-Override für Plan/Iterate. Leer → Enrichment-Modell → Fast-Tier-Chain. **Klein/schnell**: Kostenoptimiert; Planning ist Klassifikation, kein Reasoning. |
| `chat_plan_execute_dag` | bool | **false** | — | DAG-statt-Liste-Plan: Plan-Nodes mit Abhängigkeitskanten, paralleler Topo-Sort-Pfad. **On**: bessere Parallelität bei unabhängigen Sub-Fragen. **Off**: einfache Liste, deterministisch. |
| `chat_plan_execute_dag_iterative` | bool | **false** | — | Inter-Level-Critic (AP-D3): nach jeder DAG-Ebene läuft ein LLM-Critic, fügt Nodes hinzu, prunet redundante, kann früh stoppen. **On**: +1 LLM-Call pro Ebene, höhere Antwortqualität bei dynamischen Plänen. **Off**: statische DAG-Ausführung. Benötigt `chat_plan_execute_dag=true`. |
| `chat_plan_execute_dag_iterative_model` | string | **""** | — | Fast-Tier-Override für DAG-Critic. Critic ist Klassifikation → kleines Modell empfohlen. |
| `chat_plan_execute_max_dag_depth` | int | **3** | 1–5 | Längste Kette im DAG-Plan. **Hoch**: tiefere Reasoning-Ketten erlaubt. **Niedrig**: Circuit-Breaker gegen pathologische Pläne. Tiefe > 5 zahlt sich empirisch selten aus. |
| `chat_plan_execute_max_dag_nodes` | int | **6** | 1–12 | Hartes Cap auf Gesamt-Knotenzahl. **Hoch**: breitere Pläne. **Niedrig**: Kostenkontrolle. |
| `chat_plan_execute_tool_aware` | bool | **false** | — | Planner sieht den Tool-Katalog (AP-B3, statt nur `kb_search`). **On**: Planner kann `keyword_search`, `chunk_read`, `graph_search`, `calculator` etc. einplanen — verbessert Multi-Modal-Reasoning. **Off**: rein retrieval-getrieben. Setzt MCP-Registry voraus. |
| `chat_agentic_enabled` | bool | **false** | — | Agentic-Loop: 1. Suche → LLM-Critic → optionale Folgesuchen. **On**: Multi-Hop-Recall steigt, +1–2 LLM-Judge-Calls. **Off**: kein Critic-Overhead. |
| `chat_agentic_max_hops` | int | **3** | 1–5 | Max. Folgesuchen (inkl. erster Suche). **Hoch**: mehr Recall bei langen Reasoning-Ketten. **Niedrig (1)**: One-Shot, Judge nie aufgerufen — gut zum Testen der Verdrahtung ohne Judge-Kosten. |
| `chat_agentic_plateau_stop` | bool | **false** | — | Early-Stop bei Qualitätsplateau (Phase 1 §1.3): zwei aufeinanderfolgende Runden ≤ Chunk-Schwelle AND Score-Delta unterschritten → Abbruch. **On**: spart Hops, wenn neue Suchen nichts mehr bringen. **Off**: läuft bis `max_hops`. |
| `chat_agentic_plateau_chunks_threshold` | int | **1** | 0–5 | Chunks-pro-Runde-Boden für Plateau-Erkennung. **Hoch (3+)**: aggressiveres Stoppen. **Niedrig (0)**: degeneriert zur bestehenden Zero-Progress-Erkennung. |
| `chat_agentic_plateau_score_delta` | float | **0.02** | 0.0–1.0 | Top-Score-Verbesserung, die nicht als Plateau zählt. **Hoch (0.1)**: strenger, stoppt früher. **Niedrig (0.005)**: läuft länger, akzeptiert kleinste Verbesserungen. |

---

## 2. Faktualitäts- und Refine-Gates

| Knob | Typ | Default | Bereich | Wirkung |
|---|---|---|---|---|
| `chat_factuality_verifier_enabled` | bool | **false** | — | Aktiviert Post-Response-Faktcheck-LLM (Phase 3 §3.3). **On**: Antworten werden auf nicht-belegte Claims geprüft. **Off**: nur deterministischer Citation-Validator. |
| `chat_factuality_verifier_always_run` | bool | **false** | — | Bypass Cost-Gate. **On**: Verifier auf jeder Antwort (höchste Sicherheit, höchste Kosten). **Off**: nur wenn Citation-Validator Suspect-Marker setzt. |
| `chat_factuality_verifier_model` | string | **""** | — | Fast-Tier-Override für Verifier. Klein/schnell genügt. |
| `chat_factuality_gate_enabled` | bool | **false** | — | Refine-Gate (AP-A1): wenn Verifier ≥1 unsupported/contradicted Claim flagt, läuft ein zusätzlicher LLM-Refine-Call und ersetzt die Antwort. **On**: höhere Faktualität, +1 LLM-Call pro betroffener Antwort. Benötigt `chat_factuality_verifier_enabled=true`. **Off**: Verifier-Verdikt wird nur protokolliert, Antwort bleibt unverändert. |
| `chat_factuality_gate_max_refines` | int | **1** | 0–2 | Refine-Runden pro Turn. **0**: faktisch deaktiviert (Refine-Gate aus, ohne Master-Flag zu kippen). **1**: Standard, ein Refine-Versuch. **2**: erlaubt eine zweite Runde, wenn der erste Refine immer noch flags hat. |
| `chat_refine_model` | string | **""** | — | LLM-Override für Refine. Leer → Antwort-Generator-Modell (gleicher Stil und Register). Separat vom Verifier, damit man kleinen Verifier mit großem Refiner kombinieren kann. |
| `chat_self_rag_enabled` | bool | **false** | — | Unified Self-RAG-Verifier (AP-D2): konsolidiert ISREL/ISSUP/ISUSE in einen LLM-Call. **Wechselseitig exklusiv** mit `chat_factuality_verifier_enabled`. **On**: vereinheitlichter Verdict, ISUSE triggert holistischen Refine auch ohne geflaggte Claims. **Off**: klassischer Verifier oder gar keiner. |
| `chat_self_rag_model` | string | **""** | — | Fast-Tier-Override für Self-RAG. Gemma/Qwen3 4B reichen für Structured-Output. |

---

## 3. Turn-Budgets (AP-A3)

Drei orthogonale Caps pro Chat-Turn. **0** bedeutet jeweils "unbegrenzt". Werden in den Orchestratoren nach jedem LLM-Call / Tool-Call abgezogen; Erschöpfung → Turn endet graceful mit Outcome `budget_exhausted`.

| Knob | Typ | Default | Bereich | Wirkung |
|---|---|---|---|---|
| `chat_turn_budget_seconds` | int | **0** (unlimited) | 0–600 | Wall-Clock-Deadline pro Turn. **Hoch (90+)**: erlaubt tiefe Multi-Hop-Pläne. **Niedrig (10–30)**: harte Latenz-SLA, riskiert Plan-Abbruch. **0**: kein Time-out. |
| `chat_turn_budget_tokens` | int | **0** (unlimited) | 0–2 000 000 | Token-Cap kumulativ über alle LLM-Calls. **Hoch**: lange Antworten + viele Plan-Iterationen. **Niedrig**: Kostenkontrolle, vor allem auf Reasoning-Tier-Modellen. |
| `chat_turn_budget_tool_calls` | int | **0** (unlimited) | 0–100 | Max. Tool-Calls (jedes `Registry.Dispatch` zählt eins). **Hoch**: viel Tool-Use erlaubt. **Niedrig (3–5)**: schützt gegen Tool-Loops. |

---

## 4. KB-Router (AP-A4)

Aktiv nur, wenn der Chat-Request `?route=auto` mitschickt. Sub-KB-Routing pickt automatisch die beste passende KB aus dem Pool.

| Knob | Typ | Default | Bereich | Wirkung |
|---|---|---|---|---|
| `chat_kb_router_enabled` | bool | **false** | — | **On**: KB-Router läuft bei `?route=auto`, KB-Beschreibungen (`kb.description`) müssen gepflegt sein. **Off**: bestehende kb_id wird respektiert. |
| `chat_kb_router_min_confidence` | float | **0.6** | 0.0–1.0 | Schwelle für Top-1-Pick. **Hoch (0.8+)**: Router fällt häufiger auf `fallback_all` zurück (alle KBs durchsuchen), weniger Fehlrouting. **Niedrig (0.3)**: aggressiveres Routing, mehr Fehler-Risiko. |
| `chat_kb_router_model` | string | **""** | — | Fast-Tier-Override für Router-Klassifikator. |

