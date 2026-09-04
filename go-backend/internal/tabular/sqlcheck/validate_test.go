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

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
