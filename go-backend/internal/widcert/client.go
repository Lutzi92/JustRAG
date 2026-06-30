package widcert

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/justrag/go-backend/internal/fetcher"
	"github.com/justrag/go-backend/internal/httpclient"
)

// fetchTimeout caps the entire two-step fetch.
const fetchTimeout = 15 * time.Second

// maxRedirects caps redirect hops for a single WID request.
const maxRedirects = 10

// Client fetches WID advisories. The zero value is not usable; use NewClient.
type Client struct {
	http    *http.Client
	baseURL string // "https://wid.cert-bund.de"; overridable in tests
}

// NewClient returns a Client over the proxy-aware shared HTTP transport
// (httpclient.New honours HTTP(S)_PROXY), so it works behind a corporate / k8s
// egress proxy on a private IP. fetcher.SafeHTTPClient must NOT be used here:
// its dial-time private-IP block rejects the egress proxy's own address as an
// SSRF target (observed in prod: "proxyconnect tcp: ssrf dial: private IP ...").
//
// The SSRF protection that actually matters for this fixed, trusted host —
// refusing a redirect to an internal metadata endpoint — is enforced at the
// redirect layer via checkRedirect. That also works through a proxy, whereas a
// dial-time check never sees the real target once the request is proxied.
func NewClient() *Client {
	hc := httpclient.New(fetchTimeout)
	hc.CheckRedirect = checkRedirect
	return &Client{
		http:    hc,
		baseURL: "https://" + Host,
	}
}

// checkRedirect caps hop count and refuses any redirect whose target resolves
// to a private / loopback / link-local address (e.g. a 302 to 169.254.169.254
// cloud metadata). fetcher.ValidateURL re-resolves the host, catching both
// literal private IPs and hostnames that resolve to them.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("widcert: stopped after %d redirects", maxRedirects)
	}
	return fetcher.ValidateURL(req.Context(), req.URL.String())
}

// Fetch resolves name -> uuid -> content JSON and parses it into an Advisory.
func (c *Client) Fetch(ctx context.Context, name string) (*Advisory, error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	uuid, err := c.getString(ctx, c.baseURL+uuidByNamePath+url.PathEscape(name))
	if err != nil {
		return nil, fmt.Errorf("resolve WID uuid for %s: %w", name, err)
	}
	uuid = strings.Trim(strings.TrimSpace(uuid), `"`)
	if uuid == "" {
		return nil, fmt.Errorf("empty WID uuid for %s", name)
	}

	data, err := c.getBytes(ctx, c.baseURL+contentPath+url.PathEscape(uuid))
	if err != nil {
		return nil, fmt.Errorf("fetch WID content for %s: %w", name, err)
	}
	return parseAdvisory(name, data)
}

func (c *Client) getBytes(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

func (c *Client) getString(ctx context.Context, u string) (string, error) {
	b, err := c.getBytes(ctx, u)
	if err != nil {
		return "", err
	}
	var s string
	if jsonErr := json.Unmarshal(b, &s); jsonErr == nil {
		return s, nil
	}
	return string(b), nil
}
