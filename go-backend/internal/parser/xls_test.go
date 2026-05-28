package parser

import "testing"

func TestXlsCanParse(t *testing.T) {
	t.Parallel()
	p := &XLSParser{}

	tests := []struct {
		name       string
		mime, file string
		want       bool
	}{
		{"exact mime match", "application/vnd.ms-excel", "data.xls", true},
		{"exact mime with non-xls ext", "application/vnd.ms-excel", "data.bin", true},
		{"octet-stream with xls ext", "application/octet-stream", "report.xls", true},
		{"empty mime with xls ext", "", "report.xls", true},
		{"octet-stream with xlsx ext", "application/octet-stream", "report.xlsx", false},
		{"csv mime and ext", "text/csv", "data.csv", false},
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

func TestXlsName(t *testing.T) {
	p := &XLSParser{}
	if p.Name() != "xls" {
		t.Errorf("Name() = %q, want %q", p.Name(), "xls")
	}
}
