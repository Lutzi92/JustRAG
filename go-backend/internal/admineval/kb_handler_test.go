package admineval

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/eval"
)

// --- fakes ---

type fakeRunStore struct {
	active   bool
	inserted *eval.Run
	got      *eval.Run
}

func (f *fakeRunStore) Insert(_ context.Context, r eval.Run) (uuid.UUID, error) {
	f.inserted = &r
	return uuid.New(), nil
}
func (f *fakeRunStore) Get(_ context.Context, _ uuid.UUID) (*eval.Run, error) { return f.got, nil }
func (f *fakeRunStore) List(_ context.Context, o eval.ListOpts) ([]eval.Run, int, error) {
	return []eval.Run{{KBID: *o.KBID}}, 1, nil
}
func (f *fakeRunStore) Delete(_ context.Context, _ uuid.UUID) (bool, bool, error) {
	return true, false, nil
}
func (f *fakeRunStore) MarkRunning(context.Context, uuid.UUID) error { return nil }
func (f *fakeRunStore) MarkCompleted(context.Context, uuid.UUID, json.RawMessage) error {
	return nil
}
func (f *fakeRunStore) MarkFailed(context.Context, uuid.UUID, string) error { return nil }
func (f *fakeRunStore) HasActiveRun(_ context.Context, _ uuid.UUID) (bool, error) {
	return f.active, nil
}

type fakeGSStore struct{ get *eval.GoldenSet }

func (f *fakeGSStore) Create(context.Context, eval.GoldenSet) (uuid.UUID, time.Time, error) {
	return uuid.New(), time.Time{}, nil
}
func (f *fakeGSStore) Get(_ context.Context, _ uuid.UUID) (*eval.GoldenSet, error) { return f.get, nil }
func (f *fakeGSStore) List(context.Context) ([]eval.GoldenSet, error)              { return nil, nil }
func (f *fakeGSStore) ListByKB(_ context.Context, kbID uuid.UUID) ([]eval.GoldenSet, error) {
	return []eval.GoldenSet{{KBID: kbID}}, nil
}
func (f *fakeGSStore) Delete(context.Context, uuid.UUID) (bool, error) { return true, nil }

type fakeKB struct{}

func (fakeKB) GetKBInfo(_ context.Context, id uuid.UUID) (string, bool, error) {
	return "kb-" + id.String(), true, nil
}

type fakeCfg struct{}

func (fakeCfg) GetSiteConfigValue(context.Context, string) (*string, error) { return nil, nil }

func req(method, target, id string, body string) *http.Request {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.SetPathValue("id", id)
	return r
}

// reqAuth injects a dummy user into the request context (for handlers that
// require auth.UserFromContext to return non-nil before checking guards).
func reqAuth(method, target, id string, body string) *http.Request {
	r := req(method, target, id, body)
	ctx := auth.WithUser(r.Context(), &auth.Claims{ID: uuid.New().String(), Role: "user"})
	return r.WithContext(ctx)
}

// CreateRunForKB rejects a second concurrent run for the KB.
func TestCreateRunForKB_InFlightGuard(t *testing.T) {
	kb := uuid.New()
	gs := uuid.New()
	h := NewHandler(&fakeRunStore{active: true}, fakeKB{}, fakeCfg{}, nil, &fakeGSStore{get: &eval.GoldenSet{ID: gs, KBID: kb}}, nil, nil)
	w := httptest.NewRecorder()
	h.CreateRunForKB(w, reqAuth(http.MethodPost, "/api/kb/"+kb.String()+"/eval/runs", kb.String(), `{"golden_set_id":"`+gs.String()+`"}`))
	if w.Code != http.StatusConflict {
		t.Fatalf("want 409 when a run is in flight, got %d", w.Code)
	}
}

// CreateRunForKB rejects a golden set belonging to a different KB.
func TestCreateRunForKB_CrossKBGoldenSetRejected(t *testing.T) {
	kb := uuid.New()
	otherKB := uuid.New()
	gs := uuid.New()
	rs := &fakeRunStore{}
	h := NewHandler(rs, fakeKB{}, fakeCfg{}, nil, &fakeGSStore{get: &eval.GoldenSet{ID: gs, KBID: otherKB}}, nil, nil)
	w := httptest.NewRecorder()
	h.CreateRunForKB(w, reqAuth(http.MethodPost, "/api/kb/"+kb.String()+"/eval/runs", kb.String(), `{"golden_set_id":"`+gs.String()+`"}`))
	if w.Code != http.StatusBadRequest && w.Code != http.StatusNotFound {
		t.Fatalf("want 400/404 for cross-KB golden set, got %d", w.Code)
	}
	if rs.inserted != nil {
		t.Fatal("must not insert a run for a cross-KB golden set")
	}
}

// GetRunForKB rejects a run from another KB.
func TestGetRunForKB_CrossKBRejected(t *testing.T) {
	kb := uuid.New()
	otherKB := uuid.New()
	run := &eval.Run{ID: uuid.New(), KBID: otherKB}
	h := NewHandler(&fakeRunStore{got: run}, fakeKB{}, fakeCfg{}, nil, &fakeGSStore{}, nil, nil)
	r := httptest.NewRequest(http.MethodGet, "/api/kb/"+kb.String()+"/eval/runs/"+run.ID.String(), nil)
	r.SetPathValue("id", kb.String())
	r.SetPathValue("runId", run.ID.String())
	w := httptest.NewRecorder()
	h.GetRunForKB(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("want 404 for a run owned by another KB, got %d", w.Code)
	}
}

// ListGoldenSetsForKB returns only the path KB's sets.
func TestListGoldenSetsForKB(t *testing.T) {
	kb := uuid.New()
	h := NewHandler(&fakeRunStore{}, fakeKB{}, fakeCfg{}, nil, &fakeGSStore{}, nil, nil)
	w := httptest.NewRecorder()
	h.ListGoldenSetsForKB(w, req(http.MethodGet, "/api/kb/"+kb.String()+"/eval/golden-sets", kb.String(), ""))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
}
