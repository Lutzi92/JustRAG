package confluence_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/hibiken/asynq"

	"github.com/justrag/go-backend/internal/confluence"
	"github.com/justrag/go-backend/internal/jobs"
)

// Pinning tests for POST /api/kb/{id}/confluence-sources: they fix the
// create + first-sync-enqueue behaviour and the error responses so the
// extraction into Importer stays behaviour-preserving.

// newMiniredisAsynq returns a real asynq client and an inspector over an
// in-memory Redis, so the enqueued task can be read back verbatim.
func newMiniredisAsynq(t *testing.T) (*asynq.Client, *asynq.Inspector) {
	t.Helper()
	mr := miniredis.RunT(t)
	opt := asynq.RedisClientOpt{Addr: mr.Addr()}
	client := asynq.NewClient(opt)
	insp := asynq.NewInspector(opt)
	t.Cleanup(func() { _ = client.Close(); _ = insp.Close() })
	return client, insp
}

func TestCreateSource_Pin_CreatesAndEnqueuesFirstSync(t *testing.T) {
	client, insp := newMiniredisAsynq(t)
	store := &mockStore{conn: &confluence.ConfluenceConnectionRow{ID: testConnID, UserID: testUserID}, source: makeSource()}
	h := confluence.NewHandler(store, testJWTSecret, client)

	body := map[string]any{
		"connectionId":       testConnID,
		"spaceKey":           "ENG",
		"rootPageId":         "123",
		"rootPageTitle":      "Root",
		"includeAttachments": true,
		"syncSchedule":       "weekly",
	}
	req := withUser(withKBAccess(newRequest(http.MethodPost, "/api/kb/"+testKBID+"/confluence-sources", body), testKBID), testUserID)
	rr := httptest.NewRecorder()
	h.CreateSource(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	want, _ := json.Marshal(makeSource())
	if strings.TrimSpace(rr.Body.String()) != string(want) {
		t.Fatalf("body = %s, want %s", rr.Body.String(), want)
	}
	if len(store.createSourceCalls) != 1 {
		t.Fatalf("create calls = %d", len(store.createSourceCalls))
	}
	c := store.createSourceCalls[0]
	if c.kbID != testKBID || c.connectionID != testConnID || c.spaceKey != "ENG" ||
		c.rootPageID == nil || *c.rootPageID != "123" || c.rootPageTitle == nil || *c.rootPageTitle != "Root" ||
		!c.includeAttachments || c.syncSchedule != "weekly" {
		t.Fatalf("create args = %+v", c)
	}

	tasks, err := insp.ListPendingTasks(jobs.QueueHeavy)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("pending tasks = %d, want 1", len(tasks))
	}
	task := tasks[0]
	if task.Type != jobs.TypeConfluenceSync || task.MaxRetry != 3 || task.Timeout != jobs.TimeoutFor(jobs.TypeConfluenceSync) {
		t.Fatalf("task = type %q maxRetry %d timeout %v", task.Type, task.MaxRetry, task.Timeout)
	}
	if string(task.Payload) != `{"sourceId":"`+testSourceID+`"}` {
		t.Fatalf("payload = %s", task.Payload)
	}
}

func TestCreateSource_Pin_ForeignConnectionIs403(t *testing.T) {
	for name, store := range map[string]*mockStore{
		"no connection":    {},
		"other connection": {conn: &confluence.ConfluenceConnectionRow{ID: "someone-elses"}},
		"lookup error":     {connErr: errors.New("db down")},
	} {
		t.Run(name, func(t *testing.T) {
			h := confluence.NewHandler(store, testJWTSecret)
			body := map[string]any{"connectionId": testConnID, "spaceKey": "ENG"}
			req := withUser(withKBAccess(newRequest(http.MethodPost, "/api/kb/"+testKBID+"/confluence-sources", body), testKBID), testUserID)
			rr := httptest.NewRecorder()
			h.CreateSource(rr, req)
			if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "connection does not belong to you") {
				t.Fatalf("got %d %s", rr.Code, rr.Body.String())
			}
			if len(store.createSourceCalls) != 0 {
				t.Fatal("source must not be created")
			}
		})
	}
}

func TestCreateSource_Pin_StoreErrorIs500(t *testing.T) {
	client, insp := newMiniredisAsynq(t)
	store := &mockStore{conn: &confluence.ConfluenceConnectionRow{ID: testConnID}, sourceErr: errors.New("boom")}
	h := confluence.NewHandler(store, testJWTSecret, client)
	body := map[string]any{"connectionId": testConnID, "spaceKey": "ENG"}
	req := withUser(withKBAccess(newRequest(http.MethodPost, "/api/kb/"+testKBID+"/confluence-sources", body), testKBID), testUserID)
	rr := httptest.NewRecorder()
	h.CreateSource(rr, req)
	if rr.Code != http.StatusInternalServerError || !strings.Contains(rr.Body.String(), "failed to create Confluence source") {
		t.Fatalf("got %d %s", rr.Code, rr.Body.String())
	}
	if tasks, _ := insp.ListPendingTasks(jobs.QueueHeavy); len(tasks) != 0 {
		t.Fatalf("enqueued %d tasks on a failed create", len(tasks))
	}
}

func TestCreateSource_Pin_EnqueueFailureStill201(t *testing.T) {
	client, _ := newMiniredisAsynq(t)
	_ = client.Close() // every Enqueue now fails
	store := &mockStore{conn: &confluence.ConfluenceConnectionRow{ID: testConnID}, source: makeSource()}
	h := confluence.NewHandler(store, testJWTSecret, client)
	body := map[string]any{"connectionId": testConnID, "spaceKey": "ENG"}
	req := withUser(withKBAccess(newRequest(http.MethodPost, "/api/kb/"+testKBID+"/confluence-sources", body), testKBID), testUserID)
	rr := httptest.NewRecorder()
	h.CreateSource(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("got %d %s", rr.Code, rr.Body.String())
	}
}
