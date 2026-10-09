package kbaccess

import "github.com/justrag/go-backend/internal/auth"

// VisibleKBsCTE is the SQL mirror of EffectiveRole: one CTE, named
// visible_kbs, that answers "which knowledge bases can this caller open, and
// with which role" for every KB at once, so a query can filter rows per KB
// without calling EffectiveRole once per row. internal/globalsearch is its
// consumer.
//
// KEEP IN SYNC WITH EffectiveRole (middleware.go). It mirrors the ladder rule
// for rule and in the same order — the order is load-bearing, exactly as it
// is there (an explicit membership beats the implicit roles a public KB
// grants, otherwise an editor on a public KB would be demoted to view):
//
//  1. system role superadmin            -> owner
//  2. a kb_members row with a valid role -> that role
//  3. public and system role admin       -> admin
//  4. public and published               -> view
//  5. otherwise                          -> NULL (invisible)
//
// A change to EffectiveRole must be made here in the same commit. The parity
// test is TestVisibilityAgreesWithEffectiveRole in
// internal/globalsearch/store_pg_integration_test.go: it runs this CTE (through
// the search queries) and the real EffectiveRole, fed by PGStore.GetKBByID and
// GetKBRole exactly as RequireKBRole feeds it, over a fixture matrix that
// covers every rung, and requires them to agree on visibility and role for
// every (caller, KB) pair. It lives in that package because
// internal/globalsearch is in CI's integration package list.
//
// Columns: id, name, description, header_text, visibility, role.
//
// Parameters: $1 caller user id, $2 caller system role (from the auth claims,
// as RequireKBRole passes it), $3 scoping kb id or NULL for all KBs. A
// consumer's own parameters start at $4.
//
// Details that keep the mirror exact:
//   - "public" is visibility = 'public', the same expression
//     PGStore.GetKBByID aliases to IsGlobal.
//   - Rule 2 tests the role against the four KB roles rather than IS NOT
//     NULL, because EffectiveRole tests Valid(memberRole). The kb_members
//     CHECK constraint makes the two equivalent today; the IN list keeps them
//     equivalent if the constraint ever widens.
//   - kb_members' primary key (kb_id, user_id) guarantees the LEFT JOIN adds
//     at most one row per KB, so the CTE never duplicates a KB.
//   - Every role and system-role literal is spliced in from the Go
//     constants, so a renamed role is a compile-time change here too, not a
//     silent drift.
//
// The CTE deliberately does NOT filter on role: consumers add
// `WHERE v.role IS NOT NULL`. That way a scoped lookup can tell "no such KB"
// (no row) from "KB exists but is invisible" (a row with a NULL role).
//
// Superadmins and system admins see a lot through this predicate (every KB,
// and every public KB, respectively). That is correct — it is what
// EffectiveRole grants them on every route — and must not be "fixed" here in
// isolation.
const VisibleKBsCTE = `
	visible_kbs AS (
		SELECT kb.id, kb.name, kb.description, kb.header_text, kb.visibility,
		       CASE
		         WHEN $2 = '` + auth.RoleSuperAdmin + `' THEN '` + RoleOwner + `'
		         WHEN m.role IN ('` + RoleView + `', '` + RoleEdit + `', '` +
	RoleAdmin + `', '` + RoleOwner + `') THEN m.role
		         WHEN kb.visibility = 'public' AND $2 = '` + auth.RoleAdmin + `' THEN '` + RoleAdmin + `'
		         WHEN kb.visibility = 'public' AND kb.is_published THEN '` + RoleView + `'
		       END AS role
		FROM knowledge_bases kb
		LEFT JOIN kb_members m ON m.kb_id = kb.id AND m.user_id = $1::uuid
		WHERE ($3::uuid IS NULL OR kb.id = $3::uuid)
	)`
