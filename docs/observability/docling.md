# Docling sidecar (layout-aware PDF + DOCX + PPTX parsing)

JustRAG can route PDF, DOCX, and PPTX parsing through Docling Serve for table-,
equation-, footnote-, and heading-aware extraction. Opt-in.

## Run

```
docker compose -f docker-compose.yml -f docker-compose.docling.yml up -d
```

Cold start downloads model weights (~3 GB). Subsequent restarts reuse the
`docling-cache` named volume.

The image is **pinned** (`quay.io/docling-project/docling-serve:v1.32.0` in both
`docker-compose.docling.yml` and `k8s/docling.yml`). `:latest` is a moving
target: the `picture_description_api` request field this integration sends is
deprecated upstream since docling-serve 1.21, and a removal would land as "every
PDF silently falls back to pdftotext" (see *Behaviour & fallback*). Bump the tag
deliberately and re-run the live integration tests below afterwards.

## Configure

In the admin Agent panel:
- `docling_enabled` = `true`
- `docling_base_url` = `http://docling:5001` (in-compose hostname; in k8s use the
  Service DNS, e.g. `http://docling.justrag.svc.cluster.local:5001`)
- `docling_table_mode` = `accurate` (default; `fast` trades table structure
  for speed)
- `docling_ocr_languages` = `de,en` (default; comma list, sent as docling's
  `ocr_lang`). Docling's automatic OCR engine on Linux is RapidOCR, whose
  docling-serve default language list is English + Chinese; German scans need
  `de` here or umlauts and word spacing come out wrong.
- `docling_force_ocr` = `false` (default). `true` OCRs every page and replaces
  the PDF text layer — for KBs that are mostly scans or PDFs with a broken
  text layer (garbage characters, no spaces).
- `docling_document_timeout_seconds` = `600` (default). Docling's own
  per-document processing limit; the server default is a week.

`docling_enabled` and `docling_base_url` are read at worker start (they decide
which parsers get registered). **Every other `docling_*` key is re-read on each
conversion**, so an admin-panel edit applies to the next file without a worker
restart.

## Health check

```
curl http://localhost:5001/health
```

## Image captioning (figures inside docs + standalone image uploads)

Opt-in on top of `docling_enabled`. When on, Docling describes substantive
images with a vision model, and the description flows through the normal text
chunking + embedding pipeline and becomes retrievable (citations point back to
the source page). No multimodal embeddings, no DB migration.

A figure contributes two pieces of text, both attributed to the page the figure
sits on: the **caption printed in the document** and the **vision model's
description**. Neither is labelled — a printed caption already announces itself
("Abbildung 3: …"), and a prefix invented here would be embedded into every
figure chunk in the corpus. See *Page numbers* below for why they need explicit
handling rather than arriving with the markdown.

**The sidecar must run with `DOCLING_SERVE_ENABLE_REMOTE_SERVICES=true`.** Docling
refuses to build a pipeline that calls a remote vision API unless that is set,
the refusal happens at pipeline construction (before any page is processed, so
`abort_on_error` cannot soften it), and the sync endpoint then answers HTTP 404
"Task result not found". The Go `FallbackParser` treats that like any other
error, so the observable effect of turning captioning on against a sidecar
without the flag is that **every PDF, DOCX and PPTX silently goes to pdftotext
and the legacy parsers** — one warn line per file, no tables, no page-exact
provenance. Both shipped manifests set the flag; the worker additionally
**probes** for it at startup whenever captioning is on (it converts a tiny
embedded PDF with the live options) and logs at error level if the sidecar
rejects the request.

In the admin Agent panel:
- `docling_picture_description_enabled` = `true` (default off)
- `docling_picture_area_threshold` = `0.05` (skip images below 5% of page
  area — filters logos/icons/decorative bullets; range [0,1])
- `docling_picture_description_prompt` — what the vision model is asked per
  figure. The default asks, in German, for the document's language, the
  figure type, what it shows, and **every readable number, axis label, legend
  entry and caption verbatim** — and, for a chart, **the data series as a
  markdown table** of the read-off values. That table is the "external chart
  extraction": docling's own chart stage can only run its bundled local model
  (see below), while gemma-4 reads a bar chart into `| Jan | 12 |` rows just as
  well (verified on `testdata/figure-2p.pdf`, every bar with its month). The
  table lands in the figure's chunk, so the values are retrievable and quotable.
  Docling's own default ("Describe this image in a few sentences.") produced
  English one-liners without values.
- `docling_picture_description_timeout_seconds` = `120` (default). Docling's
  default is 20 s per image, and a timed-out image simply has no description —
  no error, no log — which made caption coverage look random under GPU load.

Two settings ride along without a key: pictures docling classifies as `logo`,
`icon`, `signature`, `stamp`, `bar_code`, `qr_code` or `page_thumbnail` are
never sent to the vision model (classification is always on with captioning,
it is a cheap local model), and vision calls run one at a time per document
(docling's default), because the replica cap below is the throttle.

**The vision endpoint + API key are injected by the Go backend, not stored on the
sidecar.** On each convert request the Go client sends a `picture_description_api`
config (`url` + `params.model` + an `Authorization: Bearer` header) sourced from
the **admin AI provider config** — the same endpoint + key the rest of the app
uses — so the model-API credential never lives on the Docling sidecar. This is
required when the model API needs authentication. The vision **model** follows
`describe_image_model` (→ `model_tier_fast`), the same setting as
`/api/describe-image`. Set that to your vision-capable model (e.g.
`jlu/gemma-4-26b-it`). Docling still has to be able to **reach** that model URL
from its container/pod (network reachability only — no secret).

When captioning is on, **standalone image uploads** (`.png`/`.jpg`/…) also route
through Docling (vision caption **and** OCR) instead of the Tesseract-OCR-only
path; Tesseract remains the fallback when Docling is down or disabled.

### GPU-contention caveat (important)

Docling's vision calls to gemma-4 do **not** pass through the app's
`AI_MAX_CONCURRENT_REQUESTS` ceiling (that governs only the app's own calls). A
burst of image-heavy ingestion can have Docling hammering the same gemma-4 that
serves live chat and starve interactive answers. The throttle is therefore at the
Docling layer: **cap Docling replicas + its own request concurrency** (see the
fixed `replicas` and the rationale comment in `k8s/docling.yml`). Only raise the
replica count once you give ingestion its own gemma-4 instance.

Captioning also extends per-document convert latency. The worker converts
through docling-serve's **task endpoints** (`POST /v1/convert/file/async` →
`GET /v1/status/poll/{id}` every 2 s → `GET /v1/result/{id}`), bounded only by
`DOCLING_TIMEOUT_SECONDS` (default 300) for the whole exchange. The
synchronous `/v1/convert/file` is used only when the sidecar has no task
endpoints (404 on submit — a pre-1.0 image), and that path is additionally
capped by the sidecar's `DOCLING_SERVE_MAX_SYNC_WAIT` (upstream default
**120**), after which it answers 504 regardless of the client. The manifests
set it to 600 for the benefit of curl and the tests' fallback.

### Why the deprecated `picture_description_api` field is still sent

docling-serve 1.21 deprecated `picture_description_api` in favour of
`picture_description_preset` / `picture_description_custom_config`. On the
pinned 1.32.0 the custom config was tried live: it validates only as the
nested VLM-engine shape (`engine_options.engine_type: "api"`, a `model_spec`
with `prompt` + `response_format`, `api_overrides.api.params`), and any
endpoint URL or header placed in it is **silently ignored** — the sidecar
logs "Initializing PictureDescriptionVlmEngineModel … engine=api", calls
nothing, and the picture comes back with `description: null`. In the new
system the endpoint and its headers are meant to live **server-side**, in a
named preset (`DOCLING_SERVE_CUSTOM_PICTURE_DESCRIPTION_PRESETS`), which puts
the model-API key on the sidecar. The per-request legacy field still works
on 1.32.0 and keeps the key out of the sidecar, so that is what is sent. When
an upgrade removes it, the migration is: define the preset on the sidecar
from a Secret and send `picture_description_preset` instead.

## Behaviour & fallback

- When enabled and reachable, Docling parses every new PDF, DOCX, and PPTX file.
  Gains over the built-in parsers: table structure preserved as markdown,
  footnotes extracted (previously silently dropped by the DOCX parser),
  heading hierarchy surfaced as `sections` chunk metadata (same shape as
  `.md` files); for PPTX, slide titles are surfaced as headings and speaker
  notes are retained. With image captioning on (above), figures/charts inside
  the document are described inline too.
- When disabled OR unreachable OR returning errors, JustRAG silently falls
  back to the built-in parsers (`pdftotext` for PDF, the custom DOCX parser
  for DOCX, the built-in PPTX parser for PPTX). Failures are logged at warn
  level with `request_id` for grep-correlation.
- Already-ingested files are not retroactively re-parsed. Re-ingest a KB to
  benefit on existing data.

## Page numbers (citations)

Docling returns the document as one markdown blob; page numbers exist **only**
as provenance inside the `json_content` DoclingDocument (`prov[].page_no`).
JustRAG therefore requests `to_formats=md` **and** `to_formats=json` on every
convert, walks `body.children` in reading order, and rebuilds **per-page text
from the items themselves** — headings, lists and code by label, tables
re-rendered from the cell grid (`table_cells` row/column offsets), furniture
(`content_layer: furniture` — running headers, footers, page numbers) dropped
exactly as docling drops it from its own markdown. Each page is then chunked
independently, so a chunk's page is exact, never inferred. This is the same
shape the `pdftotext` path has always produced.

When a document carries no page provenance at all (unpaginated formats, or a
sidecar that returned no `json_content`), the markdown blob is ingested with
**no** page metadata and the UI omits the page label. That is deliberate: an
absent page reads as unknown, a fabricated one silently misleads.

Because the page text is rebuilt from items, **every kind of content has to be
walked explicitly** — anything the walk does not resolve is dropped, and the
markdown blob is no longer there to catch it. What the walk carries today:

- **Texts** by label (title, section headers with their `level` — the sidecar
  is asked for `do_pdf_heading_hierarchy` so numbered headings, PDF outlines
  and font-size cues turn into real nesting instead of a flat list of `##`;
  that nesting is what the `sections` chunk metadata is built from), list
  items, code, formulas.
- **Tables** re-rendered from the cell grid, with the table's printed
  **caption** before and its **footnotes** after. Docling's reading-order
  stage parents captions and footnotes to the table itself and keeps them out
  of `body.children`, so they are reachable only through the table's
  `captions[]` / `footnotes[]` refs.
- **Pictures**: the printed caption (same `captions[]` mechanism), then the
  **text inside the figure** (docling parents every text item it finds inside
  a picture's bounding box to the picture — for a vector chart that is the
  axis values, bar labels and legend entries, i.e. the real numbers, and
  docling's own markdown omits them), then the vision description, then
  footnotes. The description lives on the picture in one of two places
  depending on the sidecar's docling-core version — the original
  `annotations[]` list, which upstream now marks deprecated, or
  `meta.description`. Both are read, `meta` first. A caption that *is* also a
  body child is emitted once, not twice.

Every response also carries docling's **confidence** block (docling-serve ≥
1.25: parse / layout / table / OCR scores and two letter grades). It is logged
per file as `docling.confidence` — at warn level when the low grade is `poor` —
so a badly parsed scan can be found by `request_id` and re-run with force OCR.

**Three gotchas, none of them visible to the unit suite:**

1. The client looked for a `document.pages[]` array that docling-serve has
   never emitted, then fell back to labelling the whole document page 1. Every
   Docling-parsed PDF cited "S. 1".
2. The first fix recovered page boundaries by searching each item's text back
   inside `md_content`. That cannot work: docling escapes markdown
   metacharacters (`max_value` → `max\_value`), omits furniture entirely, and
   re-renders tables, so a large share of items never match — and because the
   search offset only moves forward, every miss drags later pages' boundaries
   along with it. The result was confidently wrong page numbers, which is worse
   than the honest "S. 1" it replaced.
3. Fixing (2) by rebuilding page text from items introduced a third: the walk
   resolved only `texts`, `tables` and `groups`, so **`#/pictures/N` refs were
   skipped entirely**. From 2026-08-11 until this was fixed, every PDF figure
   was sent to the vision model, billed, and its caption thrown away — while
   `md_content` still contained it, which is exactly why nothing looked wrong.
   Only standalone image uploads kept working, because they have no page
   provenance and so fall through to the markdown blob. Re-ingest to pick up
   captions in already-parsed documents.

All three shipped green because the mocks asserted a response shape nobody had
verified against a real sidecar. `integration_test.go` now pins the contract
against a live instance:

```
DOCLING_TEST_URL=http://localhost:5001 go test ./internal/parser/docling -run Integration -v
```

It converts a 10-page fixture built specifically from what broke the anchoring
(per-page markers, running header/footer, repeated boilerplate, escaped
characters) and asserts every marker lands on its own page, plus a second
fixture asserting a real detected table survives re-rendering, plus a third
(`testdata/figure-2p.pdf`, a bar chart with a printed caption on page 2)
asserting the caption lands on the figure's page exactly once, plus a fourth
that sends the worker's default request (accurate tables, `de,en` OCR,
document timeout, heading hierarchy) through the startup probe and asserts the
pinned image accepts every field — a 422 here would mean every real
conversion falls back to pdftotext. Add the vision
endpoint to exercise captioning end to end — without it the third test checks
caption provenance only and says so in its log:

```
DOCLING_TEST_URL=http://localhost:5001 \
DOCLING_TEST_VLM_URL=https://<host>/v1/chat/completions \
DOCLING_TEST_VLM_MODEL=jlu/gemma-4-26b-it \
DOCLING_TEST_VLM_KEY=<key> \
go test ./internal/parser/docling -run Integration -v
```

## What was tried and not adopted (2026-09-05)

- **Chart extraction** (`do_chart_extraction`): on the pinned image the
  granite-vision-4.1-4b model is not bundled; the sidecar tries to download
  it at request time and fails to instantiate it, and the whole conversion
  fails. Even with pre-baked artifacts (`DOCLING_SERVE_ARTIFACTS_PATH`) a 4B
  vision model per chart is GPU-only in practice. The text docling finds
  *inside* a vector chart (see above) plus the value-extracting caption prompt
  cover the same need without it.
- **Docling's own chunker** (`POST /v1/chunk/hybrid/file`, form fields
  prefixed `chunking_` / `convert_`): works on 1.32.0 and returns chunks with
  `headings[]`, `doc_items[]` refs and `page_numbers[]`, so heading context and
  page-exact citations would survive. Not adopted: it would be a second
  chunking pipeline next to `internal/splitter`, and its tokenizer has to
  match the embedder. Worth an eval on one KB if the per-page rebuild keeps
  needing patches.
- **Formula enrichment**: no equation-bearing fixture in this corpus; the
  upstream memory-growth issue (docling #1886) is still open. Left off.

**Page metadata is written at ingest time**, so any deployment that ran Docling
before this fix must **re-ingest its PDFs**. Note that chunk dedup is
KB-scoped: re-uploading the same file into the same KB drops every chunk as a
duplicate and changes nothing. Delete the old file first, or re-ingest the KB.

## Performance

- 1-page PDF: ~2–5 s.
- Complex 20-page paper with tables/equations: ~20–60 s.
- Default request timeout: 300 s. Override via the `DOCLING_TIMEOUT_SECONDS`
  env var on go-server / go-worker if needed — **and** raise the sidecar's
  `DOCLING_SERVE_MAX_SYNC_WAIT` to match (see the captioning section).

## Resource sizing

Docling defaults to GPU when available. CPU-only is fine for low ingest
volumes (≤10 PDFs/hour). Set `docker compose ... up --scale docling=2` for
parallelism if a single instance is the bottleneck.

## Kubernetes

`k8s/docling.yml` runs Docling as its own Deployment + Service (it is a Python
service carrying ~3 GB of models — do **not** bundle it into the worker pods).
The manifest uses a **fixed low replica count and no HPA on purpose**: that cap
is the throttle for the shared-gemma-4 captioning path (see the caveat above).
Point `docling_base_url` at the Service DNS. Replace the model-cache `emptyDir`
with a PVC if you want to avoid the ~3 GB re-download on every pod restart.
