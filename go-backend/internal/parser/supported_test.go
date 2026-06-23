package parser

import "testing"

func TestIsSupportedExtension(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"report.pdf", true},
		{"sheet.xlsx", true},
		{"notes.md", true},
		{"PHOTO.JPG", true},       // case-insensitive
		{"archive.TAR.GZ", false}, // unknown
		{"legacy.doc", false},     // no parser, must be rejected
		{"slides.ppt", false},     // legacy PowerPoint, unsupported
		{"Dockerfile", true},      // no extension -> text fallback
		{"README", true},          // no extension -> text fallback
		{"data.bin", false},
	}
	for _, c := range cases {
		if got := IsSupportedExtension(c.name); got != c.want {
			t.Errorf("IsSupportedExtension(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}
