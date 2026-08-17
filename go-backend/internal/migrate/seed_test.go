package migrate

import "testing"

// envFunc builds a getenv func over a fixed map so the cases below need no
// t.Setenv and can run in parallel.
func envFunc(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

func TestSeedUsers(t *testing.T) {
	t.Parallel()

	t.Run("no passwords set seeds nobody", func(t *testing.T) {
		t.Parallel()
		if got := seedUsers(envFunc(nil)); len(got) != 0 {
			t.Errorf("seedUsers() = %+v, want empty", got)
		}
	})

	t.Run("admin only", func(t *testing.T) {
		t.Parallel()
		got := seedUsers(envFunc(map[string]string{"ADMIN_PASSWORD": "a"}))
		if len(got) != 1 {
			t.Fatalf("seedUsers() = %+v, want 1 user", got)
		}
		if got[0].username != "admin" || got[0].role != "superadmin" || got[0].password != "a" {
			t.Errorf("admin seed wrong: %+v", got[0])
		}
		if !got[0].resetRole {
			t.Error("admin must reset its role on conflict so a demoted admin is recoverable")
		}
	})

	// The regression this change exists for: TESTER_PASSWORD was set in .env
	// but read by nothing, so no tester row was ever created and the
	// documented credentials could not log in.
	t.Run("tester is seeded from TESTER_PASSWORD", func(t *testing.T) {
		t.Parallel()
		got := seedUsers(envFunc(map[string]string{"ADMIN_PASSWORD": "a", "TESTER_PASSWORD": "p"}))
		if len(got) != 2 {
			t.Fatalf("seedUsers() = %+v, want admin and tester", got)
		}
		tester := got[1]
		if tester.username != "tester" || tester.password != "p" {
			t.Errorf("tester seed wrong: %+v", tester)
		}
		if tester.role != "user" {
			t.Errorf("tester role = %q, want %q — the point of the account is a NON-privileged view", tester.role, "user")
		}
		if tester.resetRole {
			t.Error("tester must not reset its role on conflict; promoting it in the admin UI has to survive a re-seed")
		}
	})

	// Each password gates its own user: an unset ADMIN_PASSWORD must not
	// suppress the tester (the pre-fix code returned early on it).
	t.Run("tester only", func(t *testing.T) {
		t.Parallel()
		got := seedUsers(envFunc(map[string]string{"TESTER_PASSWORD": "p"}))
		if len(got) != 1 {
			t.Fatalf("seedUsers() = %+v, want 1 user", got)
		}
		if got[0].username != "tester" {
			t.Errorf("seedUsers() = %+v, want tester alone", got)
		}
	})
}

func TestSeedUpsertSQL(t *testing.T) {
	t.Parallel()

	reset := seedUpsertSQL(seedUser{role: "superadmin", resetRole: true})
	if !umContains(reset, "ON CONFLICT") {
		t.Errorf("upsert must be conflict-safe: %s", reset)
	}
	if !umContains(reset, "role          = EXCLUDED.role") {
		t.Errorf("resetRole upsert must overwrite the existing role: %s", reset)
	}

	keep := seedUpsertSQL(seedUser{role: "user", resetRole: false})
	if !umContains(keep, "ON CONFLICT") {
		t.Errorf("upsert must be conflict-safe: %s", keep)
	}
	if umContains(keep, "EXCLUDED.role") {
		t.Errorf("non-resetRole upsert must not touch the existing role: %s", keep)
	}
	if !umContains(keep, "password_hash = EXCLUDED.password_hash") {
		t.Errorf("every upsert must still refresh the password: %s", keep)
	}
}
