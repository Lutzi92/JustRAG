package tabular

import "testing"

func TestInferColumnType(t *testing.T) {
	cases := []struct {
		name string
		vals []string
		want ColumnType
	}{
		{"ints", []string{"1", "2", "-3", ""}, TypeBigint},
		{"floats", []string{"1.5", "2", "3.0"}, TypeFloat},
		{"mixed numeric+text", []string{"1", "two", "3"}, TypeText},
		{"dates", []string{"2024-01-02", "2024-12-31"}, TypeDate},
		{"bools", []string{"true", "false", "TRUE"}, TypeBool},
		{"all empty", []string{"", "", ""}, TypeText},
		{"currency stays text", []string{"$1,000", "$2,000"}, TypeText},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := inferColumnType(c.vals); got != c.want {
				t.Fatalf("inferColumnType(%v) = %s, want %s", c.vals, got, c.want)
			}
		})
	}
}

func TestSanitizeIdentifier(t *testing.T) {
	cases := map[string]string{
		"Total Revenue": "total_revenue",
		"2024 Q1":       "col_2024_q1",
		"":              "col",
		"price ($)":     "price",
		"Über Größe":    "ber_gr_e", // non-ASCII stripped
	}
	for in, want := range cases {
		if got := sanitizeIdentifier(in); got != want {
			t.Fatalf("sanitizeIdentifier(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDedupeIdentifiers(t *testing.T) {
	in := []string{"name", "name", "name"}
	got := dedupeIdentifiers(in)
	want := []string{"name", "name_2", "name_3"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("dedupeIdentifiers[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestDetectHeader(t *testing.T) {
	withHeader := [][]string{{"Name", "Age"}, {"Ann", "30"}}
	h, data := detectHeader(withHeader)
	if h[0] != "Name" || len(data) != 1 {
		t.Fatalf("expected header row consumed, got headers=%v data=%v", h, data)
	}
	headerless := [][]string{{"1", "2"}, {"3", "4"}}
	h2, data2 := detectHeader(headerless)
	if h2[0] != "col_1" || len(data2) != 2 {
		t.Fatalf("expected synthesized headers + all rows as data, got %v / %d rows", h2, len(data2))
	}
}

func TestFlagSemanticColumns(t *testing.T) {
	cols := []ColumnSpec{
		{Name: "id", Type: TypeBigint},
		{Name: "status", Type: TypeText}, // short, low-cardinality categorical
		{Name: "notes", Type: TypeText},  // long, high-cardinality free text
	}
	data := [][]string{
		{"1", "open", "customer reported intermittent latency during peak load"},
		{"2", "open", "billing discrepancy on the march invoice, escalated to finance"},
		{"3", "closed", "feature request: export to parquet for the analytics team"},
	}
	flagSemanticColumns(cols, data, 16, 0.6)
	if cols[0].Embedded {
		t.Fatal("numeric column must not be flagged")
	}
	if cols[1].Embedded {
		t.Fatal("short low-cardinality categorical must not be flagged")
	}
	if !cols[2].Embedded {
		t.Fatal("long high-cardinality free-text column must be flagged")
	}
}

func TestFlagSemanticColumnsThresholdSentinels(t *testing.T) {
	cols := []ColumnSpec{{Name: "status", Type: TypeText}}
	data := [][]string{{"open"}, {"open"}, {"closed"}}
	// Both sentinels 0 => filters disabled => any TEXT column qualifies.
	flagSemanticColumns(cols, data, 0, 0)
	if !cols[0].Embedded {
		t.Fatal("sentinel-0 thresholds should flag any TEXT column")
	}
}
