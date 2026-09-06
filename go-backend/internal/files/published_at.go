package files

import "time"

// ClampPublishedAt bounds a source-supplied publication date at `now`.
//
// files.published_at is source-controlled: whatever the feed's <pubDate>, the
// Confluence page version or the git HEAD commit says lands in the column,
// and the effective date COALESCE(published_at, created_at) drives the
// recency boost, the recency listing and every date window. A single item
// dated in the future would therefore be permanently "the newest document in
// the KB" — outranking real news on every freshness-sensitive turn until
// someone deleted it. Past dates are left exactly as the source reported them
// (back-dating is legitimate: an advisory published last week and ingested
// today, or a repository whose HEAD commit is months old).
//
// A nil input stays nil — "no date" must not become "today", or
// COALESCE(published_at, created_at) would be a no-op.
//
// This lives in internal/files, next to the column it guards, because three
// ingest paths in three packages (worker/rsspoll, confluence, gitrepo) write
// published_at and all of them already depend on internal/files; a per-package
// copy of the clamp is exactly the kind of drift that lets one source skip it.
func ClampPublishedAt(t *time.Time, now time.Time) *time.Time {
	if t == nil || !t.After(now) {
		return t
	}
	clamped := now.UTC()
	return &clamped
}
