package processor

import (
	"context"
	"errors"
	"testing"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/parser"
	"github.com/justrag/go-backend/internal/parser/docling"
)

func TestParseConfigHash_IdentityMattersLiveDoclingEnabledDoesNot(t *testing.T) {
	ctx := context.Background()
	r := &fakeSiteConfigReader{values: map[string]*string{}}
	if ParseConfigHash(ctx, r, "builtin") == ParseConfigHash(ctx, r, "docling:http://a") {
		t.Error("identity must change the hash")
	}
	if ParseConfigHash(ctx, r, "docling:http://a") == ParseConfigHash(ctx, r, "docling:http://b") {
		t.Error("sidecar URL must change the hash")
	}
	on := &fakeSiteConfigReader{values: map[string]*string{"docling_enabled": strPtr("true")}}
	if ParseConfigHash(ctx, r, "builtin") != ParseConfigHash(ctx, on, "builtin") {
		t.Error("live docling_enabled must not influence the hash")
	}
}

type stubFront struct {
	err error
	txt string
}

func (s *stubFront) Name() string              { return "stub" }
func (s *stubFront) CanParse(_, _ string) bool { return true }
func (s *stubFront) Parse(context.Context, parser.ParseContext) (*parser.ParseResult, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &parser.ParseResult{Text: s.txt}, nil
}

// A Docling fallback parse is used but never cached; a primary success is.
func TestProcessFile_DegradedParseNotCached(t *testing.T) {
	for _, tc := range []struct {
		name      string
		primary   *stubFront
		wantEntry bool
	}{
		{"primary fails", &stubFront{err: errors.New("sidecar timeout")}, false},
		{"primary ok", &stubFront{txt: "docling text"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			fb := &docling.FallbackParser{Primary: tc.primary, Fallback: &parser.TextParser{}}
			cache := NewParseCache(newTestStorage(t), "docling:http://x")
			p := NewProcessor(parser.DefaultFactoryWith(nil, fb),
				ai.NewConfigResolver(noProviderConfigStore{}), nil, &mockStore{})
			p.SetSiteConfigReader(&fakeSiteConfigReader{values: map[string]*string{}})
			p.SetParseCache(cache)
			_, _ = p.ProcessFileWithResult(ctx, ProcessFileInput{
				FileID: "f-d", FilePath: writeTempText(t, "builtin text"), FileName: "doc.txt",
				MimeType: "text/plain", KBID: "kb-1", UserFileID: "uf-d", OwnerUserID: "owner-1",
			})
			key := ParseCacheKey("owner-1", "uf-d", ParseConfigHash(ctx, p.siteConfigReader, cache.Identity()))
			_, ok, err := cache.Get(ctx, key)
			if err != nil {
				t.Fatal(err)
			}
			if ok != tc.wantEntry {
				t.Errorf("cache entry exists = %v, want %v", ok, tc.wantEntry)
			}
		})
	}
}

// A degraded parse completes the ingest but must not stamp an index
// fingerprint: the fingerprint claims the preferred parser, and copy mode
// would otherwise propagate the fallback's weaker index to later KB copies.
func TestProcessFile_DegradedParseWritesNoFingerprint(t *testing.T) {
	for _, tc := range []struct {
		name    string
		primary *stubFront
		want    bool
	}{
		{"primary fails", &stubFront{err: errors.New("sidecar timeout")}, false},
		{"primary ok", &stubFront{txt: "   "}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := &mockStore{}
			fb := &docling.FallbackParser{Primary: tc.primary, Fallback: &parser.TextParser{}}
			p := NewProcessor(parser.DefaultFactoryWith(nil, fb), ai.NewConfigResolver(fpBase()), nil, st)
			p.SetSiteConfigReader(&fakeSiteConfigReader{values: map[string]*string{}})
			in := fpInput("uf-1")
			// Whitespace only: completes with no chunks, no embedder needed.
			in.FilePath = writeTempText(t, "   ")
			if err := p.ProcessFile(context.Background(), in); err != nil {
				t.Fatalf("ProcessFile: %v", err)
			}
			if got := st.fingerprints["f1"] != ""; got != tc.want {
				t.Errorf("fingerprint written = %v, want %v (statuses %v)", got, tc.want, st.statuses)
			}
		})
	}
}
