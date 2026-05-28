package ai

import "testing"

// newTestResolverWithCompletion returns a *ConfigResolver wired with fn as the
// completion hook for the duration of tb. Production paths that take a
// *ConfigResolver work unchanged — fn intercepts every completion call dispatched
// through (*ConfigResolver).completionFn.
//
// Lives in a _test.go file so the testing import stays out of production binaries
// that link internal/ai. The atomic swap is race-free, so tests in this package
// may use t.Parallel without tripping go test -race; the t.Cleanup unwind keeps
// sibling tests isolated.
//
// Replaces the previous package-level completionHook global with per-resolver
// state — see external code review item #2.
func newTestResolverWithCompletion(tb testing.TB, fn completionFunc) *ConfigResolver {
	tb.Helper()
	r := &ConfigResolver{cache: make(map[string]*cacheEntry)}
	prev := r.completionHook.Load()
	r.completionHook.Store(&fn)
	tb.Cleanup(func() { r.completionHook.Store(prev) })
	return r
}
