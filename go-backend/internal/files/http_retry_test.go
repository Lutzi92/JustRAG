package files_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hibiken/asynq"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/files"
	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/kbaccess"
)

// recordingEnqueuer captures enqueued tasks.
type recordingEnqueuer struct{ tasks []*asynq.Task }

func (m *recordingEnqueuer) Enqueue(task *asynq.Task, _ ...asynq.Option) (*asynq.TaskInfo, error) {
	m.tasks = append(m.tasks, task)
	return &asynq.TaskInfo{}, nil
}

func retryFixtureStore() *mockStore {
	return &mockStore{
		file: &files.FileInfo{
			ID: "file-1", KbID: "kb-1", Name: "doc.pdf",
			Type: "application/pdf", StoragePath: strPtr("alice/kb-1/doc.pdf"),
		},
		kb:      &kbaccess.KnowledgeBase{ID: "kb-1", UserID: strPtr("user-1")},
		resetOK: true,
	}
}

func doRetry(h *files.Handler) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/files/file-1/retry", nil)
	r.SetPathValue("id", "file-1")
	ctx := auth.WithUser(r.Context(), &auth.Claims{ID: "user-1", Username: "alice", Role: "user"})
	r = r.WithContext(ctx)
	w := httptest.NewRecorder()
	h.Retry(w, r)
	return w
}

func TestRetrySuccess(t *testing.T) {
	store := retryFixtureStore()
	enq := &recordingEnqueuer{}
	h := files.NewHandlerWithEnqueuer(store, &mockStorage{}, &mockChunkDeleter{}, enq)

	w := doRetry(h)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if len(enq.tasks) != 1 || enq.tasks[0].Type() != jobs.TypeReEmbedding {
		t.Fatalf("want one re-embedding task, got %+v", enq.tasks)
	}
	var p jobs.FileProcessingPayload
	if err := json.Unmarshal(enq.tasks[0].Payload(), &p); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if p.FileID != "file-1" || p.KbID != "kb-1" || p.FilePath != "alice/kb-1/doc.pdf" ||
		p.OriginalName != "doc.pdf" || p.MimeType != "application/pdf" {
		t.Fatalf("payload fields: %+v", p)
	}
	if !store.resetCalled {
		t.Fatal("ResetFileForRetry was not called")
	}
}

func TestRetryNotInErrorState(t *testing.T) {
	store := retryFixtureStore()
	store.resetOK = false // conditional UPDATE matched 0 rows
	enq := &recordingEnqueuer{}
	h := files.NewHandlerWithEnqueuer(store, &mockStorage{}, &mockChunkDeleter{}, enq)

	w := doRetry(h)

	if w.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d", w.Code)
	}
	if len(enq.tasks) != 0 {
		t.Fatalf("nothing must be enqueued on 409, got %d tasks", len(enq.tasks))
	}
}

func TestRetryForbiddenWithoutEdit(t *testing.T) {
	store := retryFixtureStore()
	store.kb = &kbaccess.KnowledgeBase{ID: "kb-1", UserID: strPtr("someone-else")}
	store.share = nil // no share → no edit access
	h := files.NewHandlerWithEnqueuer(store, &mockStorage{}, &mockChunkDeleter{}, &recordingEnqueuer{})

	w := doRetry(h)

	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", w.Code)
	}
}

func TestRetryNoStoragePath(t *testing.T) {
	store := retryFixtureStore()
	store.file.StoragePath = nil
	h := files.NewHandlerWithEnqueuer(store, &mockStorage{}, &mockChunkDeleter{}, &recordingEnqueuer{})

	w := doRetry(h)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d", w.Code)
	}
}

func TestRetryEnqueueFailureRevertsToError(t *testing.T) {
	store := retryFixtureStore()
	h := files.NewHandlerWithEnqueuer(store, &mockStorage{}, &mockChunkDeleter{}, &failingEnqueuer{})

	w := doRetry(h)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", w.Code)
	}
	if store.markedStage != "queue" {
		t.Fatalf("file must be reverted to error with stage=queue, got %q", store.markedStage)
	}
}

func TestRetryFailedBulk(t *testing.T) {
	store := retryFixtureStore()
	store.errorFiles = []*files.FileInfo{
		{ID: "f-a", KbID: "kb-1", Name: "a.pdf", Type: "application/pdf", StoragePath: strPtr("p/a.pdf")},
		{ID: "f-b", KbID: "kb-1", Name: "b.pdf", Type: "application/pdf", StoragePath: nil}, // skipped
		{ID: "f-c", KbID: "kb-1", Name: "c.pdf", Type: "application/pdf", StoragePath: strPtr("p/c.pdf")},
	}
	enq := &recordingEnqueuer{}
	h := files.NewHandlerWithEnqueuer(store, &mockStorage{}, &mockChunkDeleter{}, enq)

	r := httptest.NewRequest(http.MethodPost, "/api/kb/kb-1/files/retry-failed", nil)
	r.SetPathValue("id", "kb-1")
	// kbEditChain runs before the handler in production; simulate its context.
	access := &kbaccess.KBAccessResult{KB: &kbaccess.KnowledgeBase{ID: "kb-1"}, IsOwner: true, Permission: "edit"}
	r = r.WithContext(kbaccess.WithAccess(auth.WithUser(r.Context(), &auth.Claims{ID: "user-1", Role: "user"}), access))
	w := httptest.NewRecorder()
	h.RetryFailed(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]int
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["retried"] != 2 {
		t.Fatalf("want retried=2 (f-b has no storage path), got %d", resp["retried"])
	}
	if len(enq.tasks) != 2 {
		t.Fatalf("want 2 enqueued tasks, got %d", len(enq.tasks))
	}
	if store.resetCount != 2 {
		t.Fatalf("want ResetFileForRetry called 2 times (f-b skipped before reset), got %d", store.resetCount)
	}
}
