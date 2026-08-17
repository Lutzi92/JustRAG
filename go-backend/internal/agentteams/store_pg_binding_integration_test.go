//go:build integration

// Integration test for the two reads over agent_kb_links / team_kb_links that
// must NOT agree with each other.
//
// ListAttachedForKB is the chat picker's read and filters is_enabled away.
// ListBindingCandidatesForKB is the workflow canvas's read and does not. The
// whole difference between them is one WHERE clause in SQL, which no mocked
// test can reach — a unit test stubs out exactly the thing that would be wrong.
// Point either query at the other's rule and one of two live defects returns:
// a disabled agent becomes selectable in chat, or a KB whose default points at
// a disabled agent projects „keine Vorgabe" and the row becomes unclearable.
//
// Pool/skip pattern follows internal/adminglobalkbs/store_pg_editors_integration_test.go.

package agentteams_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/justrag/go-backend/internal/agentteams"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	host, port, name := os.Getenv("DB_HOST"), os.Getenv("DB_PORT"), os.Getenv("DB_NAME")
	if host == "" || port == "" || name == "" {
		t.Skip("agentteams integration tests require DB_* env (main Postgres)")
	}
	dsn := fmt.Sprintf("postgres://%s:%s@%s:%s/%s",
		url.QueryEscape(os.Getenv("DB_USER")), url.QueryEscape(os.Getenv("DB_PASSWORD")), host, port, name)
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func insertUser(t *testing.T, pool *pgxpool.Pool, username string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	err := pool.QueryRow(ctx, `
		INSERT INTO users (username, password_hash, role)
		VALUES ($1, 'x-not-a-real-hash', 'user')
		RETURNING id::text`, username).Scan(&id)
	if err != nil {
		t.Fatalf("insert user %s: %v", username, err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM users WHERE id = $1::uuid`, id) //nolint:errcheck
	})
	return id
}

func insertKB(t *testing.T, pool *pgxpool.Pool, ownerID string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	err := pool.QueryRow(ctx, `
		INSERT INTO knowledge_bases (name, description, user_id)
		VALUES ('agentteams-binding-test', 'fixture', $1::uuid)
		RETURNING id::text`, ownerID).Scan(&id)
	if err != nil {
		t.Fatalf("insert kb: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `DELETE FROM knowledge_bases WHERE id = $1::uuid`, id) //nolint:errcheck
	})
	return id
}

func insertAgent(t *testing.T, pool *pgxpool.Pool, userID, name string, enabled bool) string {
	t.Helper()
	ctx := context.Background()
	var id string
	err := pool.QueryRow(ctx, `
		INSERT INTO agents (user_id, name, is_enabled)
		VALUES ($1::uuid, $2, $3) RETURNING id::text`, userID, name, enabled).Scan(&id)
	if err != nil {
		t.Fatalf("insert agent %s: %v", name, err)
	}
	return id
}

func insertTeam(t *testing.T, pool *pgxpool.Pool, userID, name string, enabled bool) string {
	t.Helper()
	ctx := context.Background()
	var id string
	err := pool.QueryRow(ctx, `
		INSERT INTO agent_teams (user_id, name, is_enabled)
		VALUES ($1::uuid, $2, $3) RETURNING id::text`, userID, name, enabled).Scan(&id)
	if err != nil {
		t.Fatalf("insert team %s: %v", name, err)
	}
	return id
}

// The attach permission rule, in SQL. The handler test proves the wiring
// against a fake that mirrors this query; only this proves the query.
//
// Rule: exists at all, AND (the caller owns it OR it is already attached to
// this KB). Three arms, all three checked in both directions — a query that
// answered `true` unconditionally would pass an "owner is allowed" test on its
// own.
func TestAttachEligibilityOwnedOrAlreadyAttached(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := agentteams.NewStore(pool)

	owner := insertUser(t, pool, "agentteams-eligibility-owner")
	admin := insertUser(t, pool, "agentteams-eligibility-admin")
	kbID := insertKB(t, pool, admin)
	otherKB := insertKB(t, pool, admin)

	foreign := insertAgent(t, pool, owner, "Fremder Agent", true)
	own := insertAgent(t, pool, admin, "Eigener Agent", true)
	foreignTeam := insertTeam(t, pool, owner, "Fremdes Team", true)

	// foreign is attached to the OTHER KB only — so "attached somewhere" must
	// not be mistaken for "attached here".
	if err := store.AttachAgent(ctx, otherKB, foreign, false); err != nil {
		t.Fatalf("AttachAgent(other kb): %v", err)
	}

	check := func(what, agentID, kb, user string, wantExists, wantAllowed bool) {
		t.Helper()
		el, err := store.AgentAttachEligibility(ctx, agentID, kb, user)
		if err != nil {
			t.Fatalf("%s: AgentAttachEligibility: %v", what, err)
		}
		if el.Exists != wantExists || el.Allowed != wantAllowed {
			t.Errorf("%s: %+v, want Exists=%v Allowed=%v", what, el, wantExists, wantAllowed)
		}
	}

	check("own agent, unattached", own, kbID, admin, true, true)
	check("foreign agent, not attached to THIS kb", foreign, kbID, admin, true, false)

	if err := store.AttachAgent(ctx, kbID, foreign, false); err != nil {
		t.Fatalf("AttachAgent(this kb): %v", err)
	}
	check("foreign agent, now attached here", foreign, kbID, admin, true, true)

	// A well-formed but absent id: 404 territory, and Allowed must not be true
	// for it either.
	check("nonexistent agent", "00000000-0000-0000-0000-000000000000", kbID, admin, false, false)

	elT, err := store.TeamAttachEligibility(ctx, foreignTeam, kbID, admin)
	if err != nil {
		t.Fatalf("TeamAttachEligibility: %v", err)
	}
	if !elT.Exists || elT.Allowed {
		t.Errorf("foreign unattached team = %+v, want Exists=true Allowed=false", elT)
	}
	if err := store.AttachTeam(ctx, kbID, foreignTeam, false); err != nil {
		t.Fatalf("AttachTeam: %v", err)
	}
	elT, err = store.TeamAttachEligibility(ctx, foreignTeam, kbID, admin)
	if err != nil {
		t.Fatalf("TeamAttachEligibility after attach: %v", err)
	}
	if !elT.Allowed {
		t.Errorf("foreign team attached here = %+v, want Allowed=true — otherwise a KB "+
			"admin cannot clear a default someone else bound", elT)
	}
}

func addMember(t *testing.T, pool *pgxpool.Pool, teamID, agentID string) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO agent_team_members (team_id, agent_id) VALUES ($1::uuid, $2::uuid)`,
		teamID, agentID)
	if err != nil {
		t.Fatalf("insert team member: %v", err)
	}
}

// The second way a binding exists and never fires, and the one is_enabled
// cannot see: a team that is switched ON but has no ENABLED member.
//
// LoadTeamForChat's member query carries `AND is_enabled`, and
// resolveTeamSelection (internal/chat/http_send.go) turns zero members into
// (nil, "empty_team") — the selection is dropped and the full standard path
// runs. So the count the canvas needs is the count of ENABLED members, not of
// rows in agent_team_members; a plain count(*) would report the all-disabled
// team below as staffed. That clause lives only in SQL, which is why this is an
// integration test and not a unit test over a fake.
func TestBindingCandidatesCountOnlyEnabledMembers(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := agentteams.NewStore(pool)

	owner := insertUser(t, pool, "agentteams-empty-team-owner")
	kbID := insertKB(t, pool, owner)

	offMember := insertAgent(t, pool, owner, "Mitglied (aus)", false)
	onMember := insertAgent(t, pool, owner, "Mitglied (an)", true)

	emptyTeam := insertTeam(t, pool, owner, "Leeres Team", true)      // no members at all
	allOffTeam := insertTeam(t, pool, owner, "Team nur aus", true)    // members, none enabled
	staffedTeam := insertTeam(t, pool, owner, "Besetztes Team", true) // one enabled member
	addMember(t, pool, allOffTeam, offMember)
	addMember(t, pool, staffedTeam, onMember)
	addMember(t, pool, staffedTeam, offMember)

	// The empty one is the KB default — the state that made the canvas claim a
	// live binding.
	if err := store.AttachTeam(ctx, kbID, emptyTeam, true); err != nil {
		t.Fatalf("AttachTeam(default, empty): %v", err)
	}
	if err := store.AttachTeam(ctx, kbID, allOffTeam, false); err != nil {
		t.Fatalf("AttachTeam(all members disabled): %v", err)
	}
	if err := store.AttachTeam(ctx, kbID, staffedTeam, false); err != nil {
		t.Fatalf("AttachTeam(staffed): %v", err)
	}
	// An agent alongside, to pin the not-applicable encoding.
	soloAgent := insertAgent(t, pool, owner, "Solo-Agent", true)
	if err := store.AttachAgent(ctx, kbID, soloAgent, false); err != nil {
		t.Fatalf("AttachAgent: %v", err)
	}

	cands, err := store.ListBindingCandidatesForKB(ctx, kbID)
	if err != nil {
		t.Fatalf("ListBindingCandidatesForKB: %v", err)
	}
	byID := map[string]agentteams.BindingCandidate{}
	for _, c := range cands {
		byID[c.ID] = c
	}
	if len(cands) != 4 {
		t.Fatalf("candidates = %+v, want the three teams and the agent", cands)
	}

	if got := byID[emptyTeam]; got.EnabledMemberCount != 0 || !got.IsDefault || !got.IsEnabled {
		t.Errorf("empty default team = %+v, want EnabledMemberCount=0, IsDefault, "+
			"IsEnabled — it IS switched on, and that is exactly why is_enabled "+
			"alone drew it as live", got)
	}
	if got := byID[allOffTeam].EnabledMemberCount; got != 0 {
		t.Errorf("team whose only member is disabled: EnabledMemberCount = %d, want 0 — "+
			"a plain count(*) over agent_team_members would say 1 here, and chat-time "+
			"resolution would still drop the selection", got)
	}
	if got := byID[staffedTeam].EnabledMemberCount; got != 1 {
		t.Errorf("staffed team: EnabledMemberCount = %d, want 1 (one of its two "+
			"members is enabled) — an undercount here would make a working team "+
			"project as unable to answer", got)
	}
	if got := byID[soloAgent].EnabledMemberCount; got != 1 {
		t.Errorf("agent: EnabledMemberCount = %d, want 1 — the not-applicable "+
			"placeholder; a 0 would make every single-agent binding project inactive",
			got)
	}
}

// The fixture is the exact state the bug report describes: the KB's default is
// a DISABLED agent, and there is one other, enabled agent plus one disabled
// team attached alongside it.
func TestBindingReadsDisagreeAboutDisabledEntries(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := agentteams.NewStore(pool)

	owner := insertUser(t, pool, "agentteams-binding-owner")
	kbID := insertKB(t, pool, owner)

	offAgent := insertAgent(t, pool, owner, "Abgeschaltet", false)
	onAgent := insertAgent(t, pool, owner, "Bibliotheks-Assistent", true)
	offTeam := insertTeam(t, pool, owner, "Abgeschaltetes Team", false)

	// The disabled agent is the KB default. Written through the real attach,
	// so the "one default per KB across both tables" transaction is exercised
	// too — and so this test cannot pass against a store that refuses to bind
	// a disabled agent at all.
	if err := store.AttachAgent(ctx, kbID, offAgent, true); err != nil {
		t.Fatalf("AttachAgent(default, disabled): %v", err)
	}
	if err := store.AttachAgent(ctx, kbID, onAgent, false); err != nil {
		t.Fatalf("AttachAgent(enabled): %v", err)
	}
	if err := store.AttachTeam(ctx, kbID, offTeam, false); err != nil {
		t.Fatalf("AttachTeam(disabled): %v", err)
	}

	// --- the chat picker's contract: disabled entries stay OUT ---------------
	att, err := store.ListAttachedForKB(ctx, kbID)
	if err != nil {
		t.Fatalf("ListAttachedForKB: %v", err)
	}
	if len(att.Agents) != 1 || att.Agents[0].ID != onAgent {
		t.Fatalf("picker agents = %+v, want only the ENABLED agent — a disabled "+
			"agent offered in chat cannot be used: chat-time resolution "+
			"(LoadAgentForChat) rejects it", att.Agents)
	}
	if len(att.Teams) != 0 {
		t.Fatalf("picker teams = %+v, want none — the only attached team is disabled",
			att.Teams)
	}

	// --- the canvas's contract: everything attached, flagged -----------------
	cands, err := store.ListBindingCandidatesForKB(ctx, kbID)
	if err != nil {
		t.Fatalf("ListBindingCandidatesForKB: %v", err)
	}
	byID := map[string]agentteams.BindingCandidate{}
	for _, c := range cands {
		byID[c.ID] = c
	}
	if len(cands) != 3 {
		t.Fatalf("candidates = %+v, want all three attached entries including the "+
			"disabled ones", cands)
	}

	def := byID[offAgent]
	if def.Kind != "agent" || !def.IsDefault {
		t.Errorf("the disabled default = %+v, want kind=agent and IsDefault — this is "+
			"the row the admin has to be able to see and clear", def)
	}
	if def.IsEnabled {
		t.Error("the disabled default reports IsEnabled — the canvas would then " +
			"project a switched-off agent as if it answered every new chat")
	}
	if byID[onAgent].IsEnabled != true || byID[onAgent].IsDefault {
		t.Errorf("the enabled agent = %+v, want IsEnabled and not default", byID[onAgent])
	}
	if got := byID[offTeam]; got.Kind != "team" || got.IsEnabled {
		t.Errorf("the disabled team = %+v, want kind=team and IsEnabled=false", got)
	}

	// --- clearing works, and only through the row the canvas named ----------
	if err := store.AttachAgent(ctx, kbID, offAgent, false); err != nil {
		t.Fatalf("clear default: %v", err)
	}
	cands, err = store.ListBindingCandidatesForKB(ctx, kbID)
	if err != nil {
		t.Fatalf("ListBindingCandidatesForKB after clear: %v", err)
	}
	for _, c := range cands {
		if c.IsDefault {
			t.Errorf("still default after clearing: %+v — isDefault:false on the "+
				"bound link is the ONLY way the inspector can clear a binding, so a "+
				"no-op here is an unclearable KB", c)
		}
	}
	if len(cands) != 3 {
		t.Errorf("candidates = %+v after clear, want the links themselves kept — "+
			"clearing the DEFAULT must not detach anything", cands)
	}
}
