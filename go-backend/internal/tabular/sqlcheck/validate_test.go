package sqlcheck

import (
	"strings"
	"testing"
)

var allow = map[string]bool{"tabular.sheet_ab12_0_0": true, "tabular.sheet_ab12_0_1": true}

func TestValidateAcceptsRouterShapes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, sql        string
		wantTables       int
		wantLimitWrapped bool
		wantFuncs        []string
	}{
		{"ilike", `SELECT COUNT(*) FROM tabular."sheet_ab12_0_0" WHERE "stammdaten_ressort" ILIKE '%hmwk%'`, 1, true, []string{"count"}},
		{"group", `SELECT "gebaeude", SUM("bgf_m2_num") AS s FROM tabular."sheet_ab12_0_0" GROUP BY "gebaeude" HAVING SUM("bgf_m2_num") > 1000 ORDER BY s DESC LIMIT 50`, 1, false, []string{"sum"}},
		{"distinct-from", `SELECT "a" FROM tabular."sheet_ab12_0_0" WHERE "baujahr_num" IS DISTINCT FROM NULL AND "_rowid" BETWEEN 5 AND 9`, 1, true, nil},
		{"coalesce-cast", `SELECT COALESCE(SUM("x"::numeric), 0), date_trunc('month', "stand") FROM tabular."sheet_ab12_0_0" GROUP BY 2`, 1, true, []string{"sum", "date_trunc"}},
		{"cte-window", `WITH t AS (SELECT "id", ROW_NUMBER() OVER (PARTITION BY "k" ORDER BY "d") rn FROM tabular."sheet_ab12_0_1") SELECT * FROM t WHERE rn = 1`, 1, true, []string{"row_number"}},
		{"extract-interval", `SELECT EXTRACT(YEAR FROM "stand") FROM tabular."sheet_ab12_0_0" WHERE "stand" > now() - INTERVAL '1 day'`, 1, true, []string{"extract", "now"}},
		{"unicode-endash", `SELECT "größe" FROM tabular."sheet_ab12_0_0" WHERE "name" = 'Goethestraße 55 – Haus A'`, 1, true, nil},
		{"join", `SELECT a."id", b."val" FROM tabular."sheet_ab12_0_0" a JOIN tabular."sheet_ab12_0_1" b ON a."id" = b."id"`, 2, true, nil},
		{"trailing-semicolon", `SELECT 1 FROM tabular."sheet_ab12_0_0";`, 1, true, nil},
		{"limit-too-large", `SELECT "a" FROM tabular."sheet_ab12_0_0" LIMIT 5000`, 1, true, nil},
		// Fix round 1 (C1): pin the supported surface alongside the fix for
		// the nine unreachable-AST paths below, so a future change can't
		// silently narrow it back down.
		{"string-agg-orderby", `SELECT string_agg("a", ',' ORDER BY "a") FROM tabular."sheet_ab12_0_0"`, 1, true, []string{"string_agg"}},
		{"nullif", `SELECT NULLIF("a", '') FROM tabular."sheet_ab12_0_0"`, 1, true, nil},
		{"offset", `SELECT "a" FROM tabular."sheet_ab12_0_0" OFFSET 10`, 1, true, nil},
		{"case", `SELECT CASE WHEN "a" > 1 THEN 'x' ELSE 'y' END FROM tabular."sheet_ab12_0_0"`, 1, true, nil},
		{"paren-union", `(SELECT 1 FROM tabular."sheet_ab12_0_0") UNION (SELECT 2 FROM tabular."sheet_ab12_0_0")`, 1, true, nil},
		// Fix round 2 (regression 1 / R38): USING (...) and NATURAL JOIN
		// newly hit the fail-closed default (*tree.UsingJoinCond,
		// tree.NaturalJoinCond had no case).
		{"join-using", `SELECT 1 FROM tabular."sheet_ab12_0_0" a JOIN tabular."sheet_ab12_0_1" b USING ("id")`, 2, true, nil},
		{"natural-join", `SELECT 1 FROM tabular."sheet_ab12_0_0" NATURAL JOIN tabular."sheet_ab12_0_1"`, 2, true, nil},
		// Fix round 2 (R48(d)): a qualified column reference using a
		// declared table ALIAS (not a real schema/table name) must still
		// be accepted.
		{"alias-qualified-column", `SELECT a."id" FROM tabular."sheet_ab12_0_0" a`, 1, true, nil},
		// Fix round 3: two R48(d) qualifier shapes that were already
		// correctly accepted but had no dedicated test — a full 3-part
		// schema.table.column reference to an allowed table, and a 2-part
		// reference using the table's own bare (unaliased) object name.
		{"three-part-qualified-column", `SELECT tabular."sheet_ab12_0_0"."id" FROM tabular."sheet_ab12_0_0"`, 1, true, nil},
		{"bare-tablename-qualified-column", `SELECT "sheet_ab12_0_0"."id" FROM tabular."sheet_ab12_0_0"`, 1, true, nil},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			exec, info, err := Validate(c.sql, allow, 200)
			if err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if len(info.Tables) != c.wantTables {
				t.Errorf("tables = %v, want %d", info.Tables, c.wantTables)
			}
			if c.wantLimitWrapped != strings.HasPrefix(exec, "SELECT * FROM (") {
				t.Errorf("wrapped=%v exec=%q", !c.wantLimitWrapped, exec)
			}
			if c.wantLimitWrapped && !strings.HasSuffix(exec, ") AS _validated LIMIT 200") {
				t.Errorf("wrap suffix: %q", exec)
			}
			if strings.Contains(exec, "extract('year'") {
				t.Errorf("AST re-serialisation leaked into exec SQL: %q", exec)
			}
			for _, f := range c.wantFuncs {
				if !contains(info.Functions, f) {
					t.Errorf("functions %v lack %q", info.Functions, f)
				}
			}
		})
	}
}

func TestValidateRejects(t *testing.T) {
	t.Parallel()
	bad := map[string]string{
		"select-star-other-schema": `SELECT "id" FROM public.users`,
		"delete":                   `DELETE FROM tabular."sheet_ab12_0_0"`,
		"two-statements":           `SELECT 1 FROM tabular."sheet_ab12_0_0"; SELECT 2 FROM tabular."sheet_ab12_0_0"`,
		"pg_read_file":             `SELECT pg_read_file('/etc/passwd') FROM tabular."sheet_ab12_0_0"`,
		"unlisted-table":           `SELECT 1 FROM tabular."sheet_zz99_0_0"`,
		"subquery-other-schema":    `SELECT 1 FROM tabular."sheet_ab12_0_0" WHERE "id" IN (SELECT id FROM public.files)`,
		"comment":                  `SELECT 1 FROM tabular."sheet_ab12_0_0" -- x`,
		"set":                      `SET search_path = public`,
		"unqualified":              `SELECT 1 FROM "sheet_ab12_0_0"`,
	}
	for name, sql := range bad {
		if _, _, err := Validate(sql, allow, 200); err == nil {
			t.Errorf("%s: accepted %q", name, sql)
		}
	}
}

// TestValidateRejectsHiddenPaths is the C1 fix: nine AST paths the walker
// previously had no descend case for, so a relation or function reference
// reachable only through that path bypassed both allowlists undetected.
// Each payload is the security review's verbatim example (a forbidden
// relation or forbidden function reachable ONLY via the named path — if
// any of these were instead reachable some other way too, the test
// wouldn't isolate the specific gap). A tenth case (IFERROR) is not one of
// the nine but exercises the fail-closed default arm directly: it is a
// construct this package still has no explicit case for, so — unlike the
// other nine, each of which now has a bespoke descend case — its rejection
// depends entirely on the default arm (see the mutation-test evidence in
// the report).
func TestValidateRejectsHiddenPaths(t *testing.T) {
	t.Parallel()
	bad := map[string]string{
		"array-flatten":       `SELECT ARRAY(SELECT tablename::text FROM pg_tables) FROM tabular."sheet_ab12_0_0"`,
		"window-partition":    `SELECT SUM("a") OVER w FROM tabular."sheet_ab12_0_0" WINDOW w AS (PARTITION BY (SELECT count(*) FROM public.users))`,
		"funcexpr-orderby":    `SELECT string_agg("a", ',' ORDER BY current_setting('x')) FROM tabular."sheet_ab12_0_0"`,
		"limit-offset":        `SELECT 1 FROM tabular."sheet_ab12_0_0" OFFSET (SELECT count(*) FROM pg_ls_dir('/'))`,
		"collate":             `SELECT 1 FROM tabular."sheet_ab12_0_0" WHERE "a" = ((SELECT name FROM public.users LIMIT 1) COLLATE "en")`,
		"nullif-hidden":       `SELECT 1 FROM tabular."sheet_ab12_0_0" WHERE NULLIF((SELECT name FROM public.users LIMIT 1), '') IS NOT NULL`,
		"indirection":         `SELECT ("a")[(SELECT count(*) FROM public.users)] FROM tabular."sheet_ab12_0_0"`,
		"is-of-type":          `SELECT 1 FROM tabular."sheet_ab12_0_0" WHERE (SELECT name FROM public.users LIMIT 1) IS OF (text)`,
		"if-expr":             `SELECT IF((SELECT count(*) FROM public.users) > 0, 1, 2) FROM tabular."sheet_ab12_0_0"`,
		"fail-closed-default": `SELECT IFERROR((SELECT name FROM public.users LIMIT 1), 'x') FROM tabular."sheet_ab12_0_0"`,
	}
	for name, sql := range bad {
		if _, _, err := Validate(sql, allow, 200); err == nil {
			t.Errorf("%s: accepted %q", name, sql)
		}
	}
}

// TestValidateLimitTopLevelOnly is the I1 fix: info.Limit (and the
// wrap-or-not decision) must reflect only the top-level statement's own
// LIMIT, never one nested inside a subquery.
func TestValidateLimitTopLevelOnly(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, sql string
	}{
		{"nested-limit-under-outer-limit", `SELECT "a" FROM (SELECT "a" FROM tabular."sheet_ab12_0_0" LIMIT 10) z LIMIT 5000`},
		{"nested-limit-no-outer-limit", `SELECT * FROM tabular."sheet_ab12_0_0" CROSS JOIN (SELECT 1 FROM tabular."sheet_ab12_0_1" LIMIT 10) z`},
		{"negative-limit", `SELECT "a" FROM tabular."sheet_ab12_0_0" LIMIT -1`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			exec, info, err := Validate(c.sql, allow, 200)
			if err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if !strings.HasPrefix(exec, "SELECT * FROM (") || !strings.HasSuffix(exec, ") AS _validated LIMIT 200") {
				t.Errorf("expected the nested LIMIT to be ignored and the statement wrapped, got exec=%q info.Limit=%d", exec, info.Limit)
			}
		})
	}
}

// TestValidateCanonicalTableName is the I3 fix: the table-name check uses
// the parser's own structured Schema/Object fields (and its identifier
// folding: unquoted parts lower-cased, quoted parts verbatim) instead of
// rendering + blanket-lower-casing + quote-stripping the whole name.
func TestValidateCanonicalTableName(t *testing.T) {
	t.Parallel()
	if _, _, err := Validate(`SELECT 1 FROM "tabular.sheet_ab12_0_0"`, allow, 200); err == nil {
		t.Error("a single quoted identifier containing a literal dot must not be treated as schema.table")
	}
	if _, _, err := Validate(`SELECT 1 FROM TABULAR."SHEET_AB12_0_0"`, allow, 200); err == nil {
		t.Error("a quoted upper-case object name is a different relation from the lower-case allow-listed one")
	}
	if _, _, err := Validate(`SELECT 1 FROM Tabular."sheet_ab12_0_0"`, allow, 200); err != nil {
		t.Errorf("an unquoted schema must fold case-insensitively: %v", err)
	}
}

// TestValidateRejectsDollarQuotes is the R46 fix (fix round 2, regression
// 2): stripQuotedAndLiteralText only understands '...'/"..." pairs, so a
// single quote INSIDE a dollar-quoted string desyncs its quote-parity
// tracking and can let real SQL (here, a DML statement inside a CTE) pass
// the shape gate. Both dollar-tag forms are rejected outright, verbatim
// from the re-review; a dollar sign inside an ORDINARY literal (no
// dollar-quote syntax) must still be accepted.
func TestValidateRejectsDollarQuotes(t *testing.T) {
	t.Parallel()
	bad := map[string]string{
		"dollar-hidden-insert-cte": `WITH y AS (SELECT $$'$$ AS c), x AS (INSERT INTO tabular."sheet_ab12_0_0" VALUES (1) RETURNING $$'$$) SELECT * FROM x`,
		"dollar-tag-hidden":        `WITH y AS (SELECT $q$'$q$ AS c), x AS (INSERT INTO tabular."sheet_ab12_0_0" VALUES (1) RETURNING $q$'$q$) SELECT * FROM x`,
	}
	for name, sql := range bad {
		if _, _, err := Validate(sql, allow, 200); err == nil {
			t.Errorf("%s: accepted %q", name, sql)
		}
	}
	if _, _, err := Validate(`SELECT 1 FROM tabular."sheet_ab12_0_0" WHERE "a" = '$5'`, allow, 200); err != nil {
		t.Errorf("a dollar sign inside an ordinary literal must not be treated as a dollar-quote opener: %v", err)
	}
}

// TestValidateRejectsMinors is the R48 minors: reg* casts (a), AS OF
// SYSTEM TIME and index hints (b, CRDB-only syntax that can't execute
// against the real Postgres backend), an explicit catalog part (c), and a
// qualified reference to a relation outside the FROM clause (d).
func TestValidateRejectsMinors(t *testing.T) {
	t.Parallel()
	bad := map[string]string{
		"regclass-cast-postfix":   `SELECT 'pg_class'::regclass::text FROM tabular."sheet_ab12_0_0"`,
		"regclass-cast-func":      `SELECT CAST('x' AS regclass) FROM tabular."sheet_ab12_0_0"`,
		"as-of-system-time":       `SELECT 1 FROM tabular."sheet_ab12_0_0" AS OF SYSTEM TIME '-1s'`,
		"index-hint":              `SELECT 1 FROM tabular."sheet_ab12_0_0"@{FORCE_INDEX=x}`,
		"explicit-catalog":        `SELECT 1 FROM somedb.tabular."sheet_ab12_0_0"`,
		"qualified-foreign-star":  `SELECT tabular."sheet_zz99_0_0".* FROM tabular."sheet_ab12_0_0"`,
		"qualified-public-column": `SELECT public.users.name FROM tabular."sheet_ab12_0_0"`,
	}
	for name, sql := range bad {
		if _, _, err := Validate(sql, allow, 200); err == nil {
			t.Errorf("%s: accepted %q", name, sql)
		}
	}
}

// TestValidateRejectsParenTableExprAndIsNull is R48(e): ParenTableExpr
// wrapping a foreign relation (already fixed while cross-checking the
// TableExpr closed family in fix round 1 — pinned here with dedicated
// tests) and IS [NOT] NULL wrapping a foreign subquery.
func TestValidateRejectsParenTableExprAndIsNull(t *testing.T) {
	t.Parallel()
	bad := map[string]string{
		"paren-table-join":   `SELECT 1 FROM (tabular."sheet_ab12_0_0" JOIN tabular."sheet_zz99_0_0" ON true)`,
		"paren-table-nested": `SELECT 1 FROM ((SELECT 1 FROM tabular."sheet_zz99_0_0")) z`,
		"is-null":            `SELECT 1 FROM tabular."sheet_ab12_0_0" WHERE (SELECT 1 FROM tabular."sheet_zz99_0_0") IS NULL`,
		"is-not-null":        `SELECT 1 FROM tabular."sheet_ab12_0_0" WHERE (SELECT 1 FROM tabular."sheet_zz99_0_0") IS NOT NULL`,
	}
	for name, sql := range bad {
		if _, _, err := Validate(sql, allow, 200); err == nil {
			t.Errorf("%s: accepted %q", name, sql)
		}
	}
}

// TestValidateRejectsQualifiedCastTypes is the R55 fix: a cast/annotate-type
// target that isn't a plain parser-resolved *types.T — a schema-qualified
// type name (pg_catalog.regclass, public.mydomain), which parses to
// *tree.UnresolvedObjectName instead — silently bypassed the reg* check
// (the type assertion just failed and fell through). Rejected outright now,
// on both ::type and CAST(... AS type) syntax, and on ::: (AnnotateTypeExpr).
func TestValidateRejectsQualifiedCastTypes(t *testing.T) {
	t.Parallel()
	bad := map[string]string{
		"regclass-schema-qualified-postfix": `SELECT 'pg_class'::pg_catalog.regclass::text FROM tabular."sheet_ab12_0_0"`,
		"regclass-schema-qualified-cast":    `SELECT CAST('x' AS pg_catalog.regclass) FROM tabular."sheet_ab12_0_0"`,
		"domain-schema-qualified":           `SELECT CAST('x' AS public.mydomain) FROM tabular."sheet_ab12_0_0"`,
		"annotate-type-schema-qualified":    `SELECT ("a":::pg_catalog.regclass) FROM tabular."sheet_ab12_0_0"`,
	}
	for name, sql := range bad {
		if _, _, err := Validate(sql, allow, 200); err == nil {
			t.Errorf("%s: accepted %q", name, sql)
		}
	}

	accept := map[string]string{
		"cast-text-postfix":    `SELECT "a"::text FROM tabular."sheet_ab12_0_0"`,
		"cast-numeric-postfix": `SELECT "a"::numeric FROM tabular."sheet_ab12_0_0"`,
		"cast-int-func":        `SELECT CAST("a" AS int) FROM tabular."sheet_ab12_0_0"`,
		"annotate-type-int":    `SELECT ("a":::int) FROM tabular."sheet_ab12_0_0"`,
	}
	for name, sql := range accept {
		if _, _, err := Validate(sql, allow, 200); err != nil {
			t.Errorf("%s: rejected %q: %v", name, sql, err)
		}
	}
}

// TestValidateRejectsRegTypeArrays is the NEW-A fix (fix round 4): an
// ARRAY of a reg* type resolves to a *types.T whose PGName() is the
// Postgres array name — "_regclass", with a LEADING UNDERSCORE — so the
// reg* prefix test on the target type as written missed every array
// shape, and ('{pg_class}'::regclass[])[1]::oid was a working catalog-OID
// oracle (proven against the live database). The check now peels ARRAY
// wrappers before the prefix test.
func TestValidateRejectsRegTypeArrays(t *testing.T) {
	t.Parallel()
	bad := map[string]string{
		"regclass-array-postfix":  `SELECT '{pg_class}'::regclass[] FROM tabular."sheet_ab12_0_0"`,
		"regclass-array-oracle":   `SELECT ('{pg_class}'::regclass[])[1]::oid FROM tabular."sheet_ab12_0_0"`,
		"regclass-array-cast":     `SELECT CAST('{pg_class}' AS regclass[]) FROM tabular."sheet_ab12_0_0"`,
		"regclass-array-annotate": `SELECT ('{pg_class}':::regclass[]) FROM tabular."sheet_ab12_0_0"`,
		"regclass-array-keyword":  `SELECT '{pg_class}'::regclass ARRAY FROM tabular."sheet_ab12_0_0"`,
		"regproc-array":           `SELECT '{count}'::regproc[] FROM tabular."sheet_ab12_0_0"`,
		"regtype-array":           `SELECT '{int4}'::regtype[] FROM tabular."sheet_ab12_0_0"`,
		"regnamespace-array":      `SELECT '{public}'::regnamespace[] FROM tabular."sheet_ab12_0_0"`,
	}
	for name, sql := range bad {
		if _, _, err := Validate(sql, allow, 200); err == nil {
			t.Errorf("%s: accepted %q", name, sql)
		}
	}

	accept := map[string]string{
		"text-array":         `SELECT '{a,b}'::text[] FROM tabular."sheet_ab12_0_0"`,
		"int-array":          `SELECT '{1,2}'::int[] FROM tabular."sheet_ab12_0_0"`,
		"text-array-keyword": `SELECT '{a,b}'::text ARRAY FROM tabular."sheet_ab12_0_0"`,
	}
	for name, sql := range accept {
		if _, _, err := Validate(sql, allow, 200); err != nil {
			t.Errorf("%s: rejected %q: %v", name, sql, err)
		}
	}
}

// TestValidateRejectsLockingClause is the R56 fix: tree.Select.Locking
// (FOR UPDATE/SHARE/KEY SHARE/NO KEY UPDATE, optionally OF <table>) was
// never visited, so both the clause itself and any target table it named
// went unchecked.
func TestValidateRejectsLockingClause(t *testing.T) {
	t.Parallel()
	bad := map[string]string{
		"for-share":            `SELECT 1 FROM tabular."sheet_ab12_0_0" FOR SHARE`,
		"for-key-share":        `SELECT 1 FROM tabular."sheet_ab12_0_0" FOR KEY SHARE`,
		"for-share-of-foreign": `SELECT 1 FROM tabular."sheet_ab12_0_0" FOR SHARE OF "sheet_zz99_0_0"`,
	}
	for name, sql := range bad {
		if _, _, err := Validate(sql, allow, 200); err == nil {
			t.Errorf("%s: accepted %q", name, sql)
		}
	}
}

func TestValidateIsAggregate(t *testing.T) {
	t.Parallel()
	_, info, err := Validate(`SELECT SUM("bgf_num") FROM tabular."sheet_ab12_0_0"`, allow, 200)
	if err != nil || !info.IsAggregate {
		t.Fatalf("aggregate detection: info=%+v err=%v", info, err)
	}
	_, info, _ = Validate(`SELECT "bgf_num" FROM tabular."sheet_ab12_0_0"`, allow, 200)
	if info.IsAggregate {
		t.Fatal("plain projection flagged as aggregate")
	}
}

// TestReadOnlyShape is the R39 fix: the denied-keyword scan ignores
// double-quoted identifiers and single-quoted string literals (so a
// column literally named "update", or a string literal containing "SET",
// doesn't false-positive), while an actual UPDATE statement is still
// rejected; and a leading "(" is accepted so a parenthesized UNION operand
// passes the shape gate.
func TestReadOnlyShape(t *testing.T) {
	t.Parallel()
	if err := ReadOnlyShape(`SELECT 1 FROM tabular."sheet_ab12_0_0" WHERE "update" = 'A SET B'`); err != nil {
		t.Errorf("a quoted column named update and a string literal containing SET must not trip the keyword scan: %v", err)
	}
	if err := ReadOnlyShape(`UPDATE tabular.x SET a=1`); err == nil {
		t.Error("an actual UPDATE ... SET statement must still be rejected")
	}
	if err := ReadOnlyShape(`(SELECT 1 FROM tabular."sheet_ab12_0_0") UNION (SELECT 2 FROM tabular."sheet_ab12_0_0")`); err != nil {
		t.Errorf("a leading ( before a parenthesized UNION operand must be accepted: %v", err)
	}
}

// TestReadOnlyShapeLiteralTokenizer is the R54 fix: stripQuotedAndLiteralText
// only understood '...'/"..." pairs, so a Postgres escape string (E'...')
// containing a backslash-escaped quote — E'\” — desynced its quote-parity
// tracking: it saw the escaped quote as an ordinary opening/doubled-quote
// pair and stayed "inside the literal" past the point Postgres itself
// closed the string, blanking real SQL that followed (here, sql_query's
// only shape gate — it has no AST parser backstop). The five payloads are
// the re-review's verbatim shapes (dropped into an allowlisted-table query
// for the multi-statement/comment/DDL-keyword variants).
func TestReadOnlyShapeLiteralTokenizer(t *testing.T) {
	t.Parallel()
	bad := map[string]string{
		"estring-hide-semicolon-drop": `SELECT 1 FROM tabular."sheet_ab12_0_0" WHERE "a" = E'\'' ; DROP TABLE x ; 'z'`,
		"estring-hide-insert-cte":     `WITH y AS (SELECT E'\'' AS c), x AS (INSERT INTO tabular."sheet_ab12_0_0" VALUES (1) RETURNING 'q') SELECT * FROM x`,
		"estring-hide-comment":        "SELECT 1 FROM tabular.\"sheet_ab12_0_0\" WHERE \"a\" = E'\\'' -- x\n 'z'",
		"estring-hide-delete":         `SELECT 1 FROM tabular."sheet_ab12_0_0" WHERE "a" = E'\'' DELETE FROM tabular."sheet_ab12_0_0" 'z'`,
		"backslash-close-early":       `SELECT 1 FROM tabular."sheet_ab12_0_0" WHERE "a" = E'\' AND b = 1 AND c = 'x'`,
	}
	for name, sql := range bad {
		if err := ReadOnlyShape(sql); err == nil {
			t.Errorf("%s: accepted %q", name, sql)
		}
	}

	accept := map[string]string{
		"estring-plain":        `SELECT 1 FROM tabular."sheet_ab12_0_0" WHERE "a" = E'it\'s'`,
		"ustring-plain":        `SELECT 1 FROM tabular."sheet_ab12_0_0" WHERE "a" = U&'\0041'`,
		"dollar-in-literal":    `SELECT 1 FROM tabular."sheet_ab12_0_0" WHERE "a" = 'a$b$c'`,
		"dollar-in-identifier": `SELECT "a$b$c" FROM tabular."sheet_ab12_0_0"`,
	}
	for name, sql := range accept {
		if err := ReadOnlyShape(sql); err != nil {
			t.Errorf("%s: rejected %q: %v", name, sql, err)
		}
	}

	if err := ReadOnlyShape(`SELECT 1 FROM tabular."sheet_ab12_0_0" WHERE "a" = $$x$$`); err == nil {
		t.Error("a dollar-quoted literal must still be rejected (R46)")
	}
	if err := ReadOnlyShape(`SELECT 1 FROM tabular."sheet_ab12_0_0" WHERE "a" = 'unterminated`); err == nil {
		t.Error("an unterminated string literal must be rejected")
	}
}

// TestReadOnlyShapeUnicodeEscapeString is the NEW-B fix (fix round 4):
// Postgres has NO lexer-level backslash escape inside U&'…' — the \XXXX
// escapes are decoded after lexing and UESCAPE can rebind the escape
// character — so the lexer closes U&'\' at the quote right after the
// backslash. Scanning it with backslash-escaping made stripLiterals close
// LATER than Postgres and blank real SQL that follows, which is the
// direction that can hide a ";"/comment/DDL keyword from the checks. The
// assertions pin the rejection REASON, because the old behaviour also
// rejected the payload — but for the wrong reason (a runaway literal),
// which is an accident of quote parity, not a guarantee.
func TestReadOnlyShapeUnicodeEscapeString(t *testing.T) {
	t.Parallel()
	hidden := `SELECT 1 FROM tabular."sheet_ab12_0_0" WHERE "a" = U&'\' UESCAPE '!' ; DROP TABLE x`
	err := ReadOnlyShape(hidden)
	if err == nil {
		t.Fatalf("accepted %q", hidden)
	}
	if !strings.Contains(err.Error(), "only one statement") {
		t.Errorf("the ; after the U&'' literal must be visible to the statement check, got %v", err)
	}

	accept := map[string]string{
		"unicode-escape":         `SELECT 1 FROM tabular."sheet_ab12_0_0" WHERE "a" = U&'\0041'`,
		"custom-uescape":         `SELECT 1 FROM tabular."sheet_ab12_0_0" WHERE "a" = U&'!0041' UESCAPE '!'`,
		"doubled-quote-in-ustr":  `SELECT 1 FROM tabular."sheet_ab12_0_0" WHERE "a" = U&'it''s'`,
		"backslash-then-content": `SELECT 1 FROM tabular."sheet_ab12_0_0" WHERE "a" = U&'\' UESCAPE '!' AND "b" = 'x'`,
	}
	for name, sql := range accept {
		if err := ReadOnlyShape(sql); err != nil {
			t.Errorf("%s: rejected %q: %v", name, sql, err)
		}
	}
}

// TestReadOnlyShapeDollarTag is the NEW-C fix (fix round 4): a
// dollar-quote tag follows unquoted-identifier rules, so it never starts
// with a digit — "$1$" is the positional parameter $1 followed by "$".
// Reading it as an opener blanked everything up to the next "$1$", i.e.
// real SQL Postgres executes. As with NEW-B the payload was rejected
// either way, so the assertion pins the reason: without the fix it is
// only R46's blanket dollar-quote ban that catches it, and the hidden ";"
// is never seen at all.
func TestReadOnlyShapeDollarTag(t *testing.T) {
	t.Parallel()
	hidden := `SELECT 1 FROM tabular."sheet_ab12_0_0" WHERE 1 = $1$ ; DROP TABLE x ; $1$`
	err := ReadOnlyShape(hidden)
	if err == nil {
		t.Fatalf("accepted %q", hidden)
	}
	if !strings.Contains(err.Error(), "only one statement") {
		t.Errorf("a digit-leading $1$ is not a dollar-quote opener, so the ; must stay visible, got %v", err)
	}

	accept := map[string]string{
		"positional-param":    `SELECT 1 FROM tabular."sheet_ab12_0_0" WHERE "a" = 'x' AND 1 = $1`,
		"dollar-in-ident":     `SELECT "a$1$b" FROM tabular."sheet_ab12_0_0"`,
		"two-positional-args": `SELECT 1 FROM tabular."sheet_ab12_0_0" WHERE "a" = $1 AND "b" = $2`,
	}
	for name, sql := range accept {
		if err := ReadOnlyShape(sql); err != nil {
			t.Errorf("%s: rejected %q: %v", name, sql, err)
		}
	}

	// R46 still stands for every genuine tag shape.
	for _, sql := range []string{
		`SELECT 1 FROM tabular."sheet_ab12_0_0" WHERE "a" = $$x$$`,
		`SELECT 1 FROM tabular."sheet_ab12_0_0" WHERE "a" = $tag$x$tag$`,
		`SELECT 1 FROM tabular."sheet_ab12_0_0" WHERE "a" = $_t1$x$_t1$`,
	} {
		err := ReadOnlyShape(sql)
		if err == nil || !strings.Contains(err.Error(), "dollar-quoted") {
			t.Errorf("dollar-quoted literal must still be rejected as such: %q → %v", sql, err)
		}
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
