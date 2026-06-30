package fetcher

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

func TestIsPrivateIP(t *testing.T) {
	t.Parallel()
	cases := []struct {
		ip      string
		private bool
	}{
		// Existing core cases
		{"127.0.0.1", true},
		{"10.0.0.5", true},
		{"172.16.0.1", true},
		{"192.168.1.1", true},
		{"169.254.169.254", true}, // AWS metadata
		{"100.64.0.1", true},      // CGNAT
		{"::1", true},
		{"fe80::1", true},
		{"fc00::1", true},
		{"0.0.0.0", true},
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"2606:4700:4700::1111", false},
		// New: documentation / reserved ranges added in fix 1
		{"192.0.2.5", true},    // TEST-NET-1
		{"198.51.100.5", true}, // TEST-NET-2
		{"203.0.113.5", true},  // TEST-NET-3
		{"198.18.0.1", true},   // benchmark
		{"240.0.0.1", true},    // reserved
		// New: multicast cases
		{"224.0.0.1", true}, // IPv4 multicast
		{"ff02::1", true},   // IPv6 multicast
		// New: IPv6-mapped private IPv4
		{"::ffff:10.0.0.1", true},
		// Boundary cases
		{"172.31.255.255", true},  // upper bound of 172.16.0.0/12
		{"255.255.255.255", true}, // broadcast (within 240.0.0.0/4)
	}
	for _, tc := range cases {
		t.Run(tc.ip, func(t *testing.T) {
			t.Parallel()
			got := isPrivateIP(net.ParseIP(tc.ip))
			if got != tc.private {
				t.Errorf("isPrivateIP(%s) = %v, want %v", tc.ip, got, tc.private)
			}
		})
	}
}

// TestIsPrivateIPNil locks in the conservative nil-treats-as-private behaviour.
func TestIsPrivateIPNil(t *testing.T) {
	if !isPrivateIP(nil) {
		t.Error("isPrivateIP(nil) should return true (conservative default)")
	}
}

func TestValidateURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		url     string
		wantErr bool
	}{
		// Existing core cases
		{"https://example.com/", false},
		{"http://localhost/", true},
		{"http://127.0.0.1/", true},
		{"http://169.254.169.254/", true},
		{"ftp://example.com/", true},
		{"file:///etc/passwd", true},
		{"https://", true},
		{"://nope", true},
		// New: trailing-dot variant of localhost (FQDN form)
		{"http://localhost./", true},
		// New: uppercase localhost
		{"http://LOCALHOST/", true},
		// New: bracketed IPv6 loopback literal
		{"http://[::1]/", true},
		// New: bracketed IPv6-mapped private IPv4 literal
		{"http://[::ffff:10.0.0.1]/", true},
		// New: embedded credentials must not bypass localhost reject
		{"http://user:pass@localhost/", true},
		// New: ported localhost
		{"http://localhost:8080/", true},
		// New: decimal IP form of 127.0.0.1 — DNS lookup fails, still rejected
		{"http://2130706433/", true},
		// New: public domain over plain HTTP is allowed (validator permits http scheme)
		{"http://example.com/", false},
	}
	for _, tc := range cases {
		t.Run(tc.url, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			err := validateURL(ctx, tc.url)
			if (err != nil) != tc.wantErr {
				t.Errorf("validateURL(%q) err=%v, wantErr=%v", tc.url, err, tc.wantErr)
			}
		})
	}
}

func TestParseProxyHost(t *testing.T) {
	cases := []struct {
		raw, wantHostPort, wantHost string
	}{
		{"http://10.60.3.254:3128", "10.60.3.254:3128", "10.60.3.254"},
		{"10.60.3.254:3128", "10.60.3.254:3128", "10.60.3.254"},
		{"http://proxy.example:8080", "proxy.example:8080", "proxy.example"},
		{"HTTP://Proxy.Example:8080", "proxy.example:8080", "proxy.example"},
		{"", "", ""},
	}
	for _, tc := range cases {
		hp, h := parseProxyHost(tc.raw)
		if hp != tc.wantHostPort || h != tc.wantHost {
			t.Errorf("parseProxyHost(%q) = (%q,%q), want (%q,%q)", tc.raw, hp, h, tc.wantHostPort, tc.wantHost)
		}
	}
}

// The configured egress proxy on a private IP must be dialable (not rejected as
// an SSRF target); a private IP that is NOT the proxy must stay blocked.
func TestSafeDialContext_ExemptsConfiguredProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("http_proxy", "")
	t.Setenv("ALL_PROXY", "")
	t.Setenv("all_proxy", "")
	t.Setenv("https_proxy", "")
	t.Setenv("HTTPS_PROXY", "http://10.60.3.254:3128")

	dial := safeDialContext(150 * time.Millisecond)

	// Dialing the proxy's own (private) address must NOT be an SSRF refusal —
	// it should reach a real dial attempt (connection refused / timeout).
	_, err := dial(context.Background(), "tcp", "10.60.3.254:3128")
	if err != nil && strings.Contains(err.Error(), "ssrf dial: ") && strings.Contains(err.Error(), "private IP") {
		t.Errorf("configured proxy must be exempt from SSRF block, got: %v", err)
	}

	// A different private IP (not the proxy) must still be SSRF-blocked.
	_, err = dial(context.Background(), "tcp", "10.0.0.5:443")
	if err == nil || !strings.Contains(err.Error(), "private IP") {
		t.Errorf("non-proxy private IP must be blocked, got: %v", err)
	}
}

func TestSafeDialContext_NoProxyStillBlocksPrivate(t *testing.T) {
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"} {
		t.Setenv(k, "")
	}
	dial := safeDialContext(150 * time.Millisecond)
	_, err := dial(context.Background(), "tcp", "10.60.3.254:3128")
	if err == nil || !strings.Contains(err.Error(), "private IP") {
		t.Errorf("with no proxy configured, private IP must be blocked, got: %v", err)
	}
}
