# Docling sidecar (layout-aware PDF + DOCX + PPTX parsing)

JustRAG can route PDF, DOCX, and PPTX parsing through Docling Serve for table-,
equation-, footnote-, and heading-aware extraction. Opt-in.

## Run

```
docker compose -f docker-compose.yml -f docker-compose.docling.yml up -d
```

Cold start downloads model weights (~3 GB). Subsequent restarts reuse the
`docling-cache` named volume.

## Configure

In the admin Agent panel:
- `docling_enabled` = `true`
- `docling_base_url` = `http://docling:5001` (in-compose hostname; in k8s use the
  Service DNS, e.g. `http://docling.justrag.svc.cluster.local:5001`)

## Health check

```
curl http://localhost:5001/health
```

## Image captioning (figures inside docs + standalone image uploads)

Opt-in on top of `docling_enabled`. When on, Docling describes substantive
images with a vision model and the caption lands inline in the markdown, so it
flows through the normal text chunking + embedding pipeline and becomes
retrievable (citations point back to the source page). No multimodal
embeddings, no DB migration.

In the admin Agent panel:
- `docling_picture_description_enabled` = `true` (default off)
- `docling_picture_area_threshold` = `0.05` (skip images below 5% of page
  area — filters logos/icons/decorative bullets; range [0,1])
- `docling_table_mode` = `accurate` (better structured tables → cleaner
  markdown; default `fast`)

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

Captioning also extends per-document convert latency, so raise
`DOCLING_TIMEOUT_SECONDS` on go-server / go-worker when it's enabled.

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

**Two gotchas, both fixed 2026-08-11 — and both invisible to the unit suite:**

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

Both shipped green because the mocks asserted a response shape nobody had
verified against a real sidecar. `integration_test.go` now pins the contract
against a live instance:

```
DOCLING_TEST_URL=http://localhost:5001 go test ./internal/parser/docling -run Integration -v
```

It converts a 10-page fixture built specifically from what broke the anchoring
(per-page markers, running header/footer, repeated boilerplate, escaped
characters) and asserts every marker lands on its own page, plus a second
fixture asserting a real detected table survives re-rendering.

**Page metadata is written at ingest time**, so any deployment that ran Docling
before this fix must **re-ingest its PDFs**. Note that chunk dedup is
KB-scoped: re-uploading the same file into the same KB drops every chunk as a
duplicate and changes nothing. Delete the old file first, or re-ingest the KB.

## Performance

- 1-page PDF: ~2–5 s.
- Complex 20-page paper with tables/equations: ~20–60 s.
- Default request timeout: 300 s. Override via the `DOCLING_TIMEOUT_SECONDS`
  env var on go-server / go-worker if needed.

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
