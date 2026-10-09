package processor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/parser"
	"github.com/justrag/go-backend/internal/storage"
)

func newTestStorage(t *testing.T) storage.Storage {
	t.Helper()
	s, err := storage.New(storage.Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	return s
}

func TestParseCache_RoundTripAndMiss(t *testing.T) {
	ctx := context.Background()
	c := NewParseCache(newTestStorage(t), "builtin")
	key := ParseCacheKey("owner", "uf", "abc")
	if key != "users/owner/parses/uf/abc.json" {
		t.Fatalf("key = %q", key)
	}
	if !strings.HasPrefix(key, ParseCacheDir("owner", "uf")) {
		t.Fatalf("key %q not under dir %q", key, ParseCacheDir("owner", "uf"))
	}
	if r, ok, err := c.Get(ctx, key); err != nil || ok || r != nil {
		t.Fatalf("miss = (%v,%v,%v)", r, ok, err)
	}
	want := &parser.ParseResult{
		Text:       "hello",
		IsMarkdown: true,
		Pages:      []parser.PageText{{PageNumber: 1, Text: "a"}, {PageNumber: 2, Text: "b"}},
	}
	if err := c.Put(ctx, key, want); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, ok, err := c.Get(ctx, key)
	if err != nil || !ok {
		t.Fatalf("Get = (%v,%v)", ok, err)
	}
	if got.Text != want.Text || got.IsMarkdown != want.IsMarkdown || len(got.Pages) != 2 ||
		got.Pages[1].PageNumber != 2 || got.Pages[1].Text != "b" {
		t.Fatalf("round trip mismatch: %+v", got)
	}
}

func TestParseConfigHash_ChangesPerKey(t *testing.T) {
	ctx := context.Background()
	base := ParseConfigHash(ctx, &fakeSiteConfigReader{values: map[string]*string{}}, "builtin")
	if base != ParseConfigHash(ctx, &fakeSiteConfigReader{values: map[string]*string{}}, "builtin") {
		t.Fatal("hash must be stable")
	}
	keys := append([]string{"describe_image_model"}, parseConfigKeys...)
	seen := map[string]string{base: "<empty>"}
	for _, k := range keys {
		h := ParseConfigHash(ctx, &fakeSiteConfigReader{values: map[string]*string{k: strPtr("x")}}, "builtin")
		if h == base {
			t.Errorf("changing %s did not change the hash", k)
		}
		if prev, dup := seen[h]; dup {
			t.Errorf("%s and %s collide", k, prev)
		}
		seen[h] = k
	}
	// model_tier_fast feeds describe_image_model when that is unset.
	h := ParseConfigHash(ctx, &fakeSiteConfigReader{values: map[string]*string{"model_tier_fast": strPtr("m")}}, "builtin")
	if h == base {
		t.Error("model_tier_fast must influence the hash through describe_image_model")
	}
}

func TestParseCacheable(t *testing.T) {
	cases := []struct {
		mime, name string
		want       bool
	}{
		{"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "a.xlsx", false},
		{"text/csv", "a.csv", false},
		{"image/png", "a.png", false},
		{"audio/mpeg", "a.mp3", false},
		{"application/pdf", "a.pdf", true},
		{"application/vnd.openxmlformats-officedocument.wordprocessingml.document", "a.docx", true},
		{"text/plain", "a.txt", true},
	}
	for _, c := range cases {
		if got := parseCacheable(c.mime, c.name); got != c.want {
			t.Errorf("parseCacheable(%q,%q) = %v, want %v", c.mime, c.name, got, c.want)
		}
	}
}

const cachedMarker = "ZZ-CACHED-MARKER-ZZ " + injectionText

// A library file with a seeded cache entry uses the cached text (observable
// via the rss-origin injection screen) and the parser output is ignored.
func TestProcessFile_ParseCacheHitUsesCachedText(t *testing.T) {
	ctx := context.Background()
	cache := NewParseCache(newTestStorage(t), "builtin")
	store := &mockStore{origins: map[string]string{"f-lib": "rss"}}
	p := newScreeningProcessor(store, nil)
	p.SetParseCache(cache)

	key := ParseCacheKey("owner-1", "uf-1", ParseConfigHash(ctx, p.siteConfigReader, "builtin"))
	if err := cache.Put(ctx, key, &parser.ParseResult{Text: cachedMarker}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "doc.txt")
	if err := os.WriteFile(path, []byte("harmless text from the real parser"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _ := p.ProcessFileWithResult(ctx, ProcessFileInput{
		FileID: "f-lib", FilePath: path, FileName: "doc.txt", MimeType: "text/plain", KBID: "kb-1",
		UserFileID: "uf-1", OwnerUserID: "owner-1",
	})
	if !out.ParseCacheHit {
		t.Error("ParseCacheHit must be true")
	}
	raw, ok := store.injectionDetails["f-lib"]
	if !ok || !strings.Contains(string(raw), "Ignore all previous instructions") {
		t.Fatalf("cached text was not what got screened: %s", raw)
	}
}

// A miss parses and then writes the cache entry.
func TestProcessFile_ParseCacheMissWritesEntry(t *testing.T) {
	ctx := context.Background()
	cache := NewParseCache(newTestStorage(t), "builtin")
	store := &mockStore{}
	p := newScreeningProcessor(store, nil)
	p.SetParseCache(cache)
	path := writeTempText(t, "real parser text")
	out, _ := p.ProcessFileWithResult(ctx, ProcessFileInput{
		FileID: "f-miss", FilePath: path, FileName: "doc.txt", MimeType: "text/plain", KBID: "kb-1",
		UserFileID: "uf-2", OwnerUserID: "owner-1",
	})
	if out.ParseCacheHit {
		t.Error("first run must not be a hit")
	}
	key := ParseCacheKey("owner-1", "uf-2", ParseConfigHash(ctx, p.siteConfigReader, "builtin"))
	got, ok, err := cache.Get(ctx, key)
	if err != nil || !ok || !strings.Contains(got.Text, "real parser text") {
		t.Fatalf("cache not written: ok=%v err=%v got=%+v", ok, err, got)
	}
}

// Without a UserFileID the cache is never consulted or written.
func TestProcessFile_NoUserFileIDNeverTouchesCache(t *testing.T) {
	ctx := context.Background()
	cache := NewParseCache(newTestStorage(t), "builtin")
	store := &mockStore{origins: map[string]string{"f-plain": "rss"}}
	p := newScreeningProcessor(store, nil)
	p.SetParseCache(cache)
	key := ParseCacheKey("owner-1", "uf-1", ParseConfigHash(ctx, p.siteConfigReader, "builtin"))
	if err := cache.Put(ctx, key, &parser.ParseResult{Text: cachedMarker}); err != nil {
		t.Fatal(err)
	}
	path := writeTempText(t, "plain text only")
	out, _ := p.ProcessFileWithResult(ctx, ProcessFileInput{
		FileID: "f-plain", FilePath: path, FileName: "doc.txt", MimeType: "text/plain", KBID: "kb-1",
	})
	if out.ParseCacheHit {
		t.Error("no UserFileID must never hit")
	}
	if raw, ok := store.injectionDetails["f-plain"]; ok {
		t.Errorf("cached text leaked into a non-library run: %s", raw)
	}
}
