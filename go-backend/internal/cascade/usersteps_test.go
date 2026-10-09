package cascade

import (
	"strings"
	"testing"
)

func TestUserDeleteSteps_ADKSessionsBeforeUser(t *testing.T) {
	for _, kbs := range [][]string{nil, {"kb1"}} {
		steps := userDeleteSteps("u1", kbs)
		idx := map[string]int{}
		for i, s := range steps {
			idx[s.sql] = i
		}
		sess, ok1 := idx[`DELETE FROM adk_sessions WHERE user_id = $1`]
		ust, ok2 := idx[`DELETE FROM adk_user_states WHERE user_id = $1`]
		usr, ok3 := idx[`DELETE FROM users WHERE id = $1`]
		if !ok1 || !ok2 || !ok3 {
			t.Fatalf("missing step(s): sessions=%v user_states=%v users=%v", ok1, ok2, ok3)
		}
		if !(sess < ust && ust < usr) {
			t.Fatalf("order wrong: sessions=%d user_states=%d users=%d", sess, ust, usr)
		}
		if steps[sess].args[0] != "u1" || steps[ust].args[0] != "u1" {
			t.Fatalf("args not the text user id: %v %v", steps[sess].args, steps[ust].args)
		}
		for _, s := range steps {
			if s.sql == `DELETE FROM adk_app_states` {
				t.Fatal("adk_app_states must be untouched")
			}
		}
	}
}

// Final review item 4: every transaction that deletes chats first deletes
// the agent chat's ADK sessions (id = chat id) and runs of those chats.
func TestChatDeletingStepsRemoveADKSessionsFirst(t *testing.T) {
	cases := map[string][]txStep{
		"kb":        kbDeleteSteps("kb1"),
		"global kb": globalKBDeleteSteps("kb1"),
		"user":      userDeleteSteps("u1", []string{"kb1"}),
	}
	for name, steps := range cases {
		chats, sess, runs := -1, -1, -1
		for i, s := range steps {
			switch {
			case strings.HasPrefix(s.sql, "DELETE FROM chats"):
				chats = i
			case strings.HasPrefix(s.sql, "DELETE FROM adk_sessions") && strings.Contains(s.sql, "FROM chats"):
				sess = i
			case strings.HasPrefix(s.sql, "DELETE FROM agent_runs"):
				runs = i
			}
		}
		if chats < 0 || sess < 0 || runs < 0 || sess > chats || runs > chats {
			t.Errorf("%s: chats=%d adk_sessions=%d agent_runs=%d (sessions and runs must precede chats)", name, chats, sess, runs)
		}
		if sess >= 0 && !strings.Contains(steps[sess].sql, "app_name = 'agentchat'") {
			t.Errorf("%s: session delete not scoped to the agent chat app: %s", name, steps[sess].sql)
		}
	}
}
