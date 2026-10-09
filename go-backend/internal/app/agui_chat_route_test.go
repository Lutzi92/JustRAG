package app

import (
	"os"
	"regexp"
	"testing"
)

// TestAgentChatRouteChain pins the agent chat's mount: the same "chat" rate
// limiter (outermost, so unauthenticated calls spend no quota) and KB-view
// chain as POST /api/kb/{id}/chat. routeChains does not see it — its regex
// only matches routes registered directly on an rc chain.
func TestAgentChatRouteChain(t *testing.T) {
	src, err := os.ReadFile("routes.go")
	if err != nil {
		t.Fatalf("read routes.go: %v", err)
	}
	want := regexp.MustCompile(`rc\.mux\.Handle\("POST /api/kb/\{id\}/agui/chat",\s*chatRL\.Middleware\(rc\.kbViewChain\(agentChat\.ServeHTTP\)\)\)`)
	if n := len(want.FindAll(src, -1)); n != 1 {
		t.Fatalf("POST /api/kb/{id}/agui/chat registered %d times as chatRL.Middleware(rc.kbViewChain(agentChat.ServeHTTP)), want 1", n)
	}
	mentions := regexp.MustCompile(`"POST /api/kb/\{id\}/agui/chat"`)
	if n := len(mentions.FindAll(src, -1)); n != 1 {
		t.Fatalf("POST /api/kb/{id}/agui/chat appears %d times in routes.go, want 1", n)
	}
}
