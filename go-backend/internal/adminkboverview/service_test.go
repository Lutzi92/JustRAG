package adminkboverview

import (
	"context"
	"errors"
	"testing"

	"github.com/hibiken/asynq"
	"github.com/justrag/go-backend/internal/jobs"
)

// fakeStore returns canned data without touching Postgres.
type fakeStore struct {
	kbs     []KBBase
	fileMap map[string]FileStats
	msgMap  map[string]MessageStats
	listErr error
	fileErr error
	msgErr  error
}

func (f *fakeStore) ListKBs(context.Context) ([]KBBase, error) { return f.kbs, f.listErr }
func (f *fakeStore) FileStatsByKB(context.Context) (map[string]FileStats, error) {
	return f.fileMap, f.fileErr
}
func (f *fakeStore) MessageStatsByKB(context.Context) (map[string]MessageStats, error) {
	return f.msgMap, f.msgErr
}

// fakeInspector satisfies queueInspector.
type fakeInspector struct {
	info map[string]*asynq.QueueInfo
	err  error
}

func (f *fakeInspector) GetQueueInfo(qname string) (*asynq.QueueInfo, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.info[qname], nil
}

func strptr(s string) *string { return &s }

func TestOverview_MergesStatsByKBID(t *testing.T) {
	store := &fakeStore{
		kbs: []KBBase{
			{ID: "kb-1", Name: "Alpha", OwnerName: strptr("Ada Lovelace"), IsGlobal: false, IsPublished: true, CreatedAt: "2026-01-01T00:00:00Z"},
			{ID: "kb-2", Name: "Beta", OwnerName: nil, IsGlobal: true, IsPublished: false, CreatedAt: "2026-02-01T00:00:00Z"},
		},
		fileMap: map[string]FileStats{
			"kb-1": {FileCount: 10, TotalSizeBytes: 2048, FailedFileCount: 1, ProcessingFileCount: 2, LastFileUploadAt: strptr("2026-05-01T00:00:00Z")},
		},
		msgMap: map[string]MessageStats{
			"kb-1": {MessageCount: 42, ChatCount: 7, LastMessageAt: strptr("2026-05-20T00:00:00Z")},
		},
	}
	svc := NewService(store, &fakeInspector{info: map[string]*asynq.QueueInfo{}})

	resp, err := svc.Overview(context.Background())
	if err != nil {
		t.Fatalf("Overview returned error: %v", err)
	}
	if len(resp.Rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(resp.Rows))
	}

	// kb-1 gets its stats.
	r1 := resp.Rows[0]
	if r1.FileCount != 10 || r1.TotalSizeBytes != 2048 || r1.FailedFileCount != 1 || r1.ProcessingFileCount != 2 {
		t.Errorf("kb-1 file stats wrong: %+v", r1)
	}
	if r1.MessageCount != 42 || r1.ChatCount != 7 {
		t.Errorf("kb-1 message stats wrong: %+v", r1)
	}

	// kb-2 has no entry in either stat map -> all zeros, nil timestamps.
	r2 := resp.Rows[1]
	if r2.FileCount != 0 || r2.MessageCount != 0 || r2.ChatCount != 0 {
		t.Errorf("kb-2 should be zeroed, got %+v", r2)
	}
	if r2.LastFileUploadAt != nil || r2.LastMessageAt != nil {
		t.Errorf("kb-2 timestamps should be nil, got %+v / %+v", r2.LastFileUploadAt, r2.LastMessageAt)
	}
	if r2.OwnerName != nil {
		t.Errorf("kb-2 owner should be nil, got %v", *r2.OwnerName)
	}
}

func TestOverview_QueueSummaryPassthrough(t *testing.T) {
	insp := &fakeInspector{info: map[string]*asynq.QueueInfo{
		jobs.QueueQuick: {Pending: 3, Active: 1, Archived: 0},
		jobs.QueueHeavy: {Pending: 0, Active: 2, Archived: 5},
		jobs.QueueBatch: {Pending: 0, Active: 0, Archived: 0},
	}}
	svc := NewService(&fakeStore{}, insp)

	resp, err := svc.Overview(context.Background())
	if err != nil {
		t.Fatalf("Overview returned error: %v", err)
	}
	q := resp.QueueSummary[jobs.QueueQuick]
	if q.Waiting != 3 || q.Active != 1 || q.Failed != 0 {
		t.Errorf("rag-quick summary wrong: %+v", q)
	}
	h := resp.QueueSummary[jobs.QueueHeavy]
	if h.Active != 2 || h.Failed != 5 {
		t.Errorf("rag-heavy summary wrong: %+v", h)
	}
}

func TestOverview_QueueInspectorErrorDegradesToZeros(t *testing.T) {
	svc := NewService(&fakeStore{}, &fakeInspector{err: errors.New("redis down")})
	resp, err := svc.Overview(context.Background())
	if err != nil {
		t.Fatalf("Overview should not fail on inspector error: %v", err)
	}
	if got := resp.QueueSummary[jobs.QueueQuick]; got != (QueueStats{}) {
		t.Errorf("expected zeroed queue stats on inspector error, got %+v", got)
	}
}

func TestOverview_StoreErrorPropagates(t *testing.T) {
	svc := NewService(&fakeStore{listErr: errors.New("db down")}, &fakeInspector{})
	if _, err := svc.Overview(context.Background()); err == nil {
		t.Fatal("expected error when ListKBs fails")
	}
}

func TestOverview_FileStatsErrorPropagates(t *testing.T) {
	svc := NewService(&fakeStore{fileErr: errors.New("file stats db down")}, &fakeInspector{})
	if _, err := svc.Overview(context.Background()); err == nil {
		t.Fatal("expected error when FileStatsByKB fails")
	}
}

func TestOverview_MessageStatsErrorPropagates(t *testing.T) {
	svc := NewService(&fakeStore{msgErr: errors.New("msg stats db down")}, &fakeInspector{})
	if _, err := svc.Overview(context.Background()); err == nil {
		t.Fatal("expected error when MessageStatsByKB fails")
	}
}

func TestOverview_SurfacesOwnerIdentity(t *testing.T) {
	store := &fakeStore{
		kbs: []KBBase{{
			ID:            "kb-1",
			Name:          "Alpha",
			OwnerName:     strptr("Ada Lovelace"),
			OwnerID:       strptr("user-1"),
			OwnerUsername: strptr("ada"),
			CreatedAt:     "2026-01-01T00:00:00Z",
		}},
		fileMap: map[string]FileStats{},
		msgMap:  map[string]MessageStats{},
	}
	svc := NewService(store, nil)

	resp, err := svc.Overview(context.Background())
	if err != nil {
		t.Fatalf("Overview: %v", err)
	}
	if len(resp.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(resp.Rows))
	}
	row := resp.Rows[0]
	if row.OwnerID == nil || *row.OwnerID != "user-1" {
		t.Errorf("OwnerID = %v, want user-1", row.OwnerID)
	}
	if row.OwnerUsername == nil || *row.OwnerUsername != "ada" {
		t.Errorf("OwnerUsername = %v, want ada", row.OwnerUsername)
	}
}
