package websearch

import (
	"context"
	"errors"
	"fmt"
)

// ConfigReader is the site_config read surface the availability check needs.
// WebSearchStore, chat.SiteConfigReader and research.SiteConfigGetter all
// satisfy it.
type ConfigReader interface {
	GetSiteConfigValue(ctx context.Context, key string) (*string, error)
}

// The three reasons web search is unavailable on a deployment. Callers map
// them to their own status codes; IsUnavailable groups them, so a caller can
// tell "switched off / not configured" apart from a failed site_config read.
var (
	ErrDisabled      = errors.New("web search is not enabled")
	ErrAPIKeyMissing = errors.New("google_search_api_key is not configured")
	ErrCXMissing     = errors.New("google_search_cx is not configured")
)

// IsUnavailable reports whether err is one of the configuration reasons
// above, as opposed to a site_config read error.
func IsUnavailable(err error) bool {
	return errors.Is(err, ErrDisabled) || errors.Is(err, ErrAPIKeyMissing) || errors.Is(err, ErrCXMissing)
}

// Credentials are the Google Custom Search credentials of a deployment on
// which web search is switched on.
type Credentials struct {
	APIKey string
	CX     string
}

// Enabled reads web_search_enabled. This is the one place that key is
// parsed: exactly "true" switches web search on, anything else (absent,
// "false", "1", "TRUE") leaves it off — the parse every web-search entry
// point has always used. A nil reader means off.
func Enabled(ctx context.Context, r ConfigReader) (bool, error) {
	if r == nil {
		return false, nil
	}
	v, err := r.GetSiteConfigValue(ctx, "web_search_enabled")
	if err != nil {
		return false, fmt.Errorf("read web_search_enabled: %w", err)
	}
	return v != nil && *v == "true", nil
}

// Resolve is the shared availability check for every web-search entry point
// (the /websearch route, the research agent's WebClient, the web_search MCP
// tool and the chat turn's up-front check): web_search_enabled must be on
// and both Google credentials must be set. It returns ErrDisabled,
// ErrAPIKeyMissing or ErrCXMissing when web search is unavailable, and a
// wrapped read error when site_config could not be read.
func Resolve(ctx context.Context, r ConfigReader) (Credentials, error) {
	on, err := Enabled(ctx, r)
	if err != nil {
		return Credentials{}, err
	}
	if !on {
		return Credentials{}, ErrDisabled
	}
	apiKey, err := r.GetSiteConfigValue(ctx, "google_search_api_key")
	if err != nil {
		return Credentials{}, fmt.Errorf("read google_search_api_key: %w", err)
	}
	if apiKey == nil || *apiKey == "" {
		return Credentials{}, ErrAPIKeyMissing
	}
	cx, err := r.GetSiteConfigValue(ctx, "google_search_cx")
	if err != nil {
		return Credentials{}, fmt.Errorf("read google_search_cx: %w", err)
	}
	if cx == nil || *cx == "" {
		return Credentials{}, ErrCXMissing
	}
	return Credentials{APIKey: *apiKey, CX: *cx}, nil
}
