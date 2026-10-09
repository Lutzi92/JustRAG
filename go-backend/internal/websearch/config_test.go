package websearch

import (
	"context"
	"errors"
	"testing"
)

// TestResolve pins the one shared web-search availability check.
//
// Oracle: hand-written expectations. The enabled parse is the one every
// web-search entry point used before it was shared (`*v == "true"`), so
// "TRUE" and "1" stay off — written out here as literals, not derived from
// Enabled. The sentinel per missing credential is what the /websearch route
// maps to its existing 403/500 responses (TestWebSearch_ConfigGating).
func TestResolve(t *testing.T) {
	full := func() map[string]*string {
		return map[string]*string{
			"web_search_enabled":    ptr("true"),
			"google_search_api_key": ptr("key"),
			"google_search_cx":      ptr("cx"),
		}
	}
	with := func(key string, val *string) map[string]*string {
		v := full()
		if val == nil {
			delete(v, key)
		} else {
			v[key] = val
		}
		return v
	}
	cases := []struct {
		name    string
		vals    map[string]*string
		wantErr error // nil = available
	}{
		{"all set", full(), nil},
		{"enabled absent", with("web_search_enabled", nil), ErrDisabled},
		{"enabled false", with("web_search_enabled", ptr("false")), ErrDisabled},
		{"enabled TRUE is not true", with("web_search_enabled", ptr("TRUE")), ErrDisabled},
		{"enabled 1 is not true", with("web_search_enabled", ptr("1")), ErrDisabled},
		{"api key absent", with("google_search_api_key", nil), ErrAPIKeyMissing},
		{"api key empty", with("google_search_api_key", ptr("")), ErrAPIKeyMissing},
		{"cx absent", with("google_search_cx", nil), ErrCXMissing},
		{"cx empty", with("google_search_cx", ptr("")), ErrCXMissing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			creds, err := Resolve(context.Background(), &fakeStore{vals: tc.vals})
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("Resolve: %v", err)
				}
				if creds != (Credentials{APIKey: "key", CX: "cx"}) {
					t.Fatalf("credentials = %+v", creds)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if !IsUnavailable(err) {
				t.Fatalf("IsUnavailable(%v) = false, want true", err)
			}
		})
	}
}

// TestResolve_ReadErrorIsNotUnavailable: a failed site_config read must stay
// distinguishable from "switched off", so callers can answer 5xx, not 4xx.
func TestResolve_ReadErrorIsNotUnavailable(t *testing.T) {
	boom := errors.New("boom")
	_, err := Resolve(context.Background(), &fakeStore{err: boom})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap the read error", err)
	}
	if IsUnavailable(err) {
		t.Fatal("a read error must not be reported as unavailable")
	}
}

func TestResolve_NilReaderIsDisabled(t *testing.T) {
	if _, err := Resolve(context.Background(), nil); !errors.Is(err, ErrDisabled) {
		t.Fatalf("err = %v, want ErrDisabled", err)
	}
}
