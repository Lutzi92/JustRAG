package rematerialize

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hibiken/asynq"

	"github.com/justrag/go-backend/internal/files"
	"github.com/justrag/go-backend/internal/jobs"
)

// fakeEnqueuer records every EnqueueContext call so tests can assert on the
// task type, queue and options actually passed, not just the call count.
type fakeEnqueuer struct {
	tasks []*asynq.Task
	opts  [][]asynq.Option
	err   error
}

func (f *fakeEnqueuer) EnqueueContext(_ context.Context, task *asynq.Task, opts ...asynq.Option) (*asynq.TaskInfo, error) {
	f.tasks = append(f.tasks, task)
	f.opts = append(f.opts, opts)
	return &asynq.TaskInfo{}, f.err
}

// fakeFileLister returns a scripted file list.
type fakeFileLister struct {
	files []*files.FileInfo
	err   error
}

func (f fakeFileLister) ListSpreadsheetFiles(_ context.Context, _ string) ([]*files.FileInfo, error) {
	return f.files, f.err
}

// mapReader is a scripted SiteConfigReader.
type mapReader map[string]string

func (m mapReader) GetSiteConfigValue(_ context.Context, k string) (*string, error) {
	if v, ok := m[k]; ok {
		return &v, nil
	}
	return nil, nil
}

func strPtr(s string) *string { return &s }

func postRematerialize(t *testing.T, h *Handler) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/kb/kb-1/tabular/rematerialize", nil)
	req.SetPathValue("id", "kb-1")
	rec := httptest.NewRecorder()
	h.PostRematerialize(rec, req)
	return rec
}

// optValue returns the Value() of the first option of the given Type in
// opts, and whether one was found.
func optValue(opts []asynq.Option, typ asynq.OptionType) (any, bool) {
	for _, o := range opts {
		if o.Type() == typ {
			return o.Value(), true
		}
	}
	return nil, false
}

func TestPostRematerializeEnqueuesSpreadsheetFiles(t *testing.T) {
	lister := fakeFileLister{files: []*files.FileInfo{
		{ID: "f1", KbID: "kb-1", Name: "budget.xlsx", Type: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", StoragePath: strPtr("u/k/budget.xlsx")},
		{ID: "f2", KbID: "kb-1", Name: "orphan.xlsx", Type: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", StoragePath: nil},
	}}
	enq := &fakeEnqueuer{}
	h := NewHandler(enq, lister, mapReader{"chat_tabular_query_enabled": "true"})

	rec := postRematerialize(t, h)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Status string `json:"status"`
		KbID   string `json:"kbId"`
		Files  int    `json:"files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Status != "queued" || body.KbID != "kb-1" || body.Files != 1 {
		t.Fatalf("want status=queued kbId=kb-1 files=1, got %+v", body)
	}

	if len(enq.tasks) != 1 {
		t.Fatalf("want exactly 1 enqueue call (nil-storage-path file skipped), got %d", len(enq.tasks))
	}
	task := enq.tasks[0]
	if task.Type() != jobs.TypeReEmbedding {
		t.Errorf("want task type %q, got %q", jobs.TypeReEmbedding, task.Type())
	}
	var payload jobs.FileProcessingPayload
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.FileID != "f1" || payload.KbID != "kb-1" || payload.FilePath != "u/k/budget.xlsx" {
		t.Fatalf("unexpected payload: %+v", payload)
	}

	opts := enq.opts[0]
	if v, ok := optValue(opts, asynq.QueueOpt); !ok || v != jobs.QueueBatch {
		t.Errorf("want Queue(%q), got %v (present=%v)", jobs.QueueBatch, v, ok)
	}
	if v, ok := optValue(opts, asynq.MaxRetryOpt); !ok || v != 1 {
		t.Errorf("want MaxRetry(1), got %v (present=%v)", v, ok)
	}
	wantTimeout := jobs.TimeoutFor(jobs.TypeReEmbedding)
	if v, ok := optValue(opts, asynq.TimeoutOpt); !ok || v != wantTimeout {
		t.Errorf("want Timeout(%v), got %v (present=%v)", wantTimeout, v, ok)
	}
}

func TestPostRematerializeRequiresFlag(t *testing.T) {
	lister := fakeFileLister{files: []*files.FileInfo{
		{ID: "f1", KbID: "kb-1", Name: "budget.xlsx", Type: "text/csv", StoragePath: strPtr("u/k/budget.xlsx")},
	}}
	enq := &fakeEnqueuer{}
	h := NewHandler(enq, lister, mapReader{})

	rec := postRematerialize(t, h)

	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Error != "chat_tabular_query_enabled is off" {
		t.Fatalf("unexpected error message: %q", body.Error)
	}
	if len(enq.tasks) != 0 {
		t.Fatalf("gate off must not enqueue, got %d calls", len(enq.tasks))
	}
}
