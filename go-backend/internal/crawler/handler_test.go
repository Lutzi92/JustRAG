package crawler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/crawler"
)

// Pure-function check: progress keys must round-trip identically between
// worker (which writes) and HTTP handler (which reads). The exact format
// is fixed so tests pin it explicitly.
func TestCrawlProgressKey(t *testing.T) {
	t.Parallel()

	cases := []struct {
		jobID string
		want  string
	}{
		{"", "crawl:progress:"},
		{"abc-123", "crawl:progress:abc-123"},
		{"01HFXY", "crawl:progress:01HFXY"},
	}
	for _, tc := range cases {
		t.Run(tc.jobID, func(t *testing.T) {
			t.Parallel()
			if got := crawler.CrawlProgressKey(tc.jobID); got != tc.want {
				t.Errorf("CrawlProgressKey(%q) = %q, want %q", tc.jobID, got, tc.want)
			}
		})
	}
}

// withAuthenticatedRequest builds an http.Request with PathValue set for the
// route and an auth user in context.
func withAuthenticatedRequest(t *testing.T, method, url, body, userID, kbID, jobID string) *http.Request {
	t.Helper()
	var bodyReader *bytes.Reader
	if body != "" {
		bodyReader = bytes.NewReader([]byte(body))
	} else {
		bodyReader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, url, bodyReader)
	if kbID != "" {
		req.SetPathValue("id", kbID)
	}
	if jobID != "" {
		req.SetPathValue("jobId", jobID)
	}
	if userID != "" {
		req = req.WithContext(auth.WithUser(req.Context(), &auth.Claims{ID: userID, Role: "user"}))
	}
	return req
}

// Crawl: rejects missing/invalid input before touching any backend, so we
// can drive every error path with a nil asynq/inspector handler.
func TestCrawl_InputValidation(t *testing.T) {
	t.Parallel()

	type tc struct {
		name     string
		body     string
		kbID     string
		userID   string
		wantCode int
	}

	cases := []tc{
		{
			name:     "missing kb id",
			body:     `{"url":"https://example.com"}`,
			kbID:     "",
			userID:   "u1",
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "unauthenticated",
			body:     `{"url":"https://example.com"}`,
			kbID:     "kb1",
			userID:   "",
			wantCode: http.StatusUnauthorized,
		},
		{
			name:     "malformed JSON",
			body:     `{not json`,
			kbID:     "kb1",
			userID:   "u1",
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "missing url",
			body:     `{"url":""}`,
			kbID:     "kb1",
			userID:   "u1",
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "invalid url",
			body:     `{"url":"not a url"}`,
			kbID:     "kb1",
			userID:   "u1",
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "asynq not configured returns 503",
			body:     `{"url":"https://example.com","maxPages":3}`,
			kbID:     "kb1",
			userID:   "u1",
			wantCode: http.StatusServiceUnavailable,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h := crawler.NewHandler(nil, nil, nil)
			req := withAuthenticatedRequest(t, http.MethodPost, "/crawl", c.body, c.userID, c.kbID, "")
			rec := httptest.NewRecorder()
			h.Crawl(rec, req)
			if rec.Code != c.wantCode {
				t.Errorf("status = %d, want %d (body=%q)", rec.Code, c.wantCode, rec.Body.String())
			}
		})
	}
}

// GetCrawlStatus: reads progress from Redis first, then falls back to the
// inspector. With both nil, missing jobId yields 400; valid jobId yields
// 404 because Redis is unavailable and there's no inspector to fall back
// to.
func TestGetCrawlStatus_NoBackends(t *testing.T) {
	t.Parallel()

	h := crawler.NewHandler(nil, nil, nil)

	t.Run("missing job id", func(t *testing.T) {
		t.Parallel()
		req := withAuthenticatedRequest(t, http.MethodGet, "/status", "", "u1", "", "")
		rec := httptest.NewRecorder()
		h.GetCrawlStatus(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("unauthenticated", func(t *testing.T) {
		t.Parallel()
		req := withAuthenticatedRequest(t, http.MethodGet, "/status", "", "", "", "job-1")
		rec := httptest.NewRecorder()
		h.GetCrawlStatus(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rec.Code)
		}
	})

	t.Run("no backends available -> 404", func(t *testing.T) {
		t.Parallel()
		req := withAuthenticatedRequest(t, http.MethodGet, "/status", "", "u1", "", "job-1")
		rec := httptest.NewRecorder()
		h.GetCrawlStatus(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rec.Code)
		}
	})
}

// GetCrawlStatus: when Redis holds a progress blob, it should be returned
// verbatim — but only when the userId in the blob matches the caller. A
// mismatch must NOT leak the existence of the job (treated as not-found).
func TestGetCrawlStatus_RedisProgress(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	type tc struct {
		name           string
		storedUserID   string
		callerUserID   string
		extraFields    map[string]any
		wantCode       int
		wantStateField string
	}
	cases := []tc{
		{
			name:           "owner reads own job",
			storedUserID:   "u1",
			callerUserID:   "u1",
			extraFields:    map[string]any{"state": "active", "progress": map[string]any{"step": "fetch"}},
			wantCode:       http.StatusOK,
			wantStateField: "active",
		},
		{
			name:         "other user sees not-found (no existence leak)",
			storedUserID: "u1",
			callerUserID: "u2",
			extraFields:  map[string]any{"state": "active"},
			wantCode:     http.StatusNotFound,
		},
	}

	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			jobID := "job-" + string(rune('a'+i))
			payload := map[string]any{"userId": c.storedUserID}
			maps.Copy(payload, c.extraFields)
			b, err := json.Marshal(payload)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if err := rdb.Set(context.Background(), crawler.CrawlProgressKey(jobID), string(b), time.Minute).Err(); err != nil {
				t.Fatalf("set: %v", err)
			}

			h := crawler.NewHandler(nil, nil, rdb)
			req := withAuthenticatedRequest(t, http.MethodGet, "/status", "", c.callerUserID, "", jobID)
			rec := httptest.NewRecorder()
			h.GetCrawlStatus(rec, req)

			if rec.Code != c.wantCode {
				t.Errorf("status = %d, want %d (body=%q)", rec.Code, c.wantCode, rec.Body.String())
				return
			}
			if c.wantStateField != "" {
				var got map[string]any
				if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
					t.Fatalf("decode response: %v", err)
				}
				if got["state"] != c.wantStateField {
					t.Errorf("state = %v, want %q", got["state"], c.wantStateField)
				}
			}
		})
	}
}

// Redis returns malformed JSON: handler must not return that to the client.
// With no inspector configured, the only acceptable fall-through is 404.
func TestGetCrawlStatus_MalformedRedisBlob(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	if err := rdb.Set(context.Background(), crawler.CrawlProgressKey("bad-blob"), "{not json", time.Minute).Err(); err != nil {
		t.Fatalf("set: %v", err)
	}

	h := crawler.NewHandler(nil, nil, rdb)
	req := withAuthenticatedRequest(t, http.MethodGet, "/status", "", "u1", "", "bad-blob")
	rec := httptest.NewRecorder()
	h.GetCrawlStatus(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("malformed blob: status = %d, want 404 (body=%q)", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "{not json") {
		t.Error("malformed blob leaked to response body")
	}
}
