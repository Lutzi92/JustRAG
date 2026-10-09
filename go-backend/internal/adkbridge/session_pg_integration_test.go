//go:build integration

package adkbridge

import (
	"testing"

	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/session/sessiontestsuite"
)

// TestPGSessionServiceConformance runs ADK's own session-service conformance
// suite against our pgx implementation.
func TestPGSessionServiceConformance(t *testing.T) {
	sessiontestsuite.RunServiceTests(t, sessiontestsuite.SuiteOptions{SupportsUserProvidedSessionID: true},
		func(t *testing.T) session.Service {
			return NewPGSessionService(isolatedPool(t, "0082_adk_sessions.sql"))
		})
}
