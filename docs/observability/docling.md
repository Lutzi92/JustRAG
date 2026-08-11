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
convert, walks `body.children` in reading order, and locates each item's text
back in the markdown to build `ParseResult.PageSpans` (offset → page). Chunk
page metadata is then derived from each chunk's offset, so a chunk crossing a
page break is cited as a range (`S. 3–4`).

When a document has no usable provenance (unpaginated formats, or an item's
text cannot be matched back into the markdown), chunks carry **no** page
metadata and the UI simply omits the page label. That is deliberate: an absent
page reads as unknown, whereas a fabricated one silently misleads.

**Gotcha (fixed 2026-08-11):** the client previously looked for a
`document.pages[]` array that docling-serve has never emitted, and fell back to
labelling the whole document page 1 — so every Docling-parsed PDF cited "S. 1"
regardless of where the quote came from. Deployments running Docling before
this fix must **re-ingest their PDFs**; the wrong page numbers are baked into
existing chunk metadata at ingest time. The unit-test mocks asserted the
invented shape, so CI stayed green; `integration_test.go` (run with
`DOCLING_TEST_URL=…`) now pins the contract against a live sidecar.

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
