package recencylister

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/justrag/go-backend/internal/chat"
	"github.com/justrag/go-backend/internal/mcp/builtin"
)

type fakeDocsStore struct {
	recent      []builtin.RecentDocRow
	recentErr   error
	marker      []builtin.RecentDocRow
	markerErr   error
	gotKbID     string
	gotAfter    time.Time
	gotBefore   time.Time
	gotLimit    int
	gotNameRE   string
	markerLimit int
}

func (f *fakeDocsStore) RecentDocuments(ctx context.Context, kbID string, after, before time.Time, limit int) ([]builtin.RecentDocRow, error) {
	f.gotKbID, f.gotAfter, f.gotBefore, f.gotLimit = kbID, after, before, limit
	return f.recent, f.recentErr
}

func (f *fakeDocsStore) NameMarkerDocuments(ctx context.Context, kbID, nameRegex string, limit int) ([]builtin.RecentDocRow, error) {
	f.gotNameRE, f.markerLimit = nameRegex, limit
	return f.marker, f.markerErr
}

// TestRecentDocumentsConverts asserts field-by-field conversion from
// builtin.RecentDocRow to chat.RecencyDoc and that the args reach the
// underlying store unchanged. Mutation: dropping the ID/Name/CreatedAt
// assignment in toRecencyDocs (or passing the wrong kbID/after/before/limit
// through) fails this test.
func TestRecentDocumentsConverts(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	store := &fakeDocsStore{
		recent: []builtin.RecentDocRow{
			{ID: "f1", Name: "NEU WID-SEC-2026-0101.txt", Origin: "web", CreatedAt: now},
		},
	}
	a := &adapter{store: store}

	after := now.AddDate(0, 0, -7)
	before := now
	got, err := a.RecentDocuments(context.Background(), "kb-1", after, before, 50)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store.gotKbID != "kb-1" || !store.gotAfter.Equal(after) || !store.gotBefore.Equal(before) || store.gotLimit != 50 {
		t.Fatalf("args not passed through: kbID=%q after=%v before=%v limit=%d", store.gotKbID, store.gotAfter, store.gotBefore, store.gotLimit)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 doc, got %d", len(got))
	}
	want := chat.RecencyDoc{ID: "f1", Name: "NEU WID-SEC-2026-0101.txt", CreatedAt: now}
	if got[0] != want {
		t.Fatalf("conversion mismatch: got %+v want %+v", got[0], want)
	}
}

// TestRecentDocumentsPropagatesError asserts a store error is returned
// unwrapped rather than swallowed into an empty slice. Mutation: ignoring
// the store's error return fails this test.
func TestRecentDocumentsPropagatesError(t *testing.T) {
	wantErr := errors.New("db down")
	a := &adapter{store: &fakeDocsStore{recentErr: wantErr}}
	_, err := a.RecentDocuments(context.Background(), "kb-1", time.Now(), time.Now(), 10)
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected error to propagate, got %v", err)
	}
}

// TestDocumentsWithNameMarkerConverts asserts the name-marker arm converts
// rows and passes the regex + limit through. Mutation: swapping which
// method (RecentDocuments vs NameMarkerDocuments) DocumentsWithNameMarker
// calls fails this test (gotNameRE would stay empty).
func TestDocumentsWithNameMarkerConverts(t *testing.T) {
	now := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	store := &fakeDocsStore{
		marker: []builtin.RecentDocRow{
			{ID: "f2", Name: "UPDATE WID-SEC-2026-0101.txt", Origin: "web", CreatedAt: now},
		},
	}
	a := &adapter{store: store}

	got, err := a.DocumentsWithNameMarker(context.Background(), "kb-2", `\m(neu|new)\M`, 25)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store.gotNameRE != `\m(neu|new)\M` || store.markerLimit != 25 {
		t.Fatalf("args not passed through: nameRE=%q limit=%d", store.gotNameRE, store.markerLimit)
	}
	if len(got) != 1 || got[0].ID != "f2" {
		t.Fatalf("unexpected conversion result: %+v", got)
	}
}

// TestDocumentsWithNameMarkerPropagatesError asserts a marker-store error
// is returned unwrapped. Mutation: ignoring the store's error return fails
// this test.
func TestDocumentsWithNameMarkerPropagatesError(t *testing.T) {
	wantErr := errors.New("regex rejected")
	a := &adapter{store: &fakeDocsStore{markerErr: wantErr}}
	_, err := a.DocumentsWithNameMarker(context.Background(), "kb-1", "bad(", 10)
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected error to propagate, got %v", err)
	}
}

// TestNewReturnsRecencyLister is a compile-time-adjacent smoke test that
// New's return value satisfies chat.RecencyLister with a nil pool (no DB
// call is made by New itself). Mutation: changing New's return type away
// from chat.RecencyLister fails to compile, which is caught here.
func TestNewReturnsRecencyLister(t *testing.T) {
	var _ chat.RecencyLister = New(nil)
}
