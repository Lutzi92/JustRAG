package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/fetcher"
	"github.com/justrag/go-backend/internal/research"
)

type webSearchFakeConfig struct {
	vals map[string]*string
	err  error
}

func (f webSearchFakeConfig) GetSiteConfigValue(_ context.Context, key string) (*string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.vals[key], nil
}

func wsPtr(s string) *string { return &s }

// adminConfigKeys are the site_config names the tool's error text must never
// carry: the model may repeat a tool error to the user verbatim.
var adminConfigKeys = []string{"web_search_enabled", "google_search_api_key", "google_search_cx", "chat_web_search_enabled"}

// TestWebSearchTool_ErrorsNameNoAdminKeys drives the real tool handler into
// each configuration failure and checks the error the model receives.
// Oracle: the literal admin key names (must be absent) and the generic text
// the tool documents for "unavailable" vs "failed".
func TestWebSearchTool_ErrorsNameNoAdminKeys(t *testing.T) {
	f := fetcher.New(context.Background(), fetcher.Config{})
	full := map[string]string{"web_search_enabled": "true", "google_search_api_key": "k", "google_search_cx": "c"}
	without := func(key string) map[string]*string {
		out := map[string]*string{}
		for k, v := range full {
			if k != key {
				out[k] = wsPtr(v)
			}
		}
		return out
	}
	cases := []struct {
		name   string
		client *research.WebClient
		want   error
	}{
		{"no web client wired", nil, errWebSearchUnavailable},
		{"web_search_enabled off", research.NewWebClient(webSearchFakeConfig{vals: without("web_search_enabled")}, f), errWebSearchUnavailable},
		{"api key missing", research.NewWebClient(webSearchFakeConfig{vals: without("google_search_api_key")}, f), errWebSearchUnavailable},
		{"cx missing", research.NewWebClient(webSearchFakeConfig{vals: without("google_search_cx")}, f), errWebSearchUnavailable},
		{"site_config read error", research.NewWebClient(webSearchFakeConfig{err: errors.New("db down reading web_search_enabled")}, f), errWebSearchFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := webSearchHandler(tc.client)(context.Background(), json.RawMessage(`{"query":"q"}`))
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			for _, key := range adminConfigKeys {
				if strings.Contains(err.Error(), key) {
					t.Errorf("tool error %q names admin key %q", err.Error(), key)
				}
			}
		})
	}
}
