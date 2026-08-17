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
	agents         map[string]AgentRecord
	teams          map[string]TeamRecord
	attachedAgents map[string]bool // "kbID/agentID" -> isDefault
	attachedTeams  map[string]bool // "kbID/teamID"  -> isDefault
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		agents: map[string]AgentRecord{}, teams: map[string]TeamRecord{},
		attachedAgents: map[string]bool{}, attachedTeams: map[string]bool{},
	}
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

// The attach rule in one place: exists at all, and (owned by the caller OR
// already linked to this KB). Mirrors the two EXISTS arms of the SQL.
func (f *fakeStore) AgentAttachEligibility(_ context.Context, agentID, kbID, userID string) (AttachEligibility, error) {
	a, ok := f.agents[agentID]
	if !ok {
		return AttachEligibility{}, nil
	}
	_, attached := f.attachedAgents[kbID+"/"+agentID]
	return AttachEligibility{Exists: true, Allowed: a.UserID == userID || attached}, nil
}
func (f *fakeStore) TeamAttachEligibility(_ context.Context, teamID, kbID, userID string) (AttachEligibility, error) {
	tm, ok := f.teams[teamID]
	if !ok {
		return AttachEligibility{}, nil
	}
	_, attached := f.attachedTeams[kbID+"/"+teamID]
	return AttachEligibility{Exists: true, Allowed: tm.UserID == userID || attached}, nil
}

// attachedAgents / attachedTeams record what actually reached the store, keyed
// "kbID/entityID" -> isDefault. The attach tests assert on these rather than on
// the 200, because "returned 200 without writing anything" is exactly the shape
// a vacuous permission test would still pass.
func (f *fakeStore) AttachAgent(_ context.Context, kbID, agentID string, isDefault bool) error {
	f.attachedAgents[kbID+"/"+agentID] = isDefault
	return nil
}
func (f *fakeStore) DetachAgent(_ context.Context, kbID, agentID string) (bool, error) {
	delete(f.attachedAgents, kbID+"/"+agentID)
	return true, nil
}
func (f *fakeStore) AttachTeam(_ context.Context, kbID, teamID string, isDefault bool) error {
	f.attachedTeams[kbID+"/"+teamID] = isDefault
	return nil
}
func (f *fakeStore) DetachTeam(_ context.Context, kbID, teamID string) (bool, error) {
	delete(f.attachedTeams, kbID+"/"+teamID)
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

// ---------------------------------------------------------------------------
// KB attach/detach: route-gated, and gated on the agent's relationship to THIS
// KB — never on who created it alone.
//
// The routes sit on kbAdminChain (internal/app/routes.go), so by the time these
// handlers run, kbaccess has already decided the caller is a KB admin. They
// used to ALSO re-check that the caller OWNS the agent, which meant a KB admin
// who did not create it got 404 on their own KB — including when clearing a
// default someone else had bound, i.e. a binding the workflow canvas showed
// them and they could not remove.
//
// These tests state the rule in words on purpose, because it is a permission
// boundary and the exact width is the point:
//
//	a KB admin may attach, re-point or clear an agent/team on their KB when
//	they OWN it, OR when it is ALREADY attached to that KB — and not otherwise.
//
// The second arm is what makes the workflow canvas usable (a co-admin attaches
// it, another admin binds it). The first arm's ABSENCE is what would be
// dangerous: the link row is the use-time authorization — LoadAgentForChat
// checks attachment and never ownership — so binding an agent makes its
// persona, model override and config overrides run for every viewer of the KB,
// with the owner given no notice and no veto. An earlier revision dropped the
// ownership check entirely and let any KB admin conscript any agent whose UUID
// they could read; that is what these tests now forbid.
//
// Nothing usable is lost: GET /api/agents and GET /api/agent-teams stay
// owner-scoped, so an admin cannot even SEE an unattached foreign agent in
// order to name one.
// ---------------------------------------------------------------------------

// seedForeign creates an agent and a team owned by "owner", and returns the
// store — the caller in every test below is "kb-admin", a different user.
func seedForeign(t *testing.T) (*fakeStore, *Handler) {
	t.Helper()
	fs := newFakeStore()
	h := testHandler(fs)
	doJSON(t, h.CreateAgent, "POST", "/api/agents", "owner", map[string]any{"name": "Fremd"}, nil)
	doJSON(t, h.CreateTeam, "POST", "/api/agent-teams", "owner", map[string]any{"name": "FremdT"}, nil)
	if _, ok := fs.agents["agent-Fremd"]; !ok {
		t.Fatal("fixture: agent was not created")
	}
	if _, ok := fs.teams["team-FremdT"]; !ok {
		t.Fatal("fixture: team was not created")
	}
	return fs, h
}

// Arm one: the agent is foreign but ALREADY attached to this KB. This is the
// case the workflow canvas is built on — someone else attached it, and any KB
// admin can now make it the default or take it back off.
func TestAttachAgentAllowsAKbAdminToBindAnAlreadyAttachedForeignAgent(t *testing.T) {
	fs, h := seedForeign(t)
	// Attached, but not the default. The link is what authorizes the caller.
	fs.attachedAgents["kb1/agent-Fremd"] = false

	w := doJSON(t, h.AttachAgent, "PUT", "/api/kb/kb1/agents/agent-Fremd", "kb-admin",
		map[string]any{"isDefault": true},
		map[string]string{"id": "kb1", "agentId": "agent-Fremd"})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — an agent already attached to this KB must be "+
			"bindable by any KB admin, whoever created it: %s", w.Code, w.Body.String())
	}
	if got, ok := fs.attachedAgents["kb1/agent-Fremd"]; !ok || !got {
		t.Fatalf("attachedAgents = %v, want the link written with isDefault=true — "+
			"a 200 that writes nothing would be the same bug with a nicer status", fs.attachedAgents)
	}
}

// Arm two: the caller OWNS the agent, and it is not attached to this KB yet.
// This is how an agent gets attached in the first place, and it must keep
// working — a rule that only allowed already-attached agents would make the
// attach endpoint unable to attach anything.
func TestAttachAgentAllowsTheOwnerToAttachTheirOwnAgent(t *testing.T) {
	fs := newFakeStore()
	h := testHandler(fs)
	doJSON(t, h.CreateAgent, "POST", "/api/agents", "kb-admin", map[string]any{"name": "Eigen"}, nil)

	w := doJSON(t, h.AttachAgent, "PUT", "/api/kb/kb1/agents/agent-Eigen", "kb-admin",
		map[string]any{"isDefault": true},
		map[string]string{"id": "kb1", "agentId": "agent-Eigen"})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — the owner attaching their own agent is the "+
			"ordinary path: %s", w.Code, w.Body.String())
	}
	if got, ok := fs.attachedAgents["kb1/agent-Eigen"]; !ok || !got {
		t.Fatalf("attachedAgents = %v, want the link written", fs.attachedAgents)
	}
}

// The boundary itself, in both kinds: a real agent/team that the caller neither
// owns nor has attached to this KB is REFUSED, even though the caller is a KB
// admin. 403, not 404 — the row exists and saying so is not a leak the listing
// endpoints do not already prevent (they are owner-scoped, so an admin cannot
// discover a foreign id through the product at all).
//
// Without this, a KB admin could make ANY agent whose UUID they could read
// executable by every viewer of their KB — running that agent's persona, model
// override and config overrides — because the link row, not ownership, is what
// LoadAgentForChat authorizes on.
func TestAttachRefusesAForeignAgentThatIsNotAttachedToThisKB(t *testing.T) {
	fs, h := seedForeign(t)

	w := doJSON(t, h.AttachAgent, "PUT", "/api/kb/kb1/agents/agent-Fremd", "kb-admin",
		map[string]any{"isDefault": true},
		map[string]string{"id": "kb1", "agentId": "agent-Fremd"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 — a KB admin may not conscript an agent that is "+
			"neither theirs nor already attached here: %s", w.Code, w.Body.String())
	}
	if len(fs.attachedAgents) != 0 {
		t.Fatalf("attachedAgents = %v, want nothing written — a refusal that still "+
			"writes the link is the whole vulnerability with a nicer status",
			fs.attachedAgents)
	}

	w = doJSON(t, h.AttachTeam, "PUT", "/api/kb/kb1/teams/team-FremdT", "kb-admin",
		map[string]any{"isDefault": true},
		map[string]string{"id": "kb1", "teamId": "team-FremdT"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a foreign unattached team: %s",
			w.Code, w.Body.String())
	}
	if len(fs.attachedTeams) != 0 {
		t.Fatalf("attachedTeams = %v, want nothing written", fs.attachedTeams)
	}
}

// The clearing half, which is what the workflow canvas needs: an admin who does
// not own the bound agent must be able to take it back off their KB. Clearing
// goes through the SAME attach endpoint with isDefault:false (there is no
// "unset the default" endpoint), so the old owner check blocked removal too.
func TestAttachAgentClearsADefaultTheCallerDoesNotOwn(t *testing.T) {
	fs, h := seedForeign(t)
	fs.attachedAgents["kb1/agent-Fremd"] = true

	w := doJSON(t, h.AttachAgent, "PUT", "/api/kb/kb1/agents/agent-Fremd", "kb-admin",
		map[string]any{"isDefault": false},
		map[string]string{"id": "kb1", "agentId": "agent-Fremd"})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a KB admin must be able to CLEAR a default "+
			"bound by someone else: %s", w.Code, w.Body.String())
	}
	if got := fs.attachedAgents["kb1/agent-Fremd"]; got {
		t.Fatal("the link is still flagged default — clearing did not reach the store")
	}
}

func TestAttachTeamAllowsAKbAdminToBindAnAlreadyAttachedForeignTeam(t *testing.T) {
	fs, h := seedForeign(t)
	fs.attachedTeams["kb1/team-FremdT"] = false

	w := doJSON(t, h.AttachTeam, "PUT", "/api/kb/kb1/teams/team-FremdT", "kb-admin",
		map[string]any{"isDefault": true},
		map[string]string{"id": "kb1", "teamId": "team-FremdT"})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — same rule as agents: %s", w.Code, w.Body.String())
	}
	if got, ok := fs.attachedTeams["kb1/team-FremdT"]; !ok || !got {
		t.Fatalf("attachedTeams = %v, want the link written with isDefault=true", fs.attachedTeams)
	}
}

// The existence check, and its PRECEDENCE over the permission check. A 404 for
// an agent that does not exist is a correct answer and must stay: without it a
// typo'd id reaches the FK and comes back as a 500, and the write must not
// happen either way. It has to come first, too — answering 403 for a
// nonexistent id would tell an admin to go ask a nonexistent owner.
func TestAttachRejectsAnAgentThatDoesNotExist(t *testing.T) {
	fs, h := seedForeign(t)

	w := doJSON(t, h.AttachAgent, "PUT", "/api/kb/kb1/agents/nope", "kb-admin", nil,
		map[string]string{"id": "kb1", "agentId": "nope"})
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for a nonexistent agent: %s", w.Code, w.Body.String())
	}
	if len(fs.attachedAgents) != 0 {
		t.Fatalf("a nonexistent agent reached the store: %v", fs.attachedAgents)
	}

	w = doJSON(t, h.AttachTeam, "PUT", "/api/kb/kb1/teams/nope", "kb-admin", nil,
		map[string]string{"id": "kb1", "teamId": "nope"})
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for a nonexistent team: %s", w.Code, w.Body.String())
	}
	if len(fs.attachedTeams) != 0 {
		t.Fatalf("a nonexistent team reached the store: %v", fs.attachedTeams)
	}
}

// Detach was never owner-scoped (it has no GetAgent call to begin with) — pinned
// so the rule above is not later "made consistent" by adding one. It needs no
// check of its own: it can only delete a link on THIS KB, so the "already
// attached to this KB" arm holds for it by construction, and a detach that
// matches nothing is a no-op.
func TestDetachIsNotOwnerScoped(t *testing.T) {
	fs, h := seedForeign(t)
	fs.attachedAgents["kb1/agent-Fremd"] = true

	w := doJSON(t, h.DetachAgent, "DELETE", "/api/kb/kb1/agents/agent-Fremd", "kb-admin", nil,
		map[string]string{"id": "kb1", "agentId": "agent-Fremd"})
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", w.Code, w.Body.String())
	}
	if _, still := fs.attachedAgents["kb1/agent-Fremd"]; still {
		t.Fatal("detach did not reach the store")
	}
}

func TestGetRegistryServesFieldsAndTools(t *testing.T) {
	h := testHandler(newFakeStore())
	w := doJSON(t, h.GetRegistry, "GET", "/api/agents/registry", "u1", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("registry: %d", w.Code)
	}
	var resp struct {
		Fields []map[string]any `json:"fields"`
		Tools  []string         `json:"tools"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Fields) == 0 {
		t.Fatal("fields missing")
	}
	// testHandler's AvailableTools returns kb_search + calculator.
	if len(resp.Tools) != 2 || resp.Tools[0] != "calculator" || resp.Tools[1] != "kb_search" {
		t.Fatalf("tools must be the AvailableTools set, sorted: %v", resp.Tools)
	}
}
