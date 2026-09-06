package confluence

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hibiken/asynq"
)

// ---------------------------------------------------------------------------
// The parser
// ---------------------------------------------------------------------------

// Mutation: make VersionWhen return &time.Now() on a parse error → the
// "unparseable" case below stops being nil and this fails. A guessed date is
// indistinguishable from a real one at every read site, which is why the
// failure mode has to be nil (and thus COALESCE back to created_at).
func TestPageVersionWhen(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want *time.Time
	}{
		{"absent", "", nil},
		{"unparseable", "yesterday", nil},
		{
			"rfc3339 with offset and milliseconds",
			"2026-08-01T10:11:12.345+02:00",
			ptr(time.Date(2026, 8, 1, 10, 11, 12, 345000000, time.FixedZone("", 2*3600))),
		},
		{
			"rfc3339 zulu",
			"2026-08-01T08:11:12Z",
			ptr(time.Date(2026, 8, 1, 8, 11, 12, 0, time.UTC)),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := ConfluencePage{}
			p.Version.When = tc.in
			got := p.VersionWhen()
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("want nil, got %v", got)
			case tc.want != nil && got == nil:
				t.Fatalf("want %v, got nil", tc.want)
			case tc.want != nil && !got.Equal(*tc.want):
				t.Fatalf("want %v, got %v", tc.want, got)
			}
		})
	}
}

func ptr(t time.Time) *time.Time { return &t }

// ---------------------------------------------------------------------------
// The ingest path
// ---------------------------------------------------------------------------

// recordingFileStore captures every CreateConfluenceFile call so a test can
// assert on the published_at the sync path passed down to the INSERT.
type recordingFileStore struct {
	fakeConfluenceStore
	created []CreateConfluenceFileData
}

func (s *recordingFileStore) CreateConfluenceFile(_ context.Context, d CreateConfluenceFileData) (*ConfluenceFileRow, error) {
	s.created = append(s.created, d)
	return &ConfluenceFileRow{ID: "file-1", Name: d.Name, Type: d.Type}, nil
}

// unreachableAsynqClient is an asynq client pointed at a closed port: every
// Enqueue fails fast, and the sync path only logs enqueue failures. That keeps
// these tests free of a Redis dependency while still exercising the real call.
func unreachableAsynqClient(t *testing.T) *asynq.Client {
	t.Helper()
	c := asynq.NewClient(asynq.RedisClientOpt{
		Addr:         "127.0.0.1:1",
		DialTimeout:  50 * time.Millisecond,
		ReadTimeout:  50 * time.Millisecond,
		WriteTimeout: 50 * time.Millisecond,
	})
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func importPageDeps(t *testing.T, store ConfluenceStore) SyncDeps {
	t.Helper()
	return SyncDeps{
		Store:        store,
		AsynqClient:  unreachableAsynqClient(t),
		Storage:      fakeStorage{},
		ChunkService: fakeChunkDeleter{},
	}
}

// A Confluence page's own content date is the timestamp of the version we
// just fetched (W4-R10). The sync has no update path — a changed page is
// deleted and re-created through this same call — so this one assertion also
// covers "re-syncing a changed page rewrites published_at".
//
// Mutation: drop the PublishedAt field from the importPage create call →
// every subtest below fails.
func TestImportPageStoresPageVersionAsPublishedAt(t *testing.T) {
	store := &recordingFileStore{}
	page := ConfluencePage{ID: "p1", Title: "Betriebshandbuch", BodyHTML: "<p>Inhalt</p>"}
	page.Version.When = "2025-12-24T09:30:00.000Z"

	if err := importPage(context.Background(), importPageDeps(t, store),
		&ConfluenceSourceRow{ID: "src-1", KbID: "kb-1"}, nil, page, "https://cf.invalid"); err != nil {
		t.Fatalf("importPage: %v", err)
	}
	if len(store.created) != 1 {
		t.Fatalf("expected 1 created file, got %d", len(store.created))
	}
	got := store.created[0].PublishedAt
	want := time.Date(2025, 12, 24, 9, 30, 0, 0, time.UTC)
	if got == nil {
		t.Fatal("published_at must carry the page version date, got nil")
	}
	if !got.Equal(want) {
		t.Fatalf("published_at = %v, want %v", got, want)
	}
}

// Mutation: drop the files.ClampPublishedAt wrapper at the importPage create
// call → the far-future version date is stored verbatim and this fails. A
// page whose version timestamp is years ahead (a mis-set server clock, a
// migrated space) would otherwise be permanently the KB's newest document.
func TestImportPageClampsAFuturePageVersion(t *testing.T) {
	store := &recordingFileStore{}
	future := time.Now().Add(365 * 24 * time.Hour)
	page := ConfluencePage{ID: "p2", Title: "Zukunft", BodyHTML: "<p>Inhalt</p>"}
	page.Version.When = future.Format(time.RFC3339)

	before := time.Now()
	if err := importPage(context.Background(), importPageDeps(t, store),
		&ConfluenceSourceRow{ID: "src-1", KbID: "kb-1"}, nil, page, ""); err != nil {
		t.Fatalf("importPage: %v", err)
	}
	after := time.Now()

	got := store.created[0].PublishedAt
	if got == nil {
		t.Fatal("a future version date must be clamped, not dropped")
	}
	if got.Before(before) || got.After(after) {
		t.Fatalf("published_at = %v, want a timestamp inside [%v, %v]", got, before, after)
	}
}

// A page with no version expansion has no content date to report, and
// guessing one is worse than none: published_at stays NULL and every read
// site falls back to created_at through COALESCE.
func TestImportPageWithoutAVersionLeavesPublishedAtNil(t *testing.T) {
	store := &recordingFileStore{}
	page := ConfluencePage{ID: "p3", Title: "Ohne Version", BodyHTML: "<p>Inhalt</p>"}

	if err := importPage(context.Background(), importPageDeps(t, store),
		&ConfluenceSourceRow{ID: "src-1", KbID: "kb-1"}, nil, page, ""); err != nil {
		t.Fatalf("importPage: %v", err)
	}
	if got := store.created[0].PublishedAt; got != nil {
		t.Fatalf("published_at = %v, want nil", got)
	}
}

// Attachments keep published_at NULL (refinement of W4-R10): the REST shape
// this client reads carries no attachment date, and the parent page's version
// timestamp is the PAGE's content date — a decade-old PDF attached to a page
// edited yesterday must not read as brand new.
//
// Mutation: pass the page's version date through to the attachment create
// call → this fails.
func TestImportPageAttachmentsLeavePublishedAtNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/download/att.pdf" {
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = w.Write([]byte("%PDF-1.4"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []any{map[string]any{
				"id":         "att-1",
				"title":      "anhang.pdf",
				"metadata":   map[string]any{"mediaType": "application/pdf"},
				"extensions": map[string]any{"fileSize": float64(8)},
				"_links":     map[string]any{"download": "/download/att.pdf"},
			}},
			"size":  1,
			"limit": 50,
		})
	}))
	defer srv.Close()

	store := &recordingFileStore{}
	deps := importPageDeps(t, store)
	importPageAttachments(context.Background(), deps,
		&ConfluenceSourceRow{ID: "src-1", KbID: "kb-1"},
		NewConfluenceClient(srv.URL, "token"), "p1")

	if len(store.created) != 1 {
		t.Fatalf("expected 1 created attachment, got %d", len(store.created))
	}
	if got := store.created[0].PublishedAt; got != nil {
		t.Fatalf("attachment published_at = %v, want nil", got)
	}
}
