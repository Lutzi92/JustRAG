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

// longArticle is a realistic full-article body: well above minFullTextChars and
// longer than the feed's own summary, so the quality gate accepts it.
var longArticle = strings.Repeat("Detailed advisory body describing the vulnerability and affected products. ", 6)

func TestResolveRSSItemContent_FetchSuccess(t *testing.T) {
	f := &stubFetcher{res: &fetcher.Result{Markdown: longArticle}}
	feed := &rss.RSSFeedRow{ID: "f1", FetchFullText: true}
	out := resolveRSSItemContent(context.Background(), f, feed, feedItem())
	if !strings.Contains(out, "Detailed advisory body") {
		t.Errorf("expected fetched body, got: %q", out)
	}
	if strings.Contains(out, "short summary") {
		t.Errorf("feed summary should be dropped when full text present: %q", out)
	}
}

// TestResolveRSSItemContent_ShortJunkFallsBack is the cert-bund regression test:
// a JS-SPA shell yields a tiny non-empty extraction ("Warn- und Informationsdienst").
// The quality gate must reject it and keep the feed's own summary rather than
// silently storing the boilerplate.
func TestResolveRSSItemContent_ShortJunkFallsBack(t *testing.T) {
	f := &stubFetcher{res: &fetcher.Result{Markdown: "Warn- und Informationsdienst"}}
	feed := &rss.RSSFeedRow{ID: "f1", FetchFullText: true}
	out := resolveRSSItemContent(context.Background(), f, feed, feedItem())
	if !strings.Contains(out, "short summary") {
		t.Errorf("expected fallback to feed summary on junk extraction, got: %q", out)
	}
	if strings.Contains(out, "Warn- und Informationsdienst") {
		t.Errorf("junk extraction should not be stored as full text: %q", out)
	}
}

func TestIsUsableFullText(t *testing.T) {
	long := strings.Repeat("x", minFullTextChars)
	cases := []struct {
		name          string
		fetched, feed string
		want          bool
	}{
		{"below floor", "Warn- und Informationsdienst", "short summary", false},
		{"above floor, empty feed", long, "", true},
		{"above floor but shorter than feed", long, strings.Repeat("y", minFullTextChars+50), false},
		{"above floor and longer than feed", long + "more", "short summary", true},
		{"whitespace only", "        ", "", false},
	}
	for _, c := range cases {
		if got := isUsableFullText(c.fetched, c.feed); got != c.want {
			t.Errorf("%s: isUsableFullText(len %d, len %d) = %v, want %v", c.name, len(c.fetched), len(c.feed), got, c.want)
		}
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
