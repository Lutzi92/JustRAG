package chat

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/vector"
)

type fakeRecencyLister struct {
	docs         []RecencyDoc
	markerDocs   []RecencyDoc
	err          error
	markerErr    error
	gotAfter     time.Time
	gotLimit     int
	gotMarker    string
	called       bool
	markerCalled bool
}

func (f *fakeRecencyLister) RecentDocuments(_ context.Context, _ string, after, _ time.Time, limit int) ([]RecencyDoc, error) {
	f.called = true
	f.gotAfter = after
	f.gotLimit = limit
	return f.docs, f.err
}

func (f *fakeRecencyLister) DocumentsWithNameMarker(_ context.Context, _ string, marker string, limit int) ([]RecencyDoc, error) {
	f.markerCalled = true
	f.gotMarker = marker
	return f.markerDocs, f.markerErr
}

func TestApplyRecencyListing(t *testing.T) {
	ctx := context.Background()
	// Fixed "now": 2026-07-02 10:00 Berlin.
	loc, _ := time.LoadLocation("Europe/Berlin")
	now := time.Date(2026, 7, 2, 10, 0, 0, 0, loc)

	lister := &fakeRecencyLister{docs: []RecencyDoc{
		{ID: "f1", Name: "NEU Citrix_ab12.md", CreatedAt: time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)},
		{ID: "f2", Name: "NEU OpenSSL_cd34.md", CreatedAt: time.Date(2026, 6, 30, 9, 0, 0, 0, time.UTC)},
	}}

	var opts vector.SearchOptions
	res := applyRecencyListing(ctx, nil, lister, "kb1", "Welche neuen Meldungen gibt es?", &opts, now)

	if !res.fired {
		t.Fatal("expected recency listing to fire for the production failure query")
	}
	// Default window 7 days → CreatedAfter = 2026-06-25 00:00 Berlin.
	if opts.CreatedAfter == nil {
		t.Fatal("CreatedAfter not set")
	}
	wantCutoff := time.Date(2026, 6, 25, 0, 0, 0, 0, loc)
	if !opts.CreatedAfter.Equal(wantCutoff) {
		t.Errorf("CreatedAfter = %v, want %v", opts.CreatedAfter, wantCutoff)
	}
	if res.sinceISO != "2026-06-25" {
		t.Errorf("sinceISO = %q, want 2026-06-25", res.sinceISO)
	}
	if len(res.entries) != 2 || !strings.Contains(res.entries[0], "NEU Citrix_ab12.md") || !strings.Contains(res.entries[0], "2026-07-01") {
		t.Errorf("entries = %v", res.entries)
	}
	if res.truncated {
		t.Error("2 docs with cap 50 must not be truncated")
	}

	// Explicit window in the query overrides the configured default.
	var opts2 vector.SearchOptions
	res2 := applyRecencyListing(ctx, nil, lister, "kb1", "Welche Artikel wurden in den letzten 5 Tagen veröffentlicht?", &opts2, now)
	if !res2.fired || opts2.CreatedAfter == nil || !opts2.CreatedAfter.Equal(time.Date(2026, 6, 27, 0, 0, 0, 0, loc)) {
		t.Errorf("explicit 5-day window: fired=%v CreatedAfter=%v, want 2026-06-27 00:00 Berlin", res2.fired, opts2.CreatedAfter)
	}

	// Non-recency query → no-op.
	var opts3 vector.SearchOptions
	if res3 := applyRecencyListing(ctx, nil, lister, "kb1", "Welche Schwachstellen betreffen Citrix?", &opts3, now); res3.fired || opts3.CreatedAfter != nil {
		t.Error("topical query must not fire")
	}

	// Kill switch off → no-op.
	off := &fakeSiteConfigReader{values: map[string]*string{"chat_recency_listing_enabled": strPtr("false")}}
	var opts4 vector.SearchOptions
	if res4 := applyRecencyListing(ctx, off, lister, "kb1", "Welche neuen Meldungen gibt es?", &opts4, now); res4.fired || opts4.CreatedAfter != nil {
		t.Error("disabled flag must be a no-op")
	}

	// Nil lister → no-op (public API / openaicompat callers).
	var opts5 vector.SearchOptions
	if res5 := applyRecencyListing(ctx, nil, nil, "kb1", "Welche neuen Meldungen gibt es?", &opts5, now); res5.fired || opts5.CreatedAfter != nil {
		t.Error("nil lister must be a no-op")
	}

	// Lister error → fail open: no window, no addendum (a wrong "(keine)"
	// listing would be worse than the legacy behavior).
	broken := &fakeRecencyLister{err: errors.New("db down")}
	var opts6 vector.SearchOptions
	if res6 := applyRecencyListing(ctx, nil, broken, "kb1", "Welche neuen Meldungen gibt es?", &opts6, now); res6.fired || opts6.CreatedAfter != nil {
		t.Error("lister error must fail open to legacy retrieval")
	}

	// At-cap listing reports truncated.
	many := make([]RecencyDoc, 50)
	for i := range many {
		many[i] = RecencyDoc{Name: "f.md", CreatedAt: now}
	}
	full := &fakeRecencyLister{docs: many}
	var opts7 vector.SearchOptions
	if res7 := applyRecencyListing(ctx, nil, full, "kb1", "Welche neuen Meldungen gibt es?", &opts7, now); !res7.truncated {
		t.Error("at-cap listing must report truncated")
	}
}

// Name-marker arm: files labeled "NEU" in the NAME target the labeled
// subset ("neue Meldungen" in a CERT KB = NEU vs UPDATE advisories), so
// marker matches outside the window must appear in the listing and stay
// retrievable.
func TestApplyRecencyListingNameMarker(t *testing.T) {
	ctx := context.Background()
	loc, _ := time.LoadLocation("Europe/Berlin")
	now := time.Date(2026, 7, 2, 10, 0, 0, 0, loc)

	inWindow := RecencyDoc{ID: "f1", Name: "NEU Citrix_ab12.md", CreatedAt: time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)}
	oldMarker := RecencyDoc{ID: "f9", Name: "NEU Altes Advisory_9999.md", CreatedAt: time.Date(2026, 5, 12, 8, 0, 0, 0, time.UTC)}
	lister := &fakeRecencyLister{
		docs:       []RecencyDoc{inWindow},
		markerDocs: []RecencyDoc{inWindow, oldMarker},
	}

	var opts vector.SearchOptions
	res := applyRecencyListing(ctx, nil, lister, "kb1", "Welche neuen Meldungen gibt es?", &opts, now)
	if !res.fired || !lister.markerCalled {
		t.Fatalf("fired=%v markerCalled=%v, want both true", res.fired, lister.markerCalled)
	}
	// Both docs listed; the out-of-window marker doc annotated.
	if len(res.entries) != 2 {
		t.Fatalf("entries = %v, want 2", res.entries)
	}
	if !strings.Contains(res.entries[1], "NEU Altes Advisory_9999.md") || !strings.Contains(res.entries[1], "2026-05-12") {
		t.Errorf("marker entry = %q", res.entries[1])
	}
	// Retrieval widened via explicit FileIDs union instead of the window.
	if opts.CreatedAfter != nil {
		t.Error("CreatedAfter must be unset when the FileIDs union is used")
	}
	if len(opts.FileIDs) != 2 {
		t.Errorf("FileIDs = %v, want union of f1+f9", opts.FileIDs)
	}

	// Query without "neu"/"new" → no marker lookup, plain window path.
	plain := &fakeRecencyLister{docs: []RecencyDoc{inWindow}, markerDocs: []RecencyDoc{oldMarker}}
	var opts2 vector.SearchOptions
	res2 := applyRecencyListing(ctx, nil, plain, "kb1", "Was wurde zuletzt hinzugefügt?", &opts2, now)
	if plain.markerCalled {
		t.Error("marker lookup must not run without a neu/new mention")
	}
	if !res2.fired || opts2.CreatedAfter == nil {
		t.Error("window path must stay active")
	}

	// Kill switch for the marker arm.
	off := &fakeSiteConfigReader{values: map[string]*string{"chat_recency_listing_name_match_enabled": strPtr("false")}}
	offLister := &fakeRecencyLister{docs: []RecencyDoc{inWindow}, markerDocs: []RecencyDoc{oldMarker}}
	var opts3 vector.SearchOptions
	applyRecencyListing(ctx, off, offLister, "kb1", "Welche neuen Meldungen gibt es?", &opts3, now)
	if offLister.markerCalled {
		t.Error("disabled name-match must not query markers")
	}
	if opts3.CreatedAfter == nil {
		t.Error("window path must stay active when name-match is off")
	}

	// User file selection present → keep the window path (Search will
	// intersect); never overwrite the user's FileIDs with the union.
	sel := &fakeRecencyLister{docs: []RecencyDoc{inWindow}, markerDocs: []RecencyDoc{oldMarker}}
	opts4 := vector.SearchOptions{FileIDs: []string{"user-selected"}}
	applyRecencyListing(ctx, nil, sel, "kb1", "Welche neuen Meldungen gibt es?", &opts4, now)
	if opts4.CreatedAfter == nil || len(opts4.FileIDs) != 1 || opts4.FileIDs[0] != "user-selected" {
		t.Errorf("selection must be preserved: CreatedAfter=%v FileIDs=%v", opts4.CreatedAfter, opts4.FileIDs)
	}

	// Marker lookup failure → fail open to the window path.
	broken := &fakeRecencyLister{docs: []RecencyDoc{inWindow}, markerErr: errors.New("db down")}
	var opts5 vector.SearchOptions
	res5 := applyRecencyListing(ctx, nil, broken, "kb1", "Welche neuen Meldungen gibt es?", &opts5, now)
	if !res5.fired || opts5.CreatedAfter == nil || len(res5.entries) != 1 {
		t.Errorf("marker failure must keep the window listing: fired=%v CreatedAfter=%v entries=%v", res5.fired, opts5.CreatedAfter, res5.entries)
	}
}
