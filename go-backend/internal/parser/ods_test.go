package parser

import "testing"

func TestOdsCanParse(t *testing.T) {
	t.Parallel()
	p := &OdsParser{}

	tests := []struct {
		name       string
		mime, file string
		want       bool
	}{
		{"ods mime with ods ext", "application/vnd.oasis.opendocument.spreadsheet", "data.ods", true},
		{"ods mime with bin ext", "application/vnd.oasis.opendocument.spreadsheet", "data.bin", true},
		{"octet-stream with ods ext", "application/octet-stream", "report.ods", true},
		{"empty mime with ods ext", "", "report.ods", true},
		{"octet-stream with xlsx ext", "application/octet-stream", "report.xlsx", false},
		{"csv mime with csv ext", "text/csv", "data.csv", false},
		{"empty mime and file", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := p.CanParse(tt.mime, tt.file)
			if got != tt.want {
				t.Errorf("CanParse(%q, %q) = %v, want %v", tt.mime, tt.file, got, tt.want)
			}
		})
	}
}

func TestOdsName(t *testing.T) {
	t.Parallel()
	p := &OdsParser{}
	if p.Name() != "ods" {
		t.Errorf("Name() = %q, want %q", p.Name(), "ods")
	}
}
