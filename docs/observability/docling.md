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
- `docling_base_url` = `http://docling:5001` (in-compose hostname)

## Health check

```
curl http://localhost:5001/health
```

## Behaviour & fallback

- When enabled and reachable, Docling parses every new PDF, DOCX, and PPTX file.
  Gains over the built-in parsers: table structure preserved as markdown,
  footnotes extracted (previously silently dropped by the DOCX parser),
  heading hierarchy surfaced as `sections` chunk metadata (same shape as
  `.md` files); for PPTX, slide titles are surfaced as headings and speaker
  notes are retained.
- When disabled OR unreachable OR returning errors, JustRAG silently falls
  back to the built-in parsers (`pdftotext` for PDF, the custom DOCX parser
  for DOCX, the built-in PPTX parser for PPTX). Failures are logged at warn
  level with `request_id` for grep-correlation.
- Already-ingested files are not retroactively re-parsed. Re-ingest a KB to
  benefit on existing data.

## Performance

- 1-page PDF: ~2–5 s.
- Complex 20-page paper with tables/equations: ~20–60 s.
- Default request timeout: 300 s. Override via the `DOCLING_TIMEOUT_SECONDS`
  env var on go-server / go-worker if needed.

## Resource sizing

Docling defaults to GPU when available. CPU-only is fine for low ingest
volumes (≤10 PDFs/hour). Set `docker compose ... up --scale docling=2` for
parallelism if a single instance is the bottleneck.
