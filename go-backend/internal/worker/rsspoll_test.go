package worker

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/mmcdole/gofeed"

	"github.com/justrag/go-backend/internal/fetcher"
	"github.com/justrag/go-backend/internal/files"
	"github.com/justrag/go-backend/internal/kbaccess"
	"github.com/justrag/go-backend/internal/rss"
	"github.com/justrag/go-backend/internal/storage"
	"github.com/justrag/go-backend/internal/widcert"
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
	out := resolveRSSItemContent(context.Background(), f, nil, stubSiteConfig{}, feed, feedItem())
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
	out := resolveRSSItemContent(context.Background(), f, nil, stubSiteConfig{}, feed, feedItem())
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
	out := resolveRSSItemContent(context.Background(), f, nil, stubSiteConfig{}, feed, feedItem())
	if !strings.Contains(out, "short summary") {
		t.Errorf("expected fallback to feed content, got: %q", out)
	}
}

func TestResolveRSSItemContent_EmptyMarkdownFallsBack(t *testing.T) {
	f := &stubFetcher{res: &fetcher.Result{Markdown: "   "}}
	feed := &rss.RSSFeedRow{ID: "f1", FetchFullText: true}
	out := resolveRSSItemContent(context.Background(), f, nil, stubSiteConfig{}, feed, feedItem())
	if !strings.Contains(out, "short summary") {
		t.Errorf("expected fallback on empty markdown, got: %q", out)
	}
}

func TestResolveRSSItemContent_DisabledSkipsFetch(t *testing.T) {
	f := &stubFetcher{res: &fetcher.Result{Markdown: "FULL ARTICLE BODY"}}
	feed := &rss.RSSFeedRow{ID: "f1", FetchFullText: false}
	out := resolveRSSItemContent(context.Background(), f, nil, stubSiteConfig{}, feed, feedItem())
	if f.called {
		t.Error("fetcher should not be called when FetchFullText is false")
	}
	if !strings.Contains(out, "short summary") {
		t.Errorf("expected feed content, got: %q", out)
	}
}

type stubWID struct {
	adv    *widcert.Advisory
	err    error
	called bool
}

func (s *stubWID) Fetch(_ context.Context, name string) (*widcert.Advisory, error) {
	s.called = true
	if s.err != nil {
		return nil, s.err
	}
	return s.adv, nil
}

type stubSiteConfig struct{ val *string }

func (s stubSiteConfig) GetSiteConfigValue(_ context.Context, _ string) (*string, error) {
	return s.val, nil
}

func widItem() *gofeed.Item {
	return &gofeed.Item{
		Title:       "WID-SEC-2026-2038: OpenSSL",
		Link:        "https://wid.cert-bund.de/portal/wid/WID-SEC-2026-2038",
		Description: "short summary",
	}
}

func TestResolveRSSItemContent_WIDEnriched(t *testing.T) {
	wid := &stubWID{adv: &widcert.Advisory{Name: "WID-SEC-2026-2038", Title: "OpenSSL", CVEs: []string{"CVE-2026-1"}}}
	feed := &rss.RSSFeedRow{ID: "f1", FetchFullText: true}
	out := resolveRSSItemContent(context.Background(), nil, wid, stubSiteConfig{}, feed, widItem())
	if !wid.called {
		t.Fatal("WID client should have been called for a wid.cert-bund.de link")
	}
	if !strings.Contains(out, "# WID-SEC-2026-2038: OpenSSL") || !strings.Contains(out, "CVE-2026-1") {
		t.Errorf("expected structured WID markdown, got: %q", out)
	}
}

func TestResolveRSSItemContent_WIDKillSwitchOff(t *testing.T) {
	off := "false"
	wid := &stubWID{adv: &widcert.Advisory{Name: "x"}}
	f := &stubFetcher{res: &fetcher.Result{Markdown: longArticle}}
	feed := &rss.RSSFeedRow{ID: "f1", FetchFullText: true}
	out := resolveRSSItemContent(context.Background(), f, wid, stubSiteConfig{val: &off}, feed, widItem())
	if wid.called {
		t.Error("WID client must not be called when kill switch is off")
	}
	if !strings.Contains(out, "Detailed advisory body") {
		t.Errorf("expected fallback to generic full-text path, got: %q", out)
	}
}

func TestResolveRSSItemContent_WIDFetchErrorFallsBack(t *testing.T) {
	wid := &stubWID{err: errors.New("boom")}
	feed := &rss.RSSFeedRow{ID: "f1", FetchFullText: true}
	out := resolveRSSItemContent(context.Background(), nil, wid, stubSiteConfig{}, feed, widItem())
	if !strings.Contains(out, "short summary") {
		t.Errorf("expected fallback to feed summary on WID error, got: %q", out)
	}
}

func TestResolveRSSItemContent_NonWIDUnaffected(t *testing.T) {
	wid := &stubWID{adv: &widcert.Advisory{Name: "x"}}
	f := &stubFetcher{res: &fetcher.Result{Markdown: longArticle}}
	feed := &rss.RSSFeedRow{ID: "f1", FetchFullText: true}
	out := resolveRSSItemContent(context.Background(), f, wid, stubSiteConfig{}, feed, feedItem()) // example.com link
	if wid.called {
		t.Error("WID client must not be called for non-WID links")
	}
	if !strings.Contains(out, "Detailed advisory body") {
		t.Errorf("non-WID link should use generic full-text path, got: %q", out)
	}
}

// ---------------------------------------------------------------------------
// rollbackRSSItem
// ---------------------------------------------------------------------------

// fakeFileStore implements files.Store; only DeleteFileRecord is exercised
// by rollbackRSSItem, but Go requires every method.
type fakeFileStore struct {
	events        *[]string
	deletedFileID string
}

var _ files.Store = (*fakeFileStore)(nil)

func (f *fakeFileStore) GetFileByID(context.Context, string) (*files.FileInfo, error) {
	return nil, nil
}
func (f *fakeFileStore) DeleteFileRecord(_ context.Context, id string) error {
	if f.events != nil {
		*f.events = append(*f.events, "delete-record:"+id)
	}
	f.deletedFileID = id
	return nil
}
func (f *fakeFileStore) GetKBByID(context.Context, string) (*kbaccess.KnowledgeBase, error) {
	return nil, nil
}
func (f *fakeFileStore) GetKBRole(context.Context, string, string) (string, error) { return "", nil }
func (f *fakeFileStore) CreateFile(context.Context, files.CreateFileData) (*files.FileRecord, error) {
	return nil, nil
}
func (f *fakeFileStore) GetKBFileLimits(context.Context, string) (*files.KBFileLimits, error) {
	return nil, nil
}
func (f *fakeFileStore) ResetFileForRetry(context.Context, string) (bool, error) { return false, nil }
func (f *fakeFileStore) ListErrorFiles(context.Context, string) ([]*files.FileInfo, error) {
	return nil, nil
}
func (f *fakeFileStore) MarkFileError(context.Context, string, string, string) error { return nil }

// fakeRSSStorage implements storage.Storage as pure no-ops, recording the
// one path rollbackRSSItem deletes.
type fakeRSSStorage struct {
	events             *[]string
	deletedStoragePath string
}

var _ storage.Storage = (*fakeRSSStorage)(nil)

func (s *fakeRSSStorage) StoreFile(context.Context, string, []byte, string) error { return nil }
func (s *fakeRSSStorage) StoreFileFromReader(context.Context, string, io.Reader, string) error {
	return nil
}
func (s *fakeRSSStorage) ReadFile(context.Context, string) ([]byte, error) { return nil, nil }
func (s *fakeRSSStorage) ReadFileStream(context.Context, string) (io.ReadCloser, error) {
	return nil, nil
}
func (s *fakeRSSStorage) DeleteFile(_ context.Context, storagePath string) error {
	if s.events != nil {
		*s.events = append(*s.events, "delete-file:"+storagePath)
	}
	s.deletedStoragePath = storagePath
	return nil
}
func (s *fakeRSSStorage) DeleteFiles(context.Context, []string) error   { return nil }
func (s *fakeRSSStorage) DeleteDirectory(context.Context, string) error { return nil }
func (s *fakeRSSStorage) FileExists(context.Context, string) (bool, error) {
	return false, nil
}
func (s *fakeRSSStorage) IsS3() bool { return false }

// fakeRSSTableDropper implements rssTableDropper, recording each call (and
// its position in a shared event log) so the ordering test can assert it
// ran BEFORE the file-record delete.
type fakeRSSTableDropper struct {
	events  *[]string
	dropped []string
}

func (d *fakeRSSTableDropper) DropTablesForFile(_ context.Context, fileID string) error {
	if d.events != nil {
		*d.events = append(*d.events, "drop:"+fileID)
	}
	d.dropped = append(d.dropped, fileID)
	return nil
}

// TestRollbackRSSItemDropsTablesBeforeDeletingRecord pins the Phase-3 carry:
// rollbackRSSItem must drop a file's materialised spreadsheet tables BEFORE
// it deletes the files row (see rssTableDropper's doc comment for why
// deleting the row first orphans them beyond any future reach).
func TestRollbackRSSItemDropsTablesBeforeDeletingRecord(t *testing.T) {
	var events []string
	dropper := &fakeRSSTableDropper{events: &events}
	fileStore := &fakeFileStore{events: &events}
	stor := &fakeRSSStorage{events: &events}
	deps := RSSPollDeps{
		FileStore:    fileStore,
		Storage:      stor,
		TableDropper: dropper,
	}

	rollbackRSSItem(context.Background(), deps, "file-1", "rss/kb/file-1.md")

	wantEvents := []string{"drop:file-1", "delete-record:file-1", "delete-file:rss/kb/file-1.md"}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Errorf("event order = %v, want %v", events, wantEvents)
	}
	if want := []string{"file-1"}; !reflect.DeepEqual(dropper.dropped, want) {
		t.Errorf("dropped = %v, want %v", dropper.dropped, want)
	}
}

// TestRollbackRSSItemNilDropperIsNoop pins the nil-safety half: a
// deployment without a main pool (or a caller that never wired one) must
// still roll back the file record and storage blob, unaffected.
func TestRollbackRSSItemNilDropperIsNoop(t *testing.T) {
	fileStore := &fakeFileStore{}
	stor := &fakeRSSStorage{}
	deps := RSSPollDeps{
		FileStore: fileStore,
		Storage:   stor,
		// TableDropper deliberately left nil.
	}

	rollbackRSSItem(context.Background(), deps, "file-1", "rss/kb/file-1.md")

	if fileStore.deletedFileID != "file-1" {
		t.Errorf("deletedFileID = %q, want %q", fileStore.deletedFileID, "file-1")
	}
	if stor.deletedStoragePath != "rss/kb/file-1.md" {
		t.Errorf("deletedStoragePath = %q, want %q", stor.deletedStoragePath, "rss/kb/file-1.md")
	}
}
