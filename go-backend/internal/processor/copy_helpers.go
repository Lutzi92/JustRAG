package processor

import (
	"context"

	"github.com/justrag/go-backend/internal/logctx"
)

// Helpers for the worker's copy mode (P2-R6), which serves a library file by
// copying another KB copy's index instead of running ProcessFile. Each one
// resolves exactly as the ingest path does, so copy mode cannot drift from it.

// libraryParseCacheKey is the parse-cache key for a library file under p's
// (already overlay-resolved) parse configuration, or "" when the file is not
// cacheable (no cache, no owner/user file, or a spreadsheet/image/audio type).
func (p *Processor) libraryParseCacheKey(ctx context.Context, ownerUserID, userFileID, mimeType, fileName string) string {
	if userFileID == "" || ownerUserID == "" || p.parseCache == nil || !parseCacheable(mimeType, fileName) {
		return ""
	}
	return ParseCacheKey(ownerUserID, userFileID, ParseConfigHash(ctx, p.siteConfigReader, p.parseCache.Identity()))
}

// CachedParseText returns the cached parse text the ingest path would reuse
// for this library file in kbID, if there is one. A read failure is a miss.
func (p *Processor) CachedParseText(ctx context.Context, kbID, ownerUserID, userFileID, mimeType, fileName string) (string, bool) {
	p = p.withKBConfig(ctx, kbID)
	key := p.libraryParseCacheKey(ctx, ownerUserID, userFileID, mimeType, fileName)
	if key == "" {
		return "", false
	}
	r, ok, err := p.parseCache.Get(ctx, key)
	if err != nil {
		logctx.From(ctx).Warn("processor: parse cache read failed", "userFileId", userFileID, "error", err)
		return "", false
	}
	if !ok || r == nil {
		return "", false
	}
	return r.Text, true
}

// IngestRunsKG reports whether an ingest of a (non-spreadsheet) file in kbID
// runs KG extraction (overlay-resolved): kg_extraction_enabled, and neither
// parent-child nor late chunking, whose paths skip the post-embed tail. Copy
// mode rebuilds the KG only when this holds, so it never builds a graph the
// donor's ingest did not.
func (p *Processor) IngestRunsKG(ctx context.Context, kbID string) bool {
	p = p.withKBConfig(ctx, kbID)
	return ingestRunsKG(ctx, p.siteConfigReader)
}

// CopyEligible reports whether a library file may be served by copy mode at
// all: not a spreadsheet (its parse is the tabular ingest) and not an image
// or audio file, whose index depends on the KB's vision / STT provider, which
// the index fingerprint does not cover — the parse cache's exclusion set.
func CopyEligible(mimeType, fileName string) bool {
	return parseCacheable(mimeType, fileName)
}

// TextSearchConfig returns the Postgres text-search config for kbID's
// language ("simple" when it cannot be resolved), as the ingest path uses for
// the BM25 tsvector.
func (p *Processor) TextSearchConfig(ctx context.Context, kbID string) string {
	return p.resolveKBLanguage(ctx, kbID)
}
