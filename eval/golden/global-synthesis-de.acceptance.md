# `global-synthesis-de.jsonl` acceptance record

- **Date:** 2026-09-06
- **Branch / commit:** `feat/rag-sota-wave3`, `5b1b59a`
  (`feat(eval): --longcontext on|off and --golden-query-type run overrides for
  cmd/eval` — the flags the runs below use; the docs commit lands on top of
  this record).
- **Model stack (from `ai_models` + the run logs):** answer generation and all
  three judges `jlu/gemma-4-26b-it` (the KB carries no per-KB
  `chat_model`/`embedding_model`/`rerank_model` override — all three columns
  are NULL); map-stage finding extractor `jlu-internal/gemma-4-26b-it-bulk`
  (`model_tier_fast`, no `chat_longcontext_map_model` override set);
  reranker `jlu/jina-rerank`; embedder `jlu/qwen3-embedding` (4096-dim).
- **Fixture:** `eval/golden/global-synthesis-de.jsonl`, 12 questions, KB
  `83262307-3a1b-49bc-bd08-3b925a868a92` (`PPM-Eval`, 297 files / 1815 chunks
  in `document_chunks_4096`). Design and the two gating conditions:
  `eval/golden/README.md` §"Global-synthesis set (Wave 3 Task 4)".
- **Site config at run time:** unchanged — no `site_configs` row was written
  for this measurement. `chat_longcontext_*` are all unset in the dev DB
  (defaults), `chat_supervisor_enabled` and `chat_drift_enabled` are empty,
  `crag_enabled = true`. `chat_longcontext_enabled` and
  `chat_longcontext_mode` were supplied per run through the new
  `--longcontext` / `--longcontext-mode` overlays (`chatOverlayReader`,
  `go-backend/cmd/eval/main.go`), confirmed in each run's first log line:
  `eval: applying chat site_config overlays for this run
  {"chat_longcontext_enabled":"true","chat_longcontext_mode":"flat"}`.

## Fixture validation

All 12 questions reached `OrchLongContext` in **every** run — 36/36
question-runs, `errors = 0` throughout:

```
{"msg":"eval.orchestrator_dispatch","question_id":"G01",
 "query_type":"complex_reasoning","orchestrator":"longcontext",
 "dispatch_reason":"complex_reasoning_longcontext_gate"}
{"msg":"rag.longcontext.fired","mode":"flat","max_tokens":100000,
 "top_k":200,"chunks":200}
```

No question was ever classified `lookup`/`enumeration`, so no question had to
be rewritten. Note that the `complex_reasoning` half of the gate is an LLM
call and is **not** guaranteed to reproduce; re-check the `agent` column
before trusting any future run of this set.

## Commands run

```bash
# (a) flat — the current default, byte-identical to the pre-orchestrator path
bash .superpowers/sdd/2026-09-06-rag-sota-wave3/t4-run.sh a-flat flat

# (b) map_reduce, retrieval-regression-gated against (a)
bash .superpowers/sdd/2026-09-06-rag-sota-wave3/t4-run.sh b-mapreduce map_reduce \
  --baseline .superpowers/sdd/2026-09-06-rag-sota-wave3/t4-a-flat.json

# (c) flat again — the noise band
bash .superpowers/sdd/2026-09-06-rag-sota-wave3/t4-run.sh c-flat2 flat
```

`t4-run.sh` expands to
`--golden eval/golden/global-synthesis-de.jsonl --production-context
--orchestrator-dispatch=true --judge --longcontext on --longcontext-mode <mode>`.

## Results

### Judge metrics (n=12, k=10)

| Run | answer relevance | faithfulness | context precision | mean answer length | mean context-assembly latency | wall time |
|---|---|---|---|---|---|---|
| (a) flat | **1.000** | 0.613 | 0.409 | 3232 chars | 10.2 s | 563 s |
| (c) flat (repeat) | **1.000** | 0.597 | 0.420 | 3356 chars | 10.3 s | 568 s |
| (b) map_reduce | **1.000** | 0.565 | 0.600 | 3995 chars | 63.4 s | 954 s |

### Retrieval metrics — diagnostics only, per W3-R8

| Run | recall@10 | MRR | nDCG@10 | precision@10 |
|---|---|---|---|---|
| (a) flat | 0.214 | 0.854 | 0.841 | 0.283 |
| (c) flat (repeat) | 0.233 | 0.792 | 0.741 | 0.308 |
| (b) map_reduce | 0.287 | 0.743 | 0.735 | 0.375 |

`FinalChunks` is the same `TruncateChunksToFit(chunks, MaxTokens)` pool in
both modes (`longcontext_consume.go` — only the *ordering* differs: flat
sandwich-orders it, map_reduce keeps score order for grouping), and the eval
adapter re-sorts by score before cutting at k. The retrieval spread above is
therefore ANN/reranker nondeterminism, **not** a mode effect; the 6.2 pp MRR
gap between the two nominally identical flat runs makes that plain.

### Noise band

| Metric | same-flag mean gap \|(c)−(a)\| | mean \|per-question delta\|, same flag | SE of the n=12 mean | (b)−(a) |
|---|---|---|---|---|
| answer relevance | 0.000 | 0.000 | 0.000 | **+0.000** |
| faithfulness | 0.016 | **0.354** | 0.141 | **−0.048** |
| context precision | 0.011 | 0.130 | 0.054 | **+0.191** |

The middle two columns matter more than the first. The two flat runs' *means*
happen to land 1.6 pp apart on faithfulness, but that is cancellation, not
stability: individual questions move by up to a full point between two runs of
the *identical* configuration (G07 0.000 → 1.000, G06 0.857 → 0.083, G01
1.000 → 0.357). The implied standard error of a 12-question faithfulness mean
is ≈ 0.14, so a 1.6 pp same-flag gap is a lucky draw and must not be quoted as
the band.

## Decision

**Recommendation: keep `chat_longcontext_mode = flat` as the default.**
`map_reduce` stays a documented opt-in.

The brief's decision rule — adopt `map_reduce` for the route iff *mean answer
relevance improves beyond the noise band* **and** *mean faithfulness does not
drop beyond it* — fails on both arms:

1. **Answer relevance cannot improve: it is already saturated.** All 12
   questions scored 5/5 in run (a), all 12 again in (c), and 11 of 12 in (b)
   — the twelfth is a judge-parser failure, not a low score; its own reasoning
   text says 5 (see the anomalies below). The
   judge (`prompts.AnswerRelevanceSystemPrompt`) sees only *question +
   answer*, never the context, and grades a Likert 1–5 "does this address the
   question"; a 3000–5000-character structured German synthesis answer scores
   5 essentially by construction. This is a **measurement-instrument
   limitation, not a property of the two modes** — the primary metric has zero
   discriminative power on this route, and no configuration of the map-reduce
   path could have satisfied arm 1 as written.
2. **Faithfulness does not improve.** map_reduce is 4.8 pp *below* flat. That
   delta is only 0.34 × the standard error of the mean, so the honest reading
   is "no detectable difference" rather than "map_reduce is worse" — but the
   rule requires no drop, and there is certainly no gain to trade the cost
   against.

The cost side is unambiguous and was measured, not estimated: map_reduce adds
**25 fast-tier LLM calls per question** (25 groups × 8 chunks over the 200-chunk
pool) and takes **6.2× the context-assembly latency** (63.4 s vs 10.2 s mean
per question; 954 s vs 563 s wall for the set). Flipping the default would put
that on every global-synthesis turn in exchange for no measured answer-quality
gain.

**The one metric that does move** is context precision: 0.409 → 0.600, +19.1 pp
at 3.5 × the standard error — comfortably outside the noise. That is the
findings-based CONTEXT block doing exactly what W3-R6 designed it to do (the
answer LLM is handed extracted claims instead of 200 raw chunk bodies). It is
not part of the decision rule (and W3-R8 explicitly de-scoped it for this
route, because the judge grades one boolean per top-10 item against a 200-chunk
pool), so it is recorded as supporting evidence for keeping map_reduce
available, not as grounds for making it the default.

## Per-question anomalies (reported, not averaged away)

- **Two map-group timeouts on G02** (run b):
  `{"msg":"longcontext.map_group_failed","group":13,"chunks":8,"error":"context
  deadline exceeded"}` and the same for group 23. The W3-R7 fallback did its
  job — those two groups contributed raw first-600-rune findings instead of
  extracted ones, which is why G02 reports 207 findings against a 25-group /
  200-chunk pool where the other questions report 33–156. G02 was also the
  slowest question in the set at 134 s. No question errored and no answer was
  lost; this is the designed degradation, observed live.
- **G07's answer-relevance judge call failed to parse** in run (b):
  `answer_relevance: response is not valid JSON: "{\"score\":\"5\", …"`. The
  model emitted the score as a JSON *string* (`"5"`) instead of a number and
  the judge's strict decoder rejected it. The metric is simply absent for that
  question (the mean is over the remaining 11) — it is not a zero. Worth a
  follow-up: the judge decoder could accept a numeric string, since the same
  model produced a valid object with the right value.
- **G07's map_reduce answer is 1078 characters** against 2600 (a) / 2709 (c) in
  flat — the largest length regression in the set, on the question with the
  *most* findings (156). Consistent with W3-R6's accepted cost: the reduce
  stage can only use what a finding surfaced.
- **G04 scored faithfulness 0.000 in run (b)** with recall 0.000 in the same
  run (0.083 in (a), 0.167 in (c)). Its retrieval simply missed the curated
  cluster that run; the faithfulness score is downstream of that, not an
  independent second failure.
- **A `context_precision` judge warning recurs across runs** — `judge returned
  11 booleans, expected 10` (G02 in (a) and (c), G04 and G07 elsewhere). The
  judge occasionally emits one extra boolean for a 10-item list. Pre-existing,
  independent of this task, and it only drops that one question's context
  precision.

## Artifacts

Under `.superpowers/sdd/2026-09-06-rag-sota-wave3/` (gitignored workspace):
`t4-a-flat.json` / `.log` / `.walltime`, `t4-b-mapreduce.*`,
`t4-c-flat2.*`, plus `t4-analyse.py` (per-question table + deltas, scrapes
`rag.longcontext.map_reduce` group counts out of the logs), `t4-stats.py`
(the noise table above) and `t4-validate-golden.py` (fixture validator:
trigger verbatim-ness, file-name existence against the live KB, row shape).

## §2 Wave 4 re-measurement (pending)

**Status:** only Step 1 (author `expected_points` + validate the loader) is
done as of this record. Steps 2–4 (judged flat×2 / map_reduce×2 runs,
pairwise comparison, the W4-R7 decision) are a separate dispatch (5b),
deliberately not started here — the Task 6 BM25 grid was still running on
the same dev stack and LLM backend (`t6-grid.lock` pid live, `t6-grid-resume.log`
mid-cell `C1`) when this record was written, and both a judged run and the
grid would contend for the same model server. This section will be filled in
by 5b once the grid finishes.

### `expected_points` curation (Task 5 / W4-R5)

Added to all 12 rows of `eval/golden/global-synthesis-de.jsonl` (gitignored;
the file itself is not committed — this table is the reviewable record).
Points were authored **only** from the cited source documents' chunk text
(`document_chunks_4096` in the dev vector DB, `justrag-vectordb-1`), read via
read-only `psql` against `justrag-db-1` (file-id resolution by name) and
`justrag-vectordb-1` (chunk content), never from a model answer. 45 points
total across 12 questions (3–5 per question, all within the loader's 2–6
range; longest point well under the 300-rune cap).

| Q | Points | Source files verified against (name, as in `must_cite_file_names`) |
|---|---|---|
| G01 | 5 | SAP - Migration auf S4HANA - Go4S4.md; Windows 10-Ablösung.md; Außerbetriebnahme altes IMAP-E-Mail-System (Dovecot).md; Migration KEMP-Loadbalancer zu VMware AVI.md; Folio Einführung des cloudbasierten Bibliothekssystems und Ablösung LBS.md |
| G02 | 4 | Smarte Administration mit KI.md; KI-HUB für innovative Forschung.md; KI-Infrastruktur und KI-Plattformen Konzeption zentral abgestimmter Planung, Beschaffung und Auslast.md; Weiterentwicklung der zentralen JLU-KI-Services (HAWKI, HRZ-API-Service und weitere Schnittstellen; .md |
| G03 | 4 | Datenträgerverschlüsselung.md; MFA für Admins Multifaktorauthentifizierung für IT-Admins.md; LAPS-Upgrade.md; Informationssicherheit - wann ist der ISB zu beteiligen.md; Aktualisierung Sicherheitskonzept HRZ.md; Informationssicherheits-Richtlinien Überarbeitung und Visualisierung (PoliciesVis).md |
| G04 | 4 | JLU Future Data Center - Teil 1.md; Migration Datacenter Netzwerk Optimierung Firewall, Router und Switche.md; Austausch USV 1 & 2 Erneuerung unterbrechungsfreie Stromversorgung Serverräume 1 & 2.md; Machbarkeitsstudie eines Herstellerwechsels im Bereich WLAN.md |
| G05 | 3 | ILIAS - Stud.IP Schnittstelle.md; HISinOne MoveON Schnittstelle.md; ILIAS-Update.md; Stud.IP-Update.md |
| G06 | 3 | Workshop Agenda und Inhalte.md (comment thread); Raumübersicht Workshop.md; Infos für Human Digitals Orga Workshop am 25.11.2025.md; Save the Date und E-Mailverteiler für Einladung.md; Orga Verwaltungsworkshop 15.04.2026.md |
| G07 | 4 | Zusammenfassung Ergebnisse Workshop.md; Thementisch 2 Kulturwandel & Qualifikationsbedarfe für KI an der JLU.md; Thementisch 8 Kulturwandel & Qualifikationsbedarfe für KI an der JLU.md; Thementisch 4 Ethik & Gesellschaft.md; Thementisch 7 Ethik & Gesellschaft.md |
| G08 | 4 | RWTH Aachen.md; Universität Hamburg.md; Stanford University.md; ETH Zürich.md |
| G09 | 4 | Moderne AuthN-Infrastruktur Sicherere und zeitgemäße Authentifizierung von Nutzenden.md; Moderne AuthN-Infrastruktur Aufbau einer Produktivumgebung.md; Umstellung der Authentifizierung von LDAP auf Shibboleth für Stud.IP und ILIAS.md; Erneuerung OpenLDAP-Server Infrastruktur der zentralen Authentifizierung.md |
| G10 | 4 | Digitalisierung der Aktenführung in der Rechtsabteilung (B1) - Einführung der Kanzleisoftware AnNo.md; Digitales Anforderungsformular Beschaffung FB11.md; Einführung ESS (Employee Self Service) in SAP-HCM.md; Pilotierung Workflowmanagement FormCycle.md |
| G11 | 4 | Prozess der DigITal-Projektentwicklung Welche Status durchläuft ein Projekt.md; Rollendefinition Projektleitung und Projektkoordination.md; Priorisierungs-Methodik im Digital-PPM.md; Must-Have vs. Entscheidbar Einordnung von Projekten.md |
| G12 | 4 | Client-Management-Rollout Softwaremanagement durch Baramundi.md; Windows 10-Ablösung.md; M365-Planung.md; Software Asset Management.md |

Full point text is not reproduced here since the golden set stays gitignored
and untracked; this table exists so the curation choices (which facts, from
which files) are reviewable without the jsonl. The full text lives in
`eval/golden/global-synthesis-de.jsonl` locally.

**Points deliberately left out** (no reliably verifiable fact found in the
skimmed chunk text within task scope, or the field was template boilerplate
without a filled-in value): exact `Projektende` dates for several rows in the
G01/G04 clusters (the Confluence "Steckbrief" template's `Projektende`
field is frequently edited via changelog entries rather than a stable final
value — e.g. Windows 10-Ablösung's own changelog revises its end date five
times), named `Projektleitung` persons (present in some files but not
central to what a *synthesis* answer needs to state, and inconsistent in
format across files), and G03's/G09's remaining cluster members beyond the
ones cited (Passwortverwaltung-Evaluation, Zentraler Log-Server, Wiederanlaufpläne,
IAM-Erweiterungen, AD-Kompartmentkonzept, Schnittstelle CAFM-IAM, Benutzeranlage
SAP-IAM, Account-/Beschäftigten-Lifecycle-Prozesse) — each cluster's 3–5
points already cover the question's ask (technical-vs-organizational split
for G03; abolished-vs-newly-built for G09) without exhaustively re-stating
every file in `must_cite_file_names`, per the brief's "2–6 points" ceiling.

### Loader validation smoke (Step 1)

```bash
bash .superpowers/sdd/2026-09-06-rag-sota-wave4/run-eval.sh \
  --golden eval/golden/global-synthesis-de.jsonl --question-id G01 \
  --production-context --longcontext on --longcontext-mode flat \
  --output .superpowers/sdd/2026-09-06-rag-sota-wave4/t5a-smoke.json
```

Ran once, single question, no `--judge` (retrieval + answer generation only —
permitted alongside the Task 6 grid per the Task 5 hand-off, since it is one
short run rather than a repeated/long measurement). Result: `errors = 0`,
`eval.orchestrator_dispatch` shows `query_type=complex_reasoning`,
`orchestrator=longcontext`; `rag.longcontext.fired mode=flat`; report written
to `t5a-smoke.json` with `mean_recall=0.500 mrr=1.000 mean_ndcg=0.984`
(retrieval-only numbers, expected to be noisy at n=1 and not the point of
this smoke — the point is that `ParseGoldenSetContent`/the loader accepted
every row, G01's `expected_points` included, without a validation error).
Exit code 0 (`eval-exit=0`). No `--judge`, so the coverage judge itself did
not run in this smoke; that is Step 2 in dispatch 5b.
