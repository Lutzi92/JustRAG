package siteconfig

import "context"

// BatchReader is the optional extension implemented by stores that can fetch
// multiple site_configs values in a single round-trip. Callers that use a
// per-key `Reader` interface in their public surface can type-assert it to
// `BatchReader` on the hot path; production stores (e.g. chat.PGStore)
// satisfy both, test fakes typically satisfy only the per-key form and fall
// back to per-key reads.
//
// Hoisted here so the chat and vector packages can share a single source of
// truth — divergent local re-declarations meant a method-signature change on
// the concrete store would update one consumer and silently break the other.
type BatchReader interface {
	GetSiteConfigValues(ctx context.Context, keys []string) (map[string]*string, error)
}
