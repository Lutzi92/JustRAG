package fetcher

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"
)

// mustParseCIDR parses an exact CIDR literal and panics if invalid. Used at
// package init for the static SSRF block list — every input is a hardcoded
// constant, so any failure is a programmer error caught at startup.
func mustParseCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic("fetcher: bad CIDR " + s + ": " + err.Error())
	}
	return n
}

// privateBlocks lists CIDR ranges that fetcher must never connect to.
var privateBlocks = []*net.IPNet{
	// RFC 1918 private
	mustParseCIDR("10.0.0.0/8"),
	mustParseCIDR("172.16.0.0/12"),
	mustParseCIDR("192.168.0.0/16"),
	// Loopback
	mustParseCIDR("127.0.0.0/8"),
	// Link-local / cloud metadata (169.254.169.254)
	mustParseCIDR("169.254.0.0/16"),
	// CGNAT (RFC 6598)
	mustParseCIDR("100.64.0.0/10"),
	// "This network" — defaults / unspecified
	mustParseCIDR("0.0.0.0/8"),
	// IETF protocol assignments
	mustParseCIDR("192.0.0.0/24"),
	// Documentation / TEST-NET-1/2/3 (RFC 5737)
	mustParseCIDR("192.0.2.0/24"),
	mustParseCIDR("198.51.100.0/24"),
	mustParseCIDR("203.0.113.0/24"),
	// Benchmarking (RFC 2544)
	mustParseCIDR("198.18.0.0/15"),
	// IPv4 multicast / class E reserved
	mustParseCIDR("224.0.0.0/4"),
	mustParseCIDR("240.0.0.0/4"),
	// IPv6 loopback / ULA / link-local / multicast
	mustParseCIDR("::1/128"),
	mustParseCIDR("fc00::/7"),
	mustParseCIDR("fe80::/10"),
	mustParseCIDR("ff00::/8"),
	// IPv6 transition / documentation. 6to4 (2002::/16) and Teredo
	// (2001::/32) embed an IPv4 address inside the IPv6 address; if
	// we don't block them explicitly an attacker can route to a
	// private IPv4 destination through them. 2001:db8::/32 is the
	// documentation prefix and should never appear on the wire.
	mustParseCIDR("2002::/16"),
	mustParseCIDR("2001::/32"),
	mustParseCIDR("2001:db8::/32"),
	// Note: IPv4-mapped IPv6 addresses (::ffff:x.x.x.x) are covered
	// by the IPv4 CIDR blocks above. Go's net.IPNet.Contains
	// normalises both 4-byte and 16-byte representations, so
	// e.g. ::ffff:10.0.0.1 is caught by 10.0.0.0/8 without needing
	// an additional ::ffff:0:0/96 block (which would match ALL IPv4).
}

// allowPrivateCtxKey is the context key used to opt a single request out
// of SSRF private-IP rejection. Only trusted callers that hold a URL from
// site-config (not user input) should set it — e.g. academic.SearchJustFind
// when hitting an intranet/self-hosted VuFind instance.
type allowPrivateCtxKey struct{}

// withAllowPrivate returns a child context that, when honoured by
// validateURL and safeDialContext, lets the request reach private-IP hosts.
func withAllowPrivate(ctx context.Context) context.Context {
	return context.WithValue(ctx, allowPrivateCtxKey{}, true)
}

// ctxAllowsPrivate reports whether ctx was annotated with withAllowPrivate.
func ctxAllowsPrivate(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(allowPrivateCtxKey{}).(bool)
	return v
}

// isPrivateIP returns true when ip falls in any of privateBlocks or is nil.
func isPrivateIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	for _, b := range privateBlocks {
		if b.Contains(ip) {
			return true
		}
	}
	return false
}

// validateURL parses rawURL, ensures it is http(s), has a host, and that
// every IP the host resolves to is public. Returns a non-nil error otherwise.
//
// ctx governs the DNS lookup and must carry a deadline; an attacker who
// controls the resolver could otherwise stall the goroutine indefinitely.
// Callers without a natural context (e.g. CheckRedirect) should derive one
// from req.Context() or a short-lived WithTimeout.
func validateURL(ctx context.Context, rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil || u == nil {
		return fmt.Errorf("ssrf: parse url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("ssrf: scheme %q not allowed", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("ssrf: empty host")
	}
	// Strip trailing dot (FQDN form): "localhost." must be treated as
	// "localhost" so the explicit reject below fires.
	host = strings.TrimSuffix(host, ".")
	if host == "" {
		return fmt.Errorf("ssrf: empty host")
	}
	// Trusted caller opted into private-host access (e.g. intranet
	// JustFind instance from site-config). Scheme + non-empty host are
	// still enforced above; we only skip the private-IP rejection.
	if ctxAllowsPrivate(ctx) {
		return nil
	}
	// Reject obvious literals up front so DNS isn't even consulted for
	// localhost / loopback / metadata addresses.
	if ip := net.ParseIP(host); ip != nil {
		if isPrivateIP(ip) {
			return fmt.Errorf("ssrf: literal private IP %s", ip)
		}
		return nil
	}
	if strings.EqualFold(host, "localhost") {
		return fmt.Errorf("ssrf: localhost not allowed")
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return fmt.Errorf("ssrf: resolve %s: %w", host, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("ssrf: no IPs for %s", host)
	}
	for _, ip := range ips {
		if isPrivateIP(ip) {
			return fmt.Errorf("ssrf: %s resolves to private IP %s", host, ip)
		}
	}
	return nil
}

// proxyEndpoints parses the HTTP(S)_PROXY / ALL_PROXY environment into the set
// of lowercased "host:port" strings the transport may dial when egressing
// through a proxy. safeDialContext exempts these from the private-IP block so a
// trusted egress proxy on a private IP is reachable. The exemption is scoped to
// the exact host:port — never a bare host — so a DIFFERENT port on the proxy's
// IP (e.g. an attacker-supplied http://proxy-ip:6379/) stays SSRF-blocked.
func proxyEndpoints() map[string]struct{} {
	set := make(map[string]struct{})
	for _, key := range []string{
		"HTTPS_PROXY", "https_proxy",
		"HTTP_PROXY", "http_proxy",
		"ALL_PROXY", "all_proxy",
	} {
		if hostPort := parseProxyHostPort(os.Getenv(key)); hostPort != "" {
			set[hostPort] = struct{}{}
		}
	}
	return set
}

// parseProxyHostPort returns the lowercased host:port of a proxy env value,
// which may be a full URL ("http://10.0.0.1:3128") or a bare authority
// ("10.0.0.1:3128"). When no port is given it synthesizes the scheme's default
// so the entry still matches the real dialed address. Returns "" when raw is
// empty or has no host.
func parseProxyHostPort(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	port := u.Port()
	if port == "" {
		switch u.Scheme {
		case "https":
			port = "443"
		case "socks5", "socks5h":
			port = "1080"
		default:
			port = "80"
		}
	}
	return strings.ToLower(net.JoinHostPort(u.Hostname(), port))
}

// isProxyEndpoint reports whether the dialed host:port exactly matches a
// configured egress proxy.
func isProxyEndpoint(proxies map[string]struct{}, hostPort string) bool {
	if len(proxies) == 0 {
		return false
	}
	_, ok := proxies[hostPort]
	return ok
}

// safeDialContext returns a DialContext function that re-resolves the host,
// picks a public IP, and dials *that IP* directly with the original hostname
// preserved for SNI / certificate validation. This closes the DNS-rebinding
// window between validateURL and the kernel's actual connect.
//
// When the host has multiple public IPs (dual-stack, multi-A records) the
// dialer tries them in order until one connects, falling back on connection
// errors, similar to Go's default fallback behavior (not RFC 8305
// happy-eyeballs — attempts are sequential, not concurrent/staggered).
func safeDialContext(dialTimeout time.Duration) func(ctx context.Context, network, addr string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: dialTimeout}
	// Operator-configured egress proxies, read once when the dialer is built
	// (proxy env is fixed for a process's lifetime). When a proxy is used,
	// net/http dials the PROXY's address here — not the target — and that
	// address is commonly a private IP under k8s/corporate networking. Without
	// this exemption the SSRF block rejects the trusted proxy itself
	// ("proxyconnect tcp: ssrf dial: private IP ..."), breaking all egress.
	proxies := proxyEndpoints()
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("ssrf dial: split host: %w", err)
		}
		host = strings.TrimSuffix(host, ".")
		// Exempt the configured egress proxy from the private-IP rejection,
		// matched on the exact host:port. The SSRF check still applies to direct
		// dials (NO_PROXY targets, and other ports on the proxy host, stay
		// blocked) and to redirect hops (validateURL in CheckRedirect).
		dialedHostPort := strings.ToLower(net.JoinHostPort(host, port))
		allowPrivate := ctxAllowsPrivate(ctx) || isProxyEndpoint(proxies, dialedHostPort)
		// IP literal: validate and dial.
		if ip := net.ParseIP(host); ip != nil {
			if !allowPrivate && isPrivateIP(ip) {
				return nil, fmt.Errorf("ssrf dial: private IP %s", ip)
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		}
		// Resolve IPv4 first, then IPv6. Many servers (especially Docker
		// containers) lack proper IPv6 connectivity. If we resolve both
		// at once with "ip", unreachable IPv6 addresses each eat the full
		// dialTimeout (10 s) before we try IPv4 — often exhausting the
		// 30 s context deadline before a reachable IPv4 address gets a
		// chance, causing every fetch to fail with "context deadline
		// exceeded".
		var ips []net.IP
		ip4s, err4 := net.DefaultResolver.LookupIP(ctx, "ip4", host)
		ip6s, err6 := net.DefaultResolver.LookupIP(ctx, "ip6", host)
		ips = append(ips, ip4s...)
		ips = append(ips, ip6s...)
		if len(ips) == 0 {
			if dnsErr := errors.Join(err4, err6); dnsErr != nil {
				return nil, fmt.Errorf("ssrf dial: no IPs for %s: %w", host, dnsErr)
			}
			return nil, fmt.Errorf("ssrf dial: no IPs for %s", host)
		}
		if !allowPrivate {
			for _, ip := range ips {
				if isPrivateIP(ip) {
					return nil, fmt.Errorf("ssrf dial: %s resolved to private IP %s", host, ip)
				}
			}
		}
		// Try each public IP in order (IPv4 first). crypto/tls picks
		// ServerName from the URL passed to http.Request, so SNI still
		// works even though we dial the raw IP.
		var lastErr error
		for _, ip := range ips {
			conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if dialErr == nil {
				return conn, nil
			}
			lastErr = dialErr
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("ssrf dial: no IPs to try for %s", host)
		}
		return nil, fmt.Errorf("ssrf dial: %w", lastErr)
	}
}
