package academic

import "testing"

func TestIsJustFindURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want bool
	}{
		{
			name: "justfind URL",
			url:  "https://justfind.example.com/EDS/Record?id=123",
			want: true,
		},
		{
			name: "justfind URL mixed case",
			url:  "https://JustFind.example.com/EDS/Record?id=123",
			want: true,
		},
		{
			name: "edsrecord URL",
			url:  "https://library.example.com/EdsRecord/abc",
			want: true,
		},
		{
			name: "edsrecord URL lowercase",
			url:  "https://library.example.com/edsrecord/abc",
			want: true,
		},
		{
			name: "regular URL",
			url:  "https://arxiv.org/pdf/1234.5678.pdf",
			want: false,
		},
		{
			name: "empty URL",
			url:  "",
			want: false,
		},
		{
			name: "plain domain",
			url:  "https://example.com/paper.pdf",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isJustFindURL(tt.url)
			if got != tt.want {
				t.Errorf("isJustFindURL(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

func TestExtractLinks(t *testing.T) {
	raw := `<html><body>
		<a href="https://example.com/paper.pdf">PDF Download</a>
		<a href='https://example.com/pdfviewer?id=123'>View</a>
		<a href="https://example.com/other">Other</a>
	</body></html>`

	links := extractLinks(raw)
	if len(links) != 3 {
		t.Fatalf("expected 3 links, got %d: %v", len(links), links)
	}
	if links[0].href != "https://example.com/paper.pdf" {
		t.Errorf("unexpected href[0]: %q", links[0].href)
	}
	if links[0].text != "PDF Download" {
		t.Errorf("unexpected text[0]: %q", links[0].text)
	}
	if links[1].href != "https://example.com/pdfviewer?id=123" {
		t.Errorf("unexpected href[1]: %q", links[1].href)
	}
	if links[2].href != "https://example.com/other" {
		t.Errorf("unexpected href[2]: %q", links[2].href)
	}
}

func TestScorePDFLink(t *testing.T) {
	tests := []struct {
		name      string
		link      linkInfo
		wantAbove int
		wantBelow int
	}{
		{
			name:      "direct PDF download",
			link:      linkInfo{href: "https://example.com/article/paper.pdf", text: "PDF Download"},
			wantAbove: 15,
		},
		{
			name:      "fulltext viewer",
			link:      linkInfo{href: "https://example.com/fulltext/view", text: "Full Text"},
			wantAbove: 5,
		},
		{
			name:      "thumbnail deprioritized",
			link:      linkInfo{href: "https://example.com/thumbnail.pdf", text: "Thumbnail"},
			wantBelow: 0,
		},
		{
			name:      "supplement deprioritized",
			link:      linkInfo{href: "https://example.com/supplement.pdf", text: "Supplement"},
			wantBelow: 5,
		},
		{
			name:      "generic pdf link",
			link:      linkInfo{href: "https://example.com/pdfviewer?id=1", text: "View"},
			wantAbove: -1,
			wantBelow: 10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := scorePDFLink(tt.link)
			if tt.wantAbove != 0 && score <= tt.wantAbove {
				t.Errorf("score %d, want above %d", score, tt.wantAbove)
			}
			if tt.wantBelow != 0 && score >= tt.wantBelow {
				t.Errorf("score %d, want below %d", score, tt.wantBelow)
			}
		})
	}
}
