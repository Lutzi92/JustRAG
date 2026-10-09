package confluence_test

import (
	"context"
	"errors"
	"testing"

	"github.com/hibiken/asynq"

	"github.com/justrag/go-backend/internal/confluence"
	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/syncwindow"
)

type recEnqueuer struct {
	tasks []*asynq.Task
	err   error
}

func (e *recEnqueuer) Enqueue(t *asynq.Task, _ ...asynq.Option) (*asynq.TaskInfo, error) {
	e.tasks = append(e.tasks, t)
	if e.err != nil {
		return nil, e.err
	}
	return &asynq.TaskInfo{}, nil
}

func TestImport_NoConnection(t *testing.T) {
	store := &mockStore{}
	enq := &recEnqueuer{}
	_, err := confluence.NewImporter(store, enq).Import(context.Background(), testUserID, testKBID, "ENG", nil)
	if !errors.Is(err, confluence.ErrNoConnection) {
		t.Fatalf("err = %v, want ErrNoConnection", err)
	}
	if len(store.createSourceCalls) != 0 || len(enq.tasks) != 0 {
		t.Fatalf("side effects: creates=%d tasks=%d", len(store.createSourceCalls), len(enq.tasks))
	}
}

func TestImport_ConnectionLookupError(t *testing.T) {
	store := &mockStore{connErr: errors.New("db down")}
	_, err := confluence.NewImporter(store, &recEnqueuer{}).Import(context.Background(), testUserID, testKBID, "ENG", nil)
	if err == nil || errors.Is(err, confluence.ErrNoConnection) {
		t.Fatalf("err = %v, want a lookup error", err)
	}
	if len(store.createSourceCalls) != 0 {
		t.Fatal("source must not be created")
	}
}

func TestImport_EmptySpaceKey(t *testing.T) {
	store := &mockStore{conn: &confluence.ConfluenceConnectionRow{ID: testConnID}}
	if _, err := confluence.NewImporter(store, &recEnqueuer{}).Import(context.Background(), testUserID, testKBID, "", nil); err == nil {
		t.Fatal("want error for empty spaceKey")
	}
	if len(store.createSourceCalls) != 0 {
		t.Fatal("source must not be created")
	}
}

func TestImport_CreatesManualSourceAndEnqueues(t *testing.T) {
	store := &mockStore{conn: &confluence.ConfluenceConnectionRow{ID: testConnID, UserID: testUserID}, source: makeSource()}
	enq := &recEnqueuer{}
	root := "42"
	id, err := confluence.NewImporter(store, enq).Import(context.Background(), testUserID, testKBID, "ENG", &root)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if id != testSourceID {
		t.Fatalf("id = %q", id)
	}
	if len(store.createSourceCalls) != 1 {
		t.Fatalf("creates = %d", len(store.createSourceCalls))
	}
	c := store.createSourceCalls[0]
	if c.kbID != testKBID || c.connectionID != testConnID || c.spaceKey != "ENG" ||
		c.rootPageID == nil || *c.rootPageID != "42" || c.rootPageTitle != nil ||
		c.includeAttachments || c.syncSchedule != syncwindow.ScheduleManual {
		t.Fatalf("create args = %+v", c)
	}
	if len(enq.tasks) != 1 || enq.tasks[0].Type() != jobs.TypeConfluenceSync ||
		string(enq.tasks[0].Payload()) != `{"sourceId":"`+testSourceID+`"}` {
		t.Fatalf("tasks = %+v", enq.tasks)
	}
}

func TestImport_StoreErrorNoEnqueue(t *testing.T) {
	store := &mockStore{conn: &confluence.ConfluenceConnectionRow{ID: testConnID}, sourceErr: errors.New("boom")}
	enq := &recEnqueuer{}
	if _, err := confluence.NewImporter(store, enq).Import(context.Background(), testUserID, testKBID, "ENG", nil); err == nil {
		t.Fatal("want error")
	}
	if len(enq.tasks) != 0 {
		t.Fatal("must not enqueue")
	}
}

// A nil *asynq.Client must behave like "no queue" (as Handler does), not
// panic on Enqueue.
func TestImport_NilAsynqClientSkipsEnqueue(t *testing.T) {
	store := &mockStore{conn: &confluence.ConfluenceConnectionRow{ID: testConnID}, source: makeSource()}
	var client *asynq.Client
	id, err := confluence.NewImporter(store, client).Import(context.Background(), testUserID, testKBID, "ENG", nil)
	if !errors.Is(err, confluence.ErrSyncNotQueued) || id != testSourceID {
		t.Fatalf("id=%q err=%v, want id + ErrSyncNotQueued", id, err)
	}
	// Same for an untyped nil enqueuer.
	id, err = confluence.NewImporter(store, nil).Import(context.Background(), testUserID, testKBID, "ENG", nil)
	if !errors.Is(err, confluence.ErrSyncNotQueued) || id != testSourceID {
		t.Fatalf("nil enqueuer: id=%q err=%v", id, err)
	}
}

func TestImport_EnqueueErrorReturnsIDAndErrSyncNotQueued(t *testing.T) {
	store := &mockStore{conn: &confluence.ConfluenceConnectionRow{ID: testConnID}, source: makeSource()}
	enq := &recEnqueuer{err: errors.New("redis down")}
	id, err := confluence.NewImporter(store, enq).Import(context.Background(), testUserID, testKBID, "ENG", nil)
	if !errors.Is(err, confluence.ErrSyncNotQueued) || id != testSourceID {
		t.Fatalf("id=%q err=%v, want id + ErrSyncNotQueued", id, err)
	}
	if len(store.createSourceCalls) != 1 || len(enq.tasks) != 1 {
		t.Fatalf("creates=%d enqueue attempts=%d", len(store.createSourceCalls), len(enq.tasks))
	}
}

func TestImport_EmptyUserID(t *testing.T) {
	store := &mockStore{conn: &confluence.ConfluenceConnectionRow{ID: testConnID}, source: makeSource()}
	_, err := confluence.NewImporter(store, &recEnqueuer{}).Import(context.Background(), "", testKBID, "ENG", nil)
	if !errors.Is(err, confluence.ErrInvalidUser) {
		t.Fatalf("err = %v, want ErrInvalidUser", err)
	}
	if len(store.createSourceCalls) != 0 {
		t.Fatal("source must not be created")
	}
}
