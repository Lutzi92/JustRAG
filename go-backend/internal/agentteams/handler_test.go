package agentteams

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/justrag/go-backend/internal/auth"
)

// fakeStore implements handlerStore in memory.
type fakeStore struct {
	agents map[string]AgentRecord
	teams  map[string]TeamRecord
}

func newFakeStore() *fakeStore {
	return &fakeStore{agents: map[string]AgentRecord{}, teams: map[string]TeamRecord{}}
}

func (f *fakeStore) CreateAgent(_ context.Context, a AgentRecord) (*AgentRecord, error) {
	a.ID = "agent-" + a.Name
	f.agents[a.ID] = a
	return &a, nil
}
func (f *fakeStore) ListAgentsByUser(_ context.Context, userID string) ([]AgentRecord, error) {
	var out []AgentRecord
	for _, a := range f.agents {
		if a.UserID == userID {
			out = append(out, a)
		}
	}
	return out, nil
}
func (f *fakeStore) GetAgent(_ context.Context, id, userID string) (*AgentRecord, error) {
	a, ok := f.agents[id]
	if !ok || a.UserID != userID {
		return nil, ErrNotFound
	}
	return &a, nil
}
func (f *fakeStore) UpdateAgent(_ context.Context, a AgentRecord) (*AgentRecord, error) {
	old, ok := f.agents[a.ID]
	if !ok || old.UserID != a.UserID {
		return nil, ErrNotFound
	}
	f.agents[a.ID] = a
	return &a, nil
}
func (f *fakeStore) DeleteAgent(_ context.Context, id, userID string) (bool, error) {
	a, ok := f.agents[id]
	if !ok || a.UserID != userID {
		return false, nil
	}
	delete(f.agents, id)
	return true, nil
}
func (f *fakeStore) CountOwnedAgents(_ context.Context, userID string, ids []string) (int, error) {
	n := 0
	for _, id := range ids {
		if a, ok := f.agents[id]; ok && a.UserID == userID {
			n++
		}
	}
	return n, nil
}
func (f *fakeStore) CreateTeam(_ context.Context, t TeamRecord) (*TeamRecord, error) {
	t.ID = "team-" + t.Name
	f.teams[t.ID] = t
	return &t, nil
}
func (f *fakeStore) ListTeamsByUser(_ context.Context, userID string) ([]TeamRecord, error) {
	var out []TeamRecord
	for _, t := range f.teams {
		if t.UserID == userID {
			out = append(out, t)
		}
	}
	return out, nil
}
func (f *fakeStore) GetTeam(_ context.Context, id, userID string) (*TeamRecord, error) {
	t, ok := f.teams[id]
	if !ok || t.UserID != userID {
		return nil, ErrNotFound
	}
	return &t, nil
}
func (f *fakeStore) UpdateTeam(_ context.Context, t TeamRecord) (*TeamRecord, error) {
	old, ok := f.teams[t.ID]
	if !ok || old.UserID != t.UserID {
		return nil, ErrNotFound
	}
	f.teams[t.ID] = t
	return &t, nil
}
func (f *fakeStore) DeleteTeam(_ context.Context, id, userID string) (bool, error) {
	t, ok := f.teams[id]
	if !ok || t.UserID != userID {
		return false, nil
	}
	delete(f.teams, id)
	return true, nil
}
func (f *fakeStore) AttachAgent(_ context.Context, kbID, agentID string, isDefault bool) error {
	return nil
}
func (f *fakeStore) DetachAgent(_ context.Context, kbID, agentID string) (bool, error) {
	return true, nil
}
func (f *fakeStore) AttachTeam(_ context.Context, kbID, teamID string, isDefault bool) error {
	return nil
}
func (f *fakeStore) DetachTeam(_ context.Context, kbID, teamID string) (bool, error) {
	return true, nil
}
func (f *fakeStore) ListAttachedForKB(_ context.Context, kbID string) (*KBAgents, error) {
	return &KBAgents{Agents: []AttachedAgent{}, Teams: []AttachedTeam{}}, nil
}

func testHandler(fs *fakeStore) *Handler {
	return NewHandler(fs, HandlerDeps{
		AvailableTools: func(context.Context) (map[string]bool, error) {
			return map[string]bool{"kb_search": true, "calculator": true}, nil
		},
		ModelExists: func(_ context.Context, name string) (bool, error) {
			return name == "gemma-4-26b", nil
		},
	})
}

func doJSON(t *testing.T, h http.HandlerFunc, method, target, userID string, body any, pathVals map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, target, &buf)
	r = r.WithContext(auth.WithUser(r.Context(), &auth.Claims{ID: userID}))
	for k, v := range pathVals {
		r.SetPathValue(k, v)
	}
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

func TestCreateAgentValidatesToolAllowlist(t *testing.T) {
	fs := newFakeStore()
	h := testHandler(fs)
	w := doJSON(t, h.CreateAgent, "POST", "/api/agents", "u1", map[string]any{
		"name": "Sec", "toolNames": []string{"code_exec"},
	}, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("privileged tool must be rejected, got %d: %s", w.Code, w.Body.String())
	}
	if len(fs.agents) != 0 {
		t.Fatalf("rejected agent must not reach the store, got %d agents", len(fs.agents))
	}
	w = doJSON(t, h.CreateAgent, "POST", "/api/agents", "u1", map[string]any{
		"name": "Sec", "toolNames": []string{"kb_search"},
	}, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("valid agent rejected, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateAgentValidatesConfigAndModel(t *testing.T) {
	h := testHandler(newFakeStore())
	w := doJSON(t, h.CreateAgent, "POST", "/api/agents", "u1", map[string]any{
		"name": "A", "config": map[string]string{"raptor_enabled": "true"},
	}, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("reingest config key must be rejected, got %d", w.Code)
	}
	w = doJSON(t, h.CreateAgent, "POST", "/api/agents", "u1", map[string]any{
		"name": "A", "chatModel": "not-a-model",
	}, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unknown model must be rejected, got %d", w.Code)
	}
}

func TestAgentOwnerScoping(t *testing.T) {
	fs := newFakeStore()
	h := testHandler(fs)
	doJSON(t, h.CreateAgent, "POST", "/api/agents", "u1", map[string]any{"name": "Mine"}, nil)
	w := doJSON(t, h.GetAgent, "GET", "/api/agents/agent-Mine", "u2", nil,
		map[string]string{"id": "agent-Mine"})
	if w.Code != http.StatusNotFound {
		t.Fatalf("foreign agent must 404, got %d", w.Code)
	}
}

func TestCreateTeamRejectsForeignMembers(t *testing.T) {
	fs := newFakeStore()
	h := testHandler(fs)
	doJSON(t, h.CreateAgent, "POST", "/api/agents", "u1", map[string]any{"name": "A1"}, nil)
	w := doJSON(t, h.CreateTeam, "POST", "/api/agent-teams", "u2", map[string]any{
		"name": "T", "memberIds": []string{"agent-A1"},
	}, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("foreign member must be rejected, got %d: %s", w.Code, w.Body.String())
	}
	if len(fs.teams) != 0 {
		t.Fatalf("rejected team must not reach the store, got %d teams", len(fs.teams))
	}
}
