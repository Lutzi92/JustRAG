package app

import (
	"os"
	"regexp"
	"testing"
)

// The chain-composition tests in kbadmin_chain_test.go prove that
// kbAdvancedChain refuses a plain user. They cannot prove that the advanced
// surface is actually MOUNTED on it — they rebuild the chain rather than read
// the route table, so moving a route back to kbAdminChain in routes.go leaves
// them green. That is exactly the regression this file guards.
//
// Reading the source is deliberate. The alternative, standing up the real mux,
// needs the full infra dependency tree (Postgres pools, Redis, Asynq, an AI
// resolver) that NewServer wires, which is not available in a unit test. The
// route table is a flat list of literal calls, so a regex over it is exact
// rather than approximate: every registration in routes.go has the shape
// rc.mux.Handle("<METHOD> <PATH>", rc.<chain>(<handler>)).
var routeRe = regexp.MustCompile(`rc\.mux\.Handle\("([A-Z]+ [^"]+)",\s*rc\.(\w+)\(`)

func routeChains(t *testing.T) map[string]string {
	t.Helper()
	src, err := os.ReadFile("routes.go")
	if err != nil {
		t.Fatalf("read routes.go: %v", err)
	}
	matches := routeRe.FindAllStringSubmatch(string(src), -1)
	// Sanity floor: if the regex ever stops matching the file's shape it
	// would silently return an empty map and every assertion below would
	// pass vacuously. routes.go registers on the order of a hundred routes.
	if len(matches) < 100 {
		t.Fatalf("routeChains matched only %d routes in routes.go — the regex no longer fits the file", len(matches))
	}
	out := make(map[string]string, len(matches))
	for _, m := range matches {
		out[m[1]] = m[2]
	}
	return out
}

// advancedRoutes is the KB "advanced settings" surface: every endpoint
// KbSettingsPanel's four tabs call, plus the per-KB re-embed sweep its Save
// flow offers. All of them require a system role in
// {api-user, admin, superadmin} on top of KB role admin.
var advancedRoutes = []string{
	// RAG Settings tab — the 52-key per-KB site_config registry.
	"GET /api/kb/{id}/settings",
	"PUT /api/kb/{id}/settings",
	"DELETE /api/kb/{id}/settings/{key}",
	// The re-ingest the settings Save flow offers after a requiresReingest
	// key changes. Its only caller is that flow.
	"POST /api/kb/{id}/reembed",
	// Workflow tab — projection, preset preview, preset apply.
	"GET /api/kb/{id}/workflow",
	"GET /api/kb/{id}/workflow/preset",
	"POST /api/kb/{id}/workflow/preset",
	// Agenten & Teams tab — attach/detach, and the workflow canvas's
	// default-binding write, which goes through the same two PUTs.
	"PUT /api/kb/{id}/agents/{agentId}",
	"DELETE /api/kb/{id}/agents/{agentId}",
	"PUT /api/kb/{id}/teams/{teamId}",
	"DELETE /api/kb/{id}/teams/{teamId}",
	// Evals tab.
	"GET /api/kb/{id}/eval/golden-sets",
	"POST /api/kb/{id}/eval/golden-sets",
	"POST /api/kb/{id}/eval/golden-sets/generate",
	"GET /api/kb/{id}/eval/golden-sets/jobs",
	"GET /api/kb/{id}/eval/golden-sets/{gsId}",
	"DELETE /api/kb/{id}/eval/golden-sets/{gsId}",
	"POST /api/kb/{id}/eval/runs",
	"GET /api/kb/{id}/eval/runs",
	"GET /api/kb/{id}/eval/runs/{runId}",
	"GET /api/kb/{id}/eval/runs/{runId}/export",
	"DELETE /api/kb/{id}/eval/runs/{runId}",
}

func TestAdvancedSurfaceIsOnKbAdvancedChain(t *testing.T) {
	chains := routeChains(t)
	for _, pattern := range advancedRoutes {
		got, ok := chains[pattern]
		if !ok {
			t.Errorf("%s is not registered in routes.go — if it was renamed, move the new pattern onto kbAdvancedChain and update this list", pattern)
			continue
		}
		if got != "kbAdvancedChain" {
			t.Errorf("%s is on rc.%s, want rc.kbAdvancedChain — this route is part of the KB advanced settings surface and must require a system role in {api-user, admin, superadmin} as well as KB role admin", pattern, got)
		}
	}
}

// TestAgentListStaysOnViewChain pins the one deliberate exception. The chat
// session's agent picker (web/src/hooks/useKbAgents.ts) reads this list for
// every ordinary user with view access, so gating it would break agent
// selection in chat for exactly the people the four-role model means to let
// chat. Its siblings PUT/DELETE .../agents/{agentId} are on kbAdvancedChain;
// the read is not.
func TestAgentListStaysOnViewChain(t *testing.T) {
	chains := routeChains(t)
	const pattern = "GET /api/kb/{id}/agents"
	got, ok := chains[pattern]
	if !ok {
		t.Fatalf("%s is not registered in routes.go", pattern)
	}
	if got != "kbViewChain" {
		t.Errorf("%s is on rc.%s, want rc.kbViewChain — the chat agent picker reads it for ordinary users", pattern, got)
	}
}
