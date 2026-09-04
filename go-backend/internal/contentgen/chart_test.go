package contentgen

import (
	"strings"
	"testing"

	"github.com/justrag/go-backend/internal/tabular"
)

// TestBuildChartSQL_NumericTypeIsChartEligible pins that a catalog column of
// tabular.TypeNumeric (the profiler's decimal/currency inference, Phase 2 of
// the spreadsheet-ingest rework) is treated as numeric by the chart y-column
// filter, exactly like TypeBigint/TypeFloat. Before this fix a Phase-2
// numeric-typed column was silently dropped from every chart's yColumns.
func TestBuildChartSQL_NumericTypeIsChartEligible(t *testing.T) {
	entry := &tabular.CatalogEntry{
		TableName: "sheet_abc_0",
		Columns: []tabular.ColumnSpec{
			{Name: "region", Type: tabular.TypeText},
			{Name: "revenue", Type: tabular.TypeNumeric},
		},
	}
	params := chartParams{
		ChartType:   "bar",
		XColumn:     "region",
		YColumns:    []string{"revenue"},
		Aggregation: "sum",
	}
	sql, keys, err := buildChartSQL(entry, params)
	if err != nil {
		t.Fatalf("buildChartSQL returned error for a TypeNumeric y column: %v", err)
	}
	if len(keys) != 1 || keys[0] != "revenue" {
		t.Fatalf("keys = %v, want [revenue]", keys)
	}
	if !strings.Contains(sql, `"revenue"`) {
		t.Fatalf("sql missing revenue column: %s", sql)
	}
}

// TestBuildChartSQL_NonNumericTypeIsRejected is the control case: a text
// column offered as a yColumn yields the "no numeric yColumns" error, so the
// TypeNumeric acceptance above isn't just accepting everything.
func TestBuildChartSQL_NonNumericTypeIsRejected(t *testing.T) {
	entry := &tabular.CatalogEntry{
		TableName: "sheet_abc_0",
		Columns: []tabular.ColumnSpec{
			{Name: "region", Type: tabular.TypeText},
			{Name: "notes", Type: tabular.TypeText},
		},
	}
	params := chartParams{
		ChartType:   "bar",
		XColumn:     "region",
		YColumns:    []string{"notes"},
		Aggregation: "sum",
	}
	if _, _, err := buildChartSQL(entry, params); err == nil {
		t.Fatal("expected an error for a non-numeric yColumn, got nil")
	}
}
