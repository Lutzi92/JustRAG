package database

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// readOnlyProbe is the minimal pgx surface VerifyReadOnly needs. Declaring it
// here (rather than taking *pgxpool.Pool) keeps the probe unit-testable with a
// fake — the real DB path is exercised by the integration suite.
type readOnlyProbe interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// dangerousFunctions are the server-side file/program escape hatches that turn
// a SELECT-only surface into arbitrary host read/write. They are superuser-only
// by default (EXECUTE revoked from PUBLIC), so a plain least-privilege role
// answers "false" for all of them; a "true" means someone granted them.
//
// Signatures are resolved with to_regprocedure so an uninstalled extension
// (dblink is not present on a stock cluster) yields NULL instead of erroring.
var dangerousFunctions = []string{
	"pg_read_file(text)",
	"pg_read_binary_file(text)",
	"pg_ls_dir(text)",
	"lo_import(text)",
	"lo_export(oid,text)",
	"dblink(text,text)",
}

// dangerousRoles are the predefined roles that re-grant the same escape hatches
// wholesale. pg_execute_server_program is what makes `COPY … FROM PROGRAM`
// work; the two file roles cover pg_read_file/pg_write_file.
var dangerousRoles = []string{
	"pg_read_server_files",
	"pg_write_server_files",
	"pg_execute_server_program",
}

// VerifyReadOnly asserts that a pool really is the least-privilege SELECT-only
// surface the sql_query / table_query MCP tools assume.
//
// The tools' regex/keyword validator is only a second line of defence; the
// database role is the real boundary, and nothing until now checked that the
// operator actually configured one. A DSN pointing at the read/write app role
// (or at a superuser) would have been accepted silently and handed to
// LLM-authored SQL.
//
// Three assertions, all fail-closed:
//  1. a write must be rejected — catches a DSN that overrode
//     default_transaction_read_only=off on a role that can actually write;
//  2. the role must not be a superuser — a superuser bypasses every grant,
//     including the read-only transaction default via a later SET;
//  3. the role must hold no EXECUTE on the server-side file/program functions
//     and no membership in the roles that grant them.
//
// Callers treat a non-nil error like a failed connect: close the pool and leave
// the tool in its disabled state rather than refusing to start the server.
func VerifyReadOnly(ctx context.Context, q readOnlyProbe) error {
	// (1) Writes must fail. Any error here is a pass — permission denied and
	// "read-only transaction" are both fine; only success is disqualifying.
	// The temp table would die with the session anyway, but we never get to
	// use it: a successful CREATE means the caller is about to close the pool.
	if _, err := q.Exec(ctx, "CREATE TEMP TABLE justrag_readonly_probe (x int)"); err == nil {
		return fmt.Errorf("read-only pool accepted a write (CREATE TEMP TABLE succeeded): " +
			"JUSTRAG_DB_URL_READONLY must point at a role with SELECT-only grants")
	}

	// (2) Superuser bypasses every grant and can SET default_transaction_read_only=off.
	var isSuperuser bool
	if err := q.QueryRow(ctx,
		"SELECT rolsuper FROM pg_roles WHERE rolname = current_user",
	).Scan(&isSuperuser); err != nil {
		return fmt.Errorf("read-only pool: check superuser status: %w", err)
	}
	if isSuperuser {
		return fmt.Errorf("read-only pool connects as a superuser: " +
			"JUSTRAG_DB_URL_READONLY must use a dedicated least-privilege role")
	}

	// (3) Server-side file/program escape hatches, by direct EXECUTE grant…
	var hasDangerousFunc bool
	if err := q.QueryRow(ctx, `
		SELECT coalesce(bool_or(has_function_privilege(current_user, p.oid, 'execute')), false)
		FROM (SELECT to_regprocedure(sig) AS oid FROM unnest($1::text[]) AS sig) p
		WHERE p.oid IS NOT NULL`,
		dangerousFunctions,
	).Scan(&hasDangerousFunc); err != nil {
		return fmt.Errorf("read-only pool: check dangerous function grants: %w", err)
	}
	if hasDangerousFunc {
		return fmt.Errorf("read-only pool role has EXECUTE on server-side file/program functions (%v): "+
			"REVOKE them before enabling the sql_query tool", dangerousFunctions)
	}

	// …and by membership in the predefined roles that grant them wholesale.
	var hasDangerousRole bool
	if err := q.QueryRow(ctx, `
		SELECT coalesce(bool_or(pg_has_role(current_user, r.rolname, 'usage')), false)
		FROM pg_roles r WHERE r.rolname = ANY($1::text[])`,
		dangerousRoles,
	).Scan(&hasDangerousRole); err != nil {
		return fmt.Errorf("read-only pool: check dangerous role membership: %w", err)
	}
	if hasDangerousRole {
		return fmt.Errorf("read-only pool role is a member of a privileged role (%v): "+
			"REVOKE the membership before enabling the sql_query tool", dangerousRoles)
	}

	return nil
}
