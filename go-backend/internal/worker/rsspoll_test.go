package worker

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mmcdole/gofeed"

	"github.com/justrag/go-backend/internal/fetcher"
	"github.com/justrag/go-backend/internal/rss"
)

type stubFetcher struct {
	res    *fetcher.Result
	err    error
	called bool
}

func (s *stubFetcher) Fetch(_ context.Context, _ string, _ fetcher.Options) (*fetcher.Result, error) {
	s.called = true
	return s.res, s.err
}

func feedItem() *gofeed.Item {
	return &gofeed.Item{
		Title:       "Advisory 123",
		Link:        "https://example.com/advisory/123",
		Description: "short summary",
	}
}

func TestBuildRSSItemContentWithBody(t *testing.T) {
	out := buildRSSItemContentWithBody(feedItem(), "FULL ARTICLE BODY")
	if !strings.Contains(out, "# Advisory 123") {
		t.Errorf("missing title header: %q", out)
	}
	if !strings.Contains(out, "Source: https://example.com/advisory/123") {
		t.Errorf("missing source line: %q", out)
	}
	if !strings.Contains(out, "FULL ARTICLE BODY") {
		t.Errorf("missing fetched body: %q", out)
	}
}

func TestResolveRSSItemContent_FetchSuccess(t *testing.T) {
	f := &stubFetcher{res: &fetcher.Result{Markdown: "FULL ARTICLE BODY"}}
	feed := &rss.RSSFeedRow{ID: "f1", FetchFullText: true}
	out := resolveRSSItemContent(context.Background(), f, feed, feedItem())
	if !strings.Contains(out, "FULL ARTICLE BODY") {
		t.Errorf("expected fetched body, got: %q", out)
	}
	if strings.Contains(out, "short summary") {
		t.Errorf("feed summary should be dropped when full text present: %q", out)
	}
}

func TestResolveRSSItemContent_FetchErrorFallsBack(t *testing.T) {
	f := &stubFetcher{err: errors.New("boom")}
	feed := &rss.RSSFeedRow{ID: "f1", FetchFullText: true}
	out := resolveRSSItemContent(context.Background(), f, feed, feedItem())
	if !strings.Contains(out, "short summary") {
		t.Errorf("expected fallback to feed content, got: %q", out)
	}
}

func TestResolveRSSItemContent_EmptyMarkdownFallsBack(t *testing.T) {
	f := &stubFetcher{res: &fetcher.Result{Markdown: "   "}}
	feed := &rss.RSSFeedRow{ID: "f1", FetchFullText: true}
	out := resolveRSSItemContent(context.Background(), f, feed, feedItem())
	if !strings.Contains(out, "short summary") {
		t.Errorf("expected fallback on empty markdown, got: %q", out)
	}
}

func TestResolveRSSItemContent_DisabledSkipsFetch(t *testing.T) {
	f := &stubFetcher{res: &fetcher.Result{Markdown: "FULL ARTICLE BODY"}}
	feed := &rss.RSSFeedRow{ID: "f1", FetchFullText: false}
	out := resolveRSSItemContent(context.Background(), f, feed, feedItem())
	if f.called {
		t.Error("fetcher should not be called when FetchFullText is false")
	}
	if !strings.Contains(out, "short summary") {
		t.Errorf("expected feed content, got: %q", out)
	}
}
