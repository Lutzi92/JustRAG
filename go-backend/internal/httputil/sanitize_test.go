package httputil_test

import (
	"fmt"
	"testing"

	"github.com/justrag/go-backend/internal/httputil"
)

func TestSanitizeError_PathsFiltered(t *testing.T) {
	msg := httputil.SanitizeError(fmt.Errorf("failed to read /etc/passwd"))
	if msg != "An internal error occurred. Please contact support." {
		t.Errorf("expected path filtering, got: %s", msg)
	}
}

func TestSanitizeError_SQLFiltered(t *testing.T) {
	msg := httputil.SanitizeError(fmt.Errorf("SQL syntax error near SELECT"))
	if msg != "A database error occurred. Please try again later." {
		t.Errorf("expected SQL filtering, got: %s", msg)
	}
}

func TestSanitizeError_DatabaseFiltered(t *testing.T) {
	msg := httputil.SanitizeError(fmt.Errorf("database connection refused"))
	// Note: "database" matches SQL filter, but "connection" also matches security terms.
	// The SQL/database check comes first, so this should return the database error.
	expected := "A database error occurred. Please try again later."
	if msg != expected {
		t.Errorf("expected database filtering, got: %s", msg)
	}
}

func TestSanitizeError_SecurityTermsFiltered(t *testing.T) {
	terms := []string{
		"password mismatch", "api_key invalid", "api key not found",
		"secret leaked", "credential error",
		"jwt token signature invalid", "auth token expired",
		"db connection refused", "redis connection error",
	}
	for _, term := range terms {
		msg := httputil.SanitizeError(fmt.Errorf("%s", term))
		if msg != "A system error occurred. Please contact support." {
			t.Errorf("expected security filtering for '%s', got: %s", term, msg)
		}
	}
}

// TestSanitizeError_BroadTermsNoLongerOverMatched documents the narrowed
// filter: legitimate user-facing messages that contain bare "connection",
// "token", or "fatal" now pass through instead of being collapsed into a
// generic "system error" string.
func TestSanitizeError_BroadTermsNoLongerOverMatched(t *testing.T) {
	cases := []string{
		"connection timeout: retry in 30s",
		"token limit exceeded for model gemma-4-26b",
		"fatal error: invalid mode",
	}
	for _, msg := range cases {
		got := httputil.SanitizeError(fmt.Errorf("%s", msg))
		if got != msg {
			t.Errorf("expected passthrough for %q, got: %s", msg, got)
		}
	}
}

func TestSanitizeError_SafeMessagePassthrough(t *testing.T) {
	msg := httputil.SanitizeError(fmt.Errorf("Knowledge base not found"))
	if msg != "Knowledge base not found" {
		t.Errorf("expected passthrough, got: %s", msg)
	}
}

func TestSanitizeError_NilError(t *testing.T) {
	msg := httputil.SanitizeError(nil)
	if msg != "An unexpected error occurred." {
		t.Errorf("expected default message, got: %s", msg)
	}
}

// TestSanitizeError_PathHeuristicNotTooBroad verifies that valid error
// messages containing a single `/` (HTTP status notations, ratios, etc.) are
// not mis-flagged as filesystem paths — the previous heuristic matched any
// `/` or `\` and corrupted otherwise-safe messages.
func TestSanitizeError_PathHeuristicNotTooBroad(t *testing.T) {
	cases := []string{
		"upstream returned 400/bad request",
		"unable to parse cidr 10.0.0.0/24",
		"chunk 3/7 failed validation",
		"regex /^foo$/ did not match",
	}
	for _, msg := range cases {
		got := httputil.SanitizeError(fmt.Errorf("%s", msg))
		if got != msg {
			t.Errorf("expected passthrough for %q, got: %s", msg, got)
		}
	}
}

// TestSanitizeError_PathHeuristicCatchesPaths verifies the new regex still
// catches the cases the old check was protecting against.
func TestSanitizeError_PathHeuristicCatchesPaths(t *testing.T) {
	cases := []string{
		"open /etc/passwd: permission denied",
		"failed to load (/var/lib/foo/bar)",
		"could not stat C:\\Users\\alice\\secret.txt",
		`unc path \\fileserver\share\private failed`,
		// Single-segment paths leak too — bare directory and Unix-socket
		// references from stdlib errors used to pass through.
		"open /tmp: permission denied",
		"dial unix /tmp/redis.sock: connect: connection refused",
		"failed to stat /var",
	}
	for _, msg := range cases {
		got := httputil.SanitizeError(fmt.Errorf("%s", msg))
		if got != "An internal error occurred. Please contact support." {
			t.Errorf("expected path filter for %q, got: %s", msg, got)
		}
	}
}
