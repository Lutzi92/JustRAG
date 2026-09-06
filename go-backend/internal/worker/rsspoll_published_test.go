package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hibiken/asynq"

	"github.com/justrag/go-backend/internal/files"
	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/rss"
)

// recordingFileStore captures the CreateFileData the poll handler builds.
type recordingFileStore struct {
	fakeFileStore
	created []files.CreateFileData
}

func (f *recordingFileStore) CreateFile(_ context.Context, d files.CreateFileData) (*files.FileRecord, error) {
	f.created = append(f.created, d)
	return &files.FileRecord{ID: "file-1", KbID: d.KbID, Name: d.Name}, nil
}

// stubRSSStore serves one feed row and swallows the bookkeeping writes.
type stubRSSStore struct {
	feed *rss.RSSFeedRow
}

var _ rss.RSSStore = (*stubRSSStore)(nil)

func (s *stubRSSStore) CreateRSSFeed(context.Context, string, string, *string, string, bool) (*rss.RSSFeedRow, error) {
	return nil, nil
}
func (s *stubRSSStore) ListRSSFeeds(context.Context, string) ([]rss.RSSFeedRow, error) {
	return nil, nil
}
func (s *stubRSSStore) GetRSSFeedByID(context.Context, string) (*rss.RSSFeedRow, error) {
	return s.feed, nil
}
func (s *stubRSSStore) UpdateRSSFeed(context.Context, string, rss.RSSFeedUpdate) (*rss.RSSFeedRow, error) {
	return nil, nil
}
func (s *stubRSSStore) DeleteRSSFeed(context.Context, string) error                 { return nil }
func (s *stubRSSStore) UpdateRSSFeedPollSuccess(context.Context, string, int) error { return nil }
func (s *stubRSSStore) UpdateRSSFeedPollFailure(context.Context, string, string) error {
	return nil
}
func (s *stubRSSStore) ListFileNamesByRSSFeedID(context.Context, string) (map[string]bool, error) {
	return map[string]bool{}, nil
}
func (s *stubRSSStore) ListFileIDsByRSSFeedID(context.Context, string) ([]string, error) {
	return nil, nil
}

// TestClampPublishedAt covers the ingest-time clamp on a feed-controlled
// date. `published_at` comes straight from the feed, and a future date would
// make the item permanently "newest" for the recency boost, the recency
// listing and every date window — a single mis-dated advisory would then sit
// on top of every freshness-sensitive answer until it was deleted.
//
// Mutation: drop the `After(now)` branch in clampPublishedAt → the future
// case below fails.
func TestClampPublishedAt(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)

	if got := clampPublishedAt(nil, now); got != nil {
		t.Errorf("nil in, nil out; got %v", got)
	}

	past := time.Date(2025, 12, 24, 9, 30, 0, 0, time.UTC)
	got := clampPublishedAt(&past, now)
	if got == nil || !got.Equal(past) {
		t.Errorf("a past date must pass through unchanged, got %v", got)
	}

	future := now.Add(72 * time.Hour)
	got = clampPublishedAt(&future, now)
	if got == nil {
		t.Fatal("a future date must be clamped, not dropped")
	}
	if got.After(now) {
		t.Errorf("clamped date = %v, must not be after now (%v)", got, now)
	}
	if !got.Equal(now) {
		t.Errorf("clamped date = %v, want exactly now (%v)", got, now)
	}
}

// TestRSSPoll_ClampsFuturePublishedDate drives the same clamp through the real
// poll handler: the feed below dates an item in the year 2999.
func TestRSSPoll_ClampsFuturePublishedDate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(feedWithFuturePubDate))
	}))
	defer srv.Close()

	fileStore := &recordingFileStore{}
	client := asynq.NewClient(asynq.RedisClientOpt{Addr: "127.0.0.1:1"})
	defer client.Close() //nolint:errcheck

	handler := NewRSSPollHandler(RSSPollDeps{
		RSSStore:    &stubRSSStore{feed: &rss.RSSFeedRow{ID: "f1", KbID: "kb-1", URL: srv.URL, Status: "active"}},
		FileStore:   fileStore,
		Storage:     &fakeRSSStorage{},
		AsynqClient: client,
	})

	payload, err := json.Marshal(jobs.RSSPollPayload{FeedID: "f1"})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	before := time.Now()
	if err := handler(context.Background(), asynq.NewTask(jobs.TypeRSSPoll, payload)); err != nil {
		t.Fatalf("poll handler: %v", err)
	}
	after := time.Now()

	if len(fileStore.created) != 1 {
		t.Fatalf("CreateFile calls = %d, want 1", len(fileStore.created))
	}
	got := fileStore.created[0].PublishedAt
	if got == nil {
		t.Fatal("a dated item must still carry PublishedAt after clamping")
	}
	if got.After(after) {
		t.Errorf("PublishedAt = %v, must not be in the future", got)
	}
	if got.Before(before.Add(-time.Minute)) {
		t.Errorf("PublishedAt = %v, want ~now (clamped), not the feed's year-2999 date", got)
	}
}

const feedWithFuturePubDate = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel>
  <title>Test feed</title>
  <item>
    <title>Advisory from the future</title>
    <link>https://example.com/future</link>
    <guid>future-1</guid>
    <pubDate>Fri, 24 Dec 2999 09:30:00 GMT</pubDate>
    <description>body</description>
  </item>
</channel></rss>`

const feedWithPubDate = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel>
  <title>Test feed</title>
  <item>
    <title>Dated advisory</title>
    <link>https://example.com/dated</link>
    <guid>dated-1</guid>
    <pubDate>Wed, 24 Dec 2025 09:30:00 GMT</pubDate>
    <description>body</description>
  </item>
  <item>
    <title>Undated advisory</title>
    <link>https://example.com/undated</link>
    <guid>undated-1</guid>
    <description>body</description>
  </item>
</channel></rss>`

// TestRSSPoll_PersistsItemPublishedDate drives the real poll handler against
// an httptest feed and asserts the item's own publication date reaches
// files.CreateFileData — the single origin that fills files.published_at
// (W3-R9). The undated item must leave it nil rather than defaulting to now,
// which would make COALESCE(published_at, created_at) a no-op.
//
// Mutation: drop `PublishedAt: item.PublishedParsed` from the CreateFileData
// literal in rsspoll.go → this fails.
//
// The asynq client points at a dead Redis address on purpose: Enqueue then
// fails, the handler rolls the item back (a no-op against these fakes), and
// CreateFile has already been called with the params under test — so the
// test needs no live Redis.
func TestRSSPoll_PersistsItemPublishedDate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(feedWithPubDate))
	}))
	defer srv.Close()

	fileStore := &recordingFileStore{}
	client := asynq.NewClient(asynq.RedisClientOpt{Addr: "127.0.0.1:1"})
	defer client.Close() //nolint:errcheck

	handler := NewRSSPollHandler(RSSPollDeps{
		RSSStore:    &stubRSSStore{feed: &rss.RSSFeedRow{ID: "f1", KbID: "kb-1", URL: srv.URL, Status: "active"}},
		FileStore:   fileStore,
		Storage:     &fakeRSSStorage{},
		AsynqClient: client,
	})

	payload, err := json.Marshal(jobs.RSSPollPayload{FeedID: "f1"})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if err := handler(context.Background(), asynq.NewTask(jobs.TypeRSSPoll, payload)); err != nil {
		t.Fatalf("poll handler: %v", err)
	}

	if len(fileStore.created) != 2 {
		t.Fatalf("CreateFile calls = %d, want 2", len(fileStore.created))
	}
	var dated, undated *files.CreateFileData
	for i := range fileStore.created {
		if fileStore.created[i].PublishedAt != nil {
			dated = &fileStore.created[i]
		} else {
			undated = &fileStore.created[i]
		}
	}
	if dated == nil {
		t.Fatal("the item with a pubDate must carry PublishedAt")
	}
	want := time.Date(2025, 12, 24, 9, 30, 0, 0, time.UTC)
	if !dated.PublishedAt.UTC().Equal(want) {
		t.Errorf("PublishedAt = %v, want %v", dated.PublishedAt.UTC(), want)
	}
	if undated == nil {
		t.Error("the item without a pubDate must leave PublishedAt nil")
	}
}
