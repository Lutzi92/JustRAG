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

### `expected_points` curation (Task 5 / W4-R5) — see "Fix round 1" below for corrections

Added to all 12 rows of `eval/golden/global-synthesis-de.jsonl` (gitignored;
the file itself is not committed — this table is the reviewable record).
Points were authored **only** from the cited source documents' chunk text
(`document_chunks_4096` in the dev vector DB, `justrag-vectordb-1`), read via
read-only `psql` against `justrag-db-1` (file-id resolution by name) and
`justrag-vectordb-1` (chunk content), never from a model answer. **67 points
total across 12 questions** (4–6 per question, all within the loader's 2–6
range; longest point 305→trimmed to ≤300 runes — see Fix round 1). The
original first pass (45 points, 3–5 per question) is superseded by the
corrected/broadened set below; this table lists the corrected state only.

| Q | Points | Source files consulted (name, as in `must_cite_file_names` unless noted) |
|---|---|---|
| G01 | 5 | SAP - Migration auf S4HANA - Go4S4.md; Windows 10-Ablösung.md; Außerbetriebnahme altes IMAP-E-Mail-System (Dovecot).md; Migration KEMP-Loadbalancer zu VMware AVI.md (incl. the E-Mail/ESA exclusion line); Folio Einführung des cloudbasierten Bibliothekssystems und Ablösung LBS.md |
| G02 | 6 | Planungsprojekt Neue Wege mit KI.md; EP Vermerk Neue Wege mit KI.md; Projektablaufplan Neue Wege mit KI.md; Projektabschlussbericht Neue Wege mit KI.md; Projektkonzeption Themengebiet KI an der JLU.md; Smarte Administration mit KI.md; KI-HUB für innovative Forschung.md; KI-Infrastruktur und KI-Plattformen Konzeption zentral abgestimmter Planung, Beschaffung und Auslast.md; Weiterentwicklung der zentralen JLU-KI-Services (HAWKI, HRZ-API-Service und weitere Schnittstellen; .md; Roadmap KI-Services JLU 2026 mit Ausblick 2027.md; Support-Chatbot mit Websearch, RAG und Website-Widget Bereitstellung des Systems.md; JLU KI Wissensdatenbanken Vorprojekt.md. Not consulted for a point (near-empty/directory chunks): Website KI an der JLU.md; Kerngeschäftsfähigkeiten mit KI.md |
| G03 | 6 | Datenträgerverschlüsselung.md; MFA für Admins Multifaktorauthentifizierung für IT-Admins.md; LAPS-Upgrade.md; Zentraler Log-Server.md; Microsoft-Unternehmenszugriffsmodell Sicherere Administration von Active Directory und M365.md; Aktualisierung Sicherheitskonzept HRZ.md; Informationssicherheits-Richtlinien Überarbeitung und Visualisierung (PoliciesVis).md; Informationssicherheit Policies & Visualisations 2026 (PoliciesVis 2026).md; Informationssicherheits-Audits 2026.md; Aktualisierung der Wiederanlaufpläne inkl. Disaster Recovery.md; Informationssicherheit - wann ist der ISB zu beteiligen.md. Not consulted (left out): Ausbau IT-Sicherheit.md; Passwortverwaltung in der zentralen IT Evaluation.md |
| G04 | 6 | JLU Future Data Center - Teil 1.md; JLU Future Data Center - Teil 2 (Plan und Bedarfsanmeldung Bau).md; Migration Datacenter Netzwerk Optimierung Firewall, Router und Switche.md; Austausch USV 1 & 2 Erneuerung unterbrechungsfreie Stromversorgung Serverräume 1 & 2.md; Erneuerung PDUs (Power Distribution UnitsStromverteilereinheiten Serverräume HRZ).md; Erneuerung Netzwerk-Standortverteiler Phil II.md; Gebäudeanbindung Unizentrum (Ertüchtigung und Modernisierung Glasfaser- und Kupfernetz).md; Kanalsanierung LWL  Kabel (Teilstrecke Glasfaserring).md; Erkundung LWL.md; Machbarkeitsstudie eines Herstellerwechsels im Bereich WLAN.md; WLAN LFE WLAN-Versorgung landwirtschaftlicher Lehr- und Forschungseinrichtungen.md; Umzug TK-Standort VetMed.md |
| G05 | 6 | CaMS  zentralfinanziertes Anschlussvorhaben (zfAV) (für HISinOne) zum Nationalen Once-Only-Technica.md; CaMS  Teilprojekt MVV - Einführung eines Modul- und Veranstaltungsverzeichnisses.md; CaMS  Projekt IDANOOTS+DSC.md; CaMS  Alumni Service - Alumni-Management Einführung von HIS-ALU.md; CaMS  Teilprojekt Langfristige Strategie für das Campusmanagement.md; Stud.IP-Raumverwaltung anpassen.md; Eventmanagement mit Stud.IP.md; Auswertung von Lehrraumbelegungen in Stud.IP.md; Stud.IP-Update.md; ILIAS-Update.md; **ILIAS-Update (V10).md**; ILIAS - Stud.IP Schnittstelle.md; HISinOne MoveON Schnittstelle.md; Weiterentwicklung MoveON.md; European Student Card Initiative (ESCI).md — all 15 must-cite files now represented (the 5-file CaMS cluster, previously absent, is point 1) |
| G06 | 4 | Workshop Agenda und Inhalte.md (comment thread); Raumübersicht Workshop.md; Infos für Human Digitals Orga Workshop am 25.11.2025.md; Save the Date und E-Mailverteiler für Einladung.md; Orga Verwaltungsworkshop 15.04.2026.md; 2025-11-14 Update Workshopplanung.md; Checkliste & Ablaufplan WS.md |
| G07 | 6 | Zusammenfassung Ergebnisse Workshop.md; Thementisch 2 Kulturwandel & Qualifikationsbedarfe für KI an der JLU.md; Thementisch 8 Kulturwandel & Qualifikationsbedarfe für KI an der JLU.md; Thementisch 4 Ethik & Gesellschaft.md; Thementisch 7 Ethik & Gesellschaft.md; Thementisch 1 Implementierung Konkret. Bereits begonnen.md; Thementisch 3 Abläufe und Strukturen.md; Thementisch 5 Technische Voraussetzungen.md; Thementisch 6 Ökosystem & Partnerschaften.md. Not independently cited (support/transcription docs): Thementische Implementierung und Abläufe & Strukturen.md; Transkription aus dem Workshop Übersicht.md; Transkription aus dem Workshop techn. Voraussetzungen, Ökosystem & Partnerschaften, Ethik und Gesel.md; Workshop-Bericht.md |
| G08 | 6 | RWTH Aachen.md; Universität Hamburg.md; Universität Heidelberg.md; **TU München.md**; Stanford University.md (corrected — see Fix round 1); ETH Zürich.md; UC Berkeley.md; University of Oxford.md; Uni Bonn.md; HU Berlin.md; Universität Tübingen.md; Universität Marburg.md; FU Berlin.md — 13 of 14 must-cite files now represented; Sammlung Best Practices KI und Hochschulen.md is a Confluence dashboard macro with no institution content of its own and is not cited |
| G09 | 6 | Moderne AuthN-Infrastruktur Sicherere und zeitgemäße Authentifizierung von Nutzenden.md; Moderne AuthN-Infrastruktur Aufbau einer Produktivumgebung.md; Umstellung der Authentifizierung von LDAP auf Shibboleth für Stud.IP und ILIAS.md; Erneuerung OpenLDAP-Server Infrastruktur der zentralen Authentifizierung.md; Erneuerung AD-Domaincontroller (u.a. Authentifizierungs-Server).md; AD-Kompartmentkonzept.md; IAM Erweiterungen Compliance, Prozessoptimierung und Self Services.md; Implementierung Account-Lifecycle-Prozesse für UKGM-administrierte Landesbedienstete.md; Implementierung Beschäftigten-Lifecycle-Prozesse für die Servicestelle Arbeitsmedizin.md; Benutzeranlage und -pflege in SAP durch IAM.md (title-level); MFA für Admins Multifaktorauthentifizierung für IT-Admins.md; Schnittstelle CAFM-IAM.md — all 12 must-cite files now represented |
| G10 | 6 | Digitalisierung der Aktenführung in der Rechtsabteilung (B1) - Einführung der Kanzleisoftware AnNo.md; Digitales Anforderungsformular Beschaffung FB11.md; Einführung der elektronischen Ausgangsrechnung.md; Einführung ESS (Employee Self Service) in SAP-HCM.md; Vorprojekt Einführung Workflowmanagementsystem Formcycle.md; Pilotierung Workflowmanagement FormCycle.md; Elektronische Signatur an der JLU - Einführung und Pilotierung.md; Elektronische Signatur an der JLU - Pilotprozesse und Rollout Zentralverwaltung.md; DMS  Aktenplanstruktur für das DMS.md; DMS  Elektronische Studierendenakte.md; DMS  Einführung digitales Vertragsmanagement.md; DMS  Einführung einer digitalen dokumentengestützten Vorgangsbearbeitung im hessischen Verbund.md (title/id-level only); Projekt Onlinezugangsgesetz.md (title-level only) — all 13 must-cite files now represented |
| G11 | 6 | Prozess der DigITal-Projektentwicklung Welche Status durchläuft ein Projekt.md (corrected to 6 statuses incl. 60-pausiert — see Fix round 1); Rollendefinition Projektleitung und Projektkoordination.md; Priorisierungs-Methodik im Digital-PPM.md; Must-Have vs. Entscheidbar Einordnung von Projekten.md; Dashboard JLU-Digitalprojekt-Portfoliomanagement.md; Umgang mit Projektsteckbriefen und Informationen im Digitalprojekt-Portfolio.md. Not consulted (near-empty/index pages): Kurzanleitung Erstellung Projektsteckbrief.md; Muster-Steckbrief mit Links zu Anleitungsartikeln.md; Steckbriefe JLU-Digitalprojekte.md; Steckbriefe HRZ.md; Projektportfolio - Steckbriefe.md; Präsidiumsentscheidungen.md (explicitly says its content doesn't exist yet) |
| G12 | 4 | Client-Management-Rollout Softwaremanagement durch Baramundi.md; Windows 10-Ablösung.md; M365-Planung.md; Software Asset Management.md (corrected — see Fix round 1) |

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
times); named `Projektleitung` persons; G03's Ausbau IT-Sicherheit.md and
Passwortverwaltung in der zentralen IT Evaluation.md (their content was not
independently distinguishing enough to add a further point within the
6-point cap once the other 11 files were merged into 6 points); G07's four
transcription/support documents (already represented via the summary point);
G08's Sammlung Best Practices KI und Hochschulen.md (a Confluence dashboard
macro, not an institution profile); G11's index/near-empty pages (Kurzanleitung,
Muster-Steckbrief, three Steckbriefe-listing pages, Präsidiumsentscheidungen).

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

### Fix round 1 (curation review corrections)

A curation review verified four questions against the chunk text directly
and found four defects, all fixed in place (base commit `24bd17d`):

1. **G08 point 3 misattributed content.** The original point attributed
   "Microsoft Copilot und Grammarly sowie ein Chatbot zu Lehrveranstaltungen"
   to Stanford. Re-reading both files' chunk text: that content is verbatim
   in `TU München.md` (the "TUMtutor" course chatbot, fed by lecturers,
   answers with sources, plus Copilot/Grammarly). `Stanford University.md`'s
   only chunk mentions "AI Playground", "Responsible AI" (a UIT governance
   page) and "AI News" — no chatbot. Fixed: split into two correct,
   contrasting points (TU München's TUMtutor+Copilot/Grammarly vs. Stanford's
   AI Playground/Responsible AI — point 2 in the corrected G08 set), and
   `TU München.md` is now in the source table.
2. **G12 point 4 read as a live initiative.** "Software Asset Management.md"
   was cited for a still-active SAM rollout. Its own changelog shows Status
   changed to "Red50-abgebrochen" on 13.07.2026 with the note "Umsetzung über
   organisatorische Alternativlösungen" (per ALB decision). Fixed: the point
   now states the abandonment and the organisational-alternative outcome
   instead of an ongoing rollout.
3. **G01 point 4 overclaimed "vollständig".** "Migration KEMP-Loadbalancer zu
   VMware AVI.md" states the goal is full retirement of the central KEMP
   instance by end-2027, but explicitly excludes E-Mail/ESA: "Eine Ablösung
   des KEMP-LB für E-Mail/ESA ist derzeit nicht geplant." Fixed: dropped
   "vollständig" and added the exclusion clause.
4. **Cluster-coverage rule (controller ruling, new).** For every question
   phrased "alle …", `expected_points` must cover every major cluster in
   `must_cite_file_names` (one point per cluster, up to the 6-point cap,
   small files merged into a shared point). G05 (15 files: the 5-file CaMS
   sub-cluster was entirely absent) and G08 (14 institution files, only 4
   represented) violated it outright. Re-checked G02, G03, G04, G06, G07,
   G09, G10, G11 against the same rule and broadened all of them except G06
   (already both-events-covered at 3 points; added a 4th, a genuinely new
   fact — the ≥110-registration waitlist — rather than padding). G01 and G12
   were left at their original point counts (not in the coordinator's
   re-check list; only their flagged factual errors were fixed). Net effect:
   total points went from 45 to 67; every "alle …" row except G01/G12 grew
   from 3–5 points to 6 (the cap), and files-represented-per-row rose
   sharply (e.g. G05 5→15 of 15 files, G09 4→12 of 12 files, G04 4→12 of 12
   files) — see the corrected source-file table above.

While re-verifying G11 for cluster coverage, a fifth, incidental error
surfaced and was fixed too: the original point 1 listed five project
statuses (10/20/30/40/50); `Prozess der DigITal-Projektentwicklung...md`'s
own chunk text lists **six** — `60-pausiert` ("Projekt vorübergehend
unterbrochen") was missing. Corrected in place.

**Re-validation (loader):** a throwaway Go program
(`go-backend/cmd/evalcheck-tmp5a/main.go`, created, run, and deleted — never
committed) called `eval.LoadGoldenSet` directly on the corrected file. This
needs neither a DB connection nor the LLM backend, so it ran regardless of
the Task 6 grid's state:

```
OK: loaded 12 questions
  G01: expected_points=5   G02: expected_points=6   G03: expected_points=6
  G04: expected_points=6   G05: expected_points=6   G06: expected_points=4
  G07: expected_points=6   G08: expected_points=6   G09: expected_points=6
  G10: expected_points=6   G11: expected_points=6   G12: expected_points=4
```

**Re-validation (smoke):** `t6-grid.lock` still named a live pid at fix time
(cell `C2` running per `t6-grid-resume.log`), so per the coordinator's rule
one additional single-question, no-`--judge` smoke was run on a question
this round actually changed:

```bash
bash .superpowers/sdd/2026-09-06-rag-sota-wave4/run-eval.sh \
  --golden eval/golden/global-synthesis-de.jsonl --question-id G08 \
  --production-context --longcontext on --longcontext-mode flat \
  --output .superpowers/sdd/2026-09-06-rag-sota-wave4/t5a-fix1-smoke.json
```

Result: `errors = 0`, `orchestrator=longcontext`, `eval-exit=0`
(`mean_recall=0.214 mrr=0.250 mean_ndcg=0.540` — retrieval-only, noisy at
n=1, not the point of the smoke). The corrected file's `expected_points`
(G08 now 6 points) loaded without a validation error.

The corrected `eval/golden/global-synthesis-de.jsonl` was copied over the
main checkout's copy (`/home/steffen/git/JustRAG/eval/golden/global-synthesis-de.jsonl`,
also gitignored) so both working copies hold the same fixed fixture.
