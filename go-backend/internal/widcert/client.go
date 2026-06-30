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
)

// fetchTimeout caps the entire two-step fetch.
const fetchTimeout = 15 * time.Second

// Client fetches WID advisories. The zero value is not usable; use NewClient.
type Client struct {
	http    *http.Client
	baseURL string // "https://wid.cert-bund.de"; overridable in tests
}

// NewClient returns a Client using the SSRF-safe shared HTTP transport
// (fetcher.SafeHTTPClient), whose safeDialContext blocks private/link-local/
// loopback ranges at dial time — including after any redirect — so a hijacked
// or compromised WID host cannot redirect us at an internal metadata endpoint.
func NewClient() *Client {
	return &Client{
		http:    fetcher.SafeHTTPClient(fetchTimeout),
		baseURL: "https://" + Host,
	}
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
