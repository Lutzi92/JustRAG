package tabular

import (
	"strings"
	"testing"
)

func TestBuildTypedTableSQL(t *testing.T) {
	t.Parallel()
	cols := []ColumnSpec{
		{Name: "gis_code", Type: TypeText}, {Name: "bgf", Type: TypeNumeric}, {Name: "stand", Type: TypeDate},
		{Name: "aktiv", Type: TypeBool}, {Name: "baujahr", Type: TypeText}, {Name: "baujahr_num", Type: TypeNumeric, ShadowOf: "baujahr"},
	}
	sql := BuildTypedTableSQL("sheet_x_0_0", cols)
	for _, want := range []string{
		`CREATE TABLE "tabular"."sheet_x_0_0" AS SELECT "_rowid"`,
		`"gis_code" AS "gis_code"`, `THEN "bgf"::numeric END AS "bgf"`, `THEN "stand"::date END AS "stand"`,
		`THEN "aktiv"::boolean END AS "aktiv"`, `THEN "baujahr"::numeric END AS "baujahr_num"`, `FROM "tabular"."sheet_x_0_0__stage"`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("missing %q in\n%s", want, sql)
		}
	}
	if strings.Count(sql, "'")%2 != 0 {
		t.Error("unbalanced quotes")
	}
	st := BuildStagingTableSQL("sheet_x_0_0", []string{"a", "b"})
	if !strings.Contains(st, `"_rowid" bigint`) || !strings.Contains(st, `"a" text`) || !strings.Contains(st, `"sheet_x_0_0__stage"`) {
		t.Errorf("staging: %s", st)
	}
}
