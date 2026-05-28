//go:build !chromium

package fetcher

import "context"

// browserPool is a no-op type used in non-chromium builds. The real
// rod-backed implementation lives in browser.go behind //go:build chromium.
type browserPool struct{}

// fetchTier2 is a stub that returns ErrUnsupportedMode in non-chromium builds.
func (f *Fetcher) fetchTier2(ctx context.Context, rawURL string, opts Options) (*Result, error) {
	return nil, ErrUnsupportedMode
}

// closePools is a no-op in non-chromium builds — there are no pools to close.
func (f *Fetcher) closePools() error { return nil }
