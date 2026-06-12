// Unit tests for QueryCache that need no live vector DB — kept outside the
// `integration` build tag so `go test ./...` exercises them everywhere.

package vector

import (
	"context"
	"testing"
)

// TestQueryCache_ProcessWriteRecoversPanic guards the async writer's
// poisoned-item contract: writeLoop calls processWrite per queued item, so a
// panic escaping processWrite would kill the writer goroutine permanently
// and silently downgrade every later Store() into a channel-overflow drop.
// A nil pool makes writeOne nil-deref, standing in for any per-write panic
// (pgx driver bug, corrupt payload).
func TestQueryCache_ProcessWriteRecoversPanic(t *testing.T) {
	c := NewQueryCache(nil)
	w := queryCacheWrite{kbID: "kb-panic-test", embedding: make([]float64, queryCacheDims)}

	// Two calls: the first proves recovery, the second proves the cache is
	// still usable after a recovered panic (no poisoned state left behind).
	c.processWrite(context.Background(), w)
	c.processWrite(context.Background(), w)
}
