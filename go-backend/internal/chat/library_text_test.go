package chat

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/libpaths"
	"github.com/justrag/go-backend/internal/parser"
	"github.com/justrag/go-backend/internal/storage"
	"github.com/justrag/go-backend/internal/userfiles"
)

type degradedParser struct{}

func (degradedParser) Name() string              { return "degraded" }
func (degradedParser) CanParse(_, f string) bool { return f == "d.degr" }
func (degradedParser) Parse(context.Context, parser.ParseContext) (*parser.ParseResult, error) {
	return &parser.ParseResult{Text: "fallback", Degraded: true}, nil
}

func libFixture(t *testing.T, name, body string) (*LibraryTextSource, storage.Storage, *userfiles.UserFile) {
	t.Helper()
	stor, err := storage.New(storage.Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	f := parser.DefaultFactoryWith(nil, degradedParser{})
	uf := &userfiles.UserFile{ID: "uf1", OwnerUserID: "u1", Name: name, Mime: "text/plain", StoragePath: "users/u1/blob-" + name}
	if err := stor.StoreFile(context.Background(), uf.StoragePath, []byte(body), "text/plain"); err != nil {
		t.Fatal(err)
	}
	return NewLibraryTextSource(stor, f), stor, uf
}

func TestLibraryTextSource_CachesAndServesFromCache(t *testing.T) {
	ctx := context.Background()
	s, stor, uf := libFixture(t, "a.txt", "hello library")
	res, err := s.Text(ctx, uf)
	if err != nil || res.Text == "" {
		t.Fatalf("first: %v %+v", err, res)
	}
	key := libpaths.ChatTextKey("u1", "uf1")
	if ok, _ := stor.FileExists(ctx, key); !ok {
		t.Fatal("cache object not written at ChatTextKey")
	}
	if err := stor.DeleteFile(ctx, uf.StoragePath); err != nil {
		t.Fatal(err)
	}
	res2, err := s.Text(ctx, uf)
	if err != nil || res2.Text != res.Text {
		t.Fatalf("second should hit cache: %v %+v", err, res2)
	}
}

func TestLibraryTextSource_DegradedNotCached(t *testing.T) {
	ctx := context.Background()
	s, stor, uf := libFixture(t, "d.degr", "x")
	res, err := s.Text(ctx, uf)
	if err != nil || !res.Degraded {
		t.Fatalf("got %v %+v", err, res)
	}
	if ok, _ := stor.FileExists(ctx, libpaths.ChatTextKey("u1", "uf1")); ok {
		t.Fatal("degraded result must not be cached")
	}
}

func TestLibraryTextSource_Unparseable(t *testing.T) {
	// The default factory ends in a catch-all TextParser, so "no parser" needs
	// an empty factory.
	s, _, uf := libFixture(t, "a.bin", "x")
	s.factory = parser.NewFactory()
	if _, err := s.Text(context.Background(), uf); !errors.Is(err, ErrUnparseable) {
		t.Fatalf("no parser: want ErrUnparseable, got %v", err)
	}
}

func TestLibraryTextSource_MissingBlobIsOrdinaryError(t *testing.T) {
	s, stor, uf := libFixture(t, "a.txt", "x")
	_ = stor.DeleteFile(context.Background(), uf.StoragePath)
	_, err := s.Text(context.Background(), uf)
	if err == nil || errors.Is(err, ErrUnparseable) {
		t.Fatalf("want ordinary error, got %v", err)
	}
}

type failParser struct{}

func (failParser) Name() string              { return "fail" }
func (failParser) CanParse(_, f string) bool { return f == "f.fail" }
func (failParser) Parse(context.Context, parser.ParseContext) (*parser.ParseResult, error) {
	return nil, errors.New("secret internal detail")
}

func TestLibraryTextSource_ParserFailureIsBareSentinel(t *testing.T) {
	stor, _ := storage.New(storage.Config{DataDir: t.TempDir()})
	uf := &userfiles.UserFile{ID: "uf1", OwnerUserID: "u1", Name: "f.fail", Mime: "x/y", StoragePath: "b"}
	_ = stor.StoreFile(context.Background(), "b", []byte("x"), "x/y")
	s := NewLibraryTextSource(stor, parser.DefaultFactoryWith(nil, failParser{}))
	_, err := s.Text(context.Background(), uf)
	if err != ErrUnparseable {
		t.Fatalf("want bare ErrUnparseable, got %v", err)
	}
}

func TestLibraryTextSource_CanceledContextIsOrdinaryError(t *testing.T) {
	s, _, uf := libFixture(t, "a.txt", "x")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.Text(ctx, uf)
	if err == nil || errors.Is(err, ErrUnparseable) {
		t.Fatalf("want ordinary error, got %v", err)
	}
}

func TestLibraryTextSource_CorruptCacheReparsesAndOverwrites(t *testing.T) {
	ctx := context.Background()
	s, stor, uf := libFixture(t, "a.txt", "fresh")
	key := libpaths.ChatTextKey("u1", "uf1")
	_ = stor.StoreFile(ctx, key, []byte("{garbage"), "application/json")
	res, err := s.Text(ctx, uf)
	if err != nil || res.Text != "fresh" {
		t.Fatalf("got %v %+v", err, res)
	}
	raw, _ := stor.ReadFile(ctx, key)
	if string(raw) == "{garbage" {
		t.Fatal("corrupt cache not overwritten")
	}
}

// blockParser handles "b.blk": it reports each start on started, then waits
// for release (or its ctx), tracking the peak number of concurrent parses.
type blockParser struct {
	started chan struct{}
	release chan struct{}
	mu      sync.Mutex
	cur     int
	peak    int
}

func (*blockParser) Name() string              { return "block" }
func (*blockParser) CanParse(_, f string) bool { return f == "b.blk" }
func (p *blockParser) Parse(ctx context.Context, _ parser.ParseContext) (*parser.ParseResult, error) {
	p.mu.Lock()
	p.cur++
	if p.cur > p.peak {
		p.peak = p.cur
	}
	p.mu.Unlock()
	defer func() { p.mu.Lock(); p.cur--; p.mu.Unlock() }()
	p.started <- struct{}{}
	select {
	case <-p.release:
		return &parser.ParseResult{Text: "parsed"}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func blockFixture(t *testing.T, bp *blockParser, n int) (*LibraryTextSource, storage.Storage, []*userfiles.UserFile) {
	t.Helper()
	stor, err := storage.New(storage.Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ufs := make([]*userfiles.UserFile, n)
	for i := range ufs {
		ufs[i] = &userfiles.UserFile{ID: fmt.Sprintf("uf%d", i), OwnerUserID: "u1", Name: "b.blk", Mime: "x/y", StoragePath: fmt.Sprintf("blob%d", i)}
		if err := stor.StoreFile(context.Background(), ufs[i].StoragePath, []byte("x"), "x/y"); err != nil {
			t.Fatal(err)
		}
	}
	return NewLibraryTextSource(stor, parser.DefaultFactoryWith(nil, bp)), stor, ufs
}

func TestLibraryTextSource_ParseTimeout(t *testing.T) {
	bp := &blockParser{started: make(chan struct{}, 1), release: make(chan struct{})}
	s, stor, ufs := blockFixture(t, bp, 1)
	s.parseTimeout = 30 * time.Millisecond
	_, err := s.Text(context.Background(), ufs[0])
	if err != ErrLibraryParseTimeout {
		t.Fatalf("want ErrLibraryParseTimeout, got %v", err)
	}
	if ok, _ := stor.FileExists(context.Background(), libpaths.ChatTextKey("u1", "uf0")); ok {
		t.Fatal("a timed-out parse must not be cached")
	}
}

// A client that disconnects mid-parse does not cancel the parse: it finishes
// and is cached for the next turn.
func TestLibraryTextSource_ParseSurvivesRequestCancel(t *testing.T) {
	bp := &blockParser{started: make(chan struct{}, 1), release: make(chan struct{})}
	s, stor, ufs := blockFixture(t, bp, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := s.Text(ctx, ufs[0])
		done <- err
	}()
	<-bp.started
	cancel()
	time.Sleep(20 * time.Millisecond) // the parse must still be running
	close(bp.release)
	if err := <-done; err != nil {
		t.Fatalf("Text: %v", err)
	}
	if ok, _ := stor.FileExists(context.Background(), libpaths.ChatTextKey("u1", "uf0")); !ok {
		t.Fatal("the parse finished after the client left but was not cached")
	}
}

func TestLibraryTextSource_ParseConcurrencyBounded(t *testing.T) {
	const n = 6
	bp := &blockParser{started: make(chan struct{}, n), release: make(chan struct{})}
	s, _, ufs := blockFixture(t, bp, n)
	s.sem = make(chan struct{}, 2)
	var wg sync.WaitGroup
	for _, uf := range ufs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = s.Text(context.Background(), uf)
		}()
	}
	<-bp.started
	<-bp.started
	select {
	case <-bp.started:
		t.Fatal("a third parse started while two slots were held")
	case <-time.After(50 * time.Millisecond):
	}
	close(bp.release)
	wg.Wait()
	if bp.peak != 2 {
		t.Fatalf("peak concurrent parses = %d, want 2", bp.peak)
	}
	if libraryParseConcurrency < 1 || cap(NewLibraryTextSource(nil, nil).sem) != libraryParseConcurrency {
		t.Fatal("production source is not bounded by libraryParseConcurrency")
	}
}

// Waiting for a slot honours the request ctx.
func TestLibraryTextSource_SlotWaitHonoursContext(t *testing.T) {
	bp := &blockParser{started: make(chan struct{}, 1), release: make(chan struct{})}
	s, _, ufs := blockFixture(t, bp, 1)
	s.sem = make(chan struct{}, 1)
	s.sem <- struct{}{} // the only slot is taken
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := s.Text(ctx, ufs[0])
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrUnparseable) {
		t.Fatalf("want a ctx error, got %v", err)
	}
	select {
	case <-bp.started:
		t.Fatal("parsed without a slot")
	default:
	}
}

func TestChatTextKey(t *testing.T) {
	if got := libpaths.ChatTextKey("o", "f"); got != libpaths.ParseCacheDir("o", "f")+"chat-text.json" {
		t.Fatal(got)
	}
}

// deadlineStore records the ctx deadline each StoreFile call runs under.
type deadlineStore struct {
	storage.Storage
	mu        sync.Mutex
	key       string
	remaining time.Duration
	hasDL     bool
	ctxErr    error
}

func (d *deadlineStore) StoreFile(ctx context.Context, key string, data []byte, ct string) error {
	if key == libpaths.ChatTextKey("u1", "uf0") {
		d.mu.Lock()
		d.key = key
		dl, ok := ctx.Deadline()
		d.hasDL = ok
		d.remaining = time.Until(dl)
		d.ctxErr = ctx.Err()
		d.mu.Unlock()
	}
	return d.Storage.StoreFile(ctx, key, data, ct)
}

// The cache write has its own detached 10 s budget: a parse that used nearly
// all of its timeout still gets its result cached.
func TestLibraryTextSource_CacheWriteHasOwnBudget(t *testing.T) {
	bp := slowParser{d: 150 * time.Millisecond}
	s, stor, ufs := blockFixture(t, &blockParser{}, 1)
	ds := &deadlineStore{Storage: stor}
	s.stor = ds
	s.factory = parser.DefaultFactoryWith(nil, bp)
	s.parseTimeout = 200 * time.Millisecond
	ufs[0].Name = "s.slow"
	if _, err := s.Text(context.Background(), ufs[0]); err != nil {
		t.Fatalf("Text: %v", err)
	}
	ds.mu.Lock()
	defer ds.mu.Unlock()
	if !ds.hasDL || ds.ctxErr != nil {
		t.Fatalf("cache write ctx: deadline=%v err=%v", ds.hasDL, ds.ctxErr)
	}
	if ds.remaining < 5*time.Second {
		t.Fatalf("cache write budget = %v, want about 10s (not the leftover parse budget)", ds.remaining)
	}
	if ok, _ := stor.FileExists(context.Background(), libpaths.ChatTextKey("u1", "uf0")); !ok {
		t.Fatal("result not cached")
	}
}

type slowParser struct{ d time.Duration }

func (slowParser) Name() string              { return "slow" }
func (slowParser) CanParse(_, f string) bool { return f == "s.slow" }
func (p slowParser) Parse(context.Context, parser.ParseContext) (*parser.ParseResult, error) {
	time.Sleep(p.d)
	return &parser.ParseResult{Text: "slow text"}, nil
}
