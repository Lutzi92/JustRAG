package admineval

import (
	"testing"

	"github.com/justrag/go-backend/internal/siteconfig"
)

// Every per-KB-overridable registry key MUST be in snapshotConfigKeys so an
// eval run captures (and therefore exercises) the KB's override.
func TestRegistrySubsetOfSnapshotKeys(t *testing.T) {
	snap := map[string]bool{}
	for _, k := range snapshotConfigKeys {
		snap[k] = true
	}
	for _, f := range siteconfig.All() {
		if !snap[f.Key] {
			t.Errorf("registry key %q is not in snapshotConfigKeys — eval would not capture its override", f.Key)
		}
	}
}
