package chat

import "testing"

func TestDeriveFarmIDFromFilename(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"kz numeric", "KZ 1.docx", "KZ 1"},
		{"kz named", "KZ 4 Bayserke-Agro.docx", "KZ 4"},
		{"uz k", "UZ K 2.docx", "UZ K 2"},
		{"uz s", "UZ S 6.docx", "UZ S 6"},
		{"kz double digit named", "KZ 10 Kazmaly.docx", "KZ 10"},
		{"path prefix", "Case studies KZ/KZ 8 Genofond.docx", "KZ 8"},
	}
	for _, c := range cases {
		if got := deriveFarmIDFromFilename(c.in); got != c.want {
			t.Errorf("%s: deriveFarmIDFromFilename(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestDeriveFarmIDFromFilename_FallbackToStem(t *testing.T) {
	// No leading ID token -> cleaned stem.
	if got := deriveFarmIDFromFilename("annual_report.pdf"); got != "annual_report" {
		t.Errorf("fallback: got %q, want %q", got, "annual_report")
	}
}

func TestStructuredTableShape(t *testing.T) {
	tbl := StructuredTable{
		Columns:    []TableColumn{{Key: "farm_id", Label: "Farm ID", Type: "string", DeriveFrom: "filename"}},
		Rows:       []TableRow{{"farm_id": "KZ 1", "_file_name": "KZ 1.docx"}},
		Truncated:  true,
		TotalFiles: 25,
	}
	if tbl.Rows[0]["farm_id"] != "KZ 1" {
		t.Fatal("row value mismatch")
	}
	if tbl.Columns[0].DeriveFrom != "filename" {
		t.Fatal("column DeriveFrom mismatch")
	}
}
