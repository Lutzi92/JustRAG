//go:build !chromium

package fetcher

import "context"

// justFindCtx is a no-op type used in non-chromium builds. The real
// rod-backed implementation lives in justfind.go behind //go:build chromium.
//
//nolint:unused // used by the chromium build-tag variant (lint runs without the tag)
type justFindCtx struct{}

// fetchTier3 is a stub that returns ErrUnsupportedMode in non-chromium builds.
func (f *Fetcher) fetchTier3(ctx context.Context, rawURL string, opts Options) (*Result, error) {
	return nil, ErrUnsupportedMode
}
