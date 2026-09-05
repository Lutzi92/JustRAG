package app

import "testing"

// TestFileTabularIsOnKbViewChain pins the mount point for the "Tabellen"
// file-detail endpoint: it must sit on kbViewChain (like its sibling
// GET /api/kb/{id}/files), not on a stricter chain — any KB viewer may open
// the panel, and GetFileTabular's own fileBelongsToKB guard is what rejects
// a fileId belonging to a different KB. Reads routes.go's actual
// registration via the routeChains helper in advanced_routes_test.go, so
// moving the route to a different chain in routes.go turns this test red.
func TestFileTabularIsOnKbViewChain(t *testing.T) {
	chains := routeChains(t)
	const pattern = "GET /api/kb/{id}/files/{fileId}/tabular"
	got, ok := chains[pattern]
	if !ok {
		t.Fatalf("%s is not registered in routes.go", pattern)
	}
	if got != "kbViewChain" {
		t.Errorf("%s is on rc.%s, want rc.kbViewChain", pattern, got)
	}
}
