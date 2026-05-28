package confluence

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestExtractSVGText(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		contains []string // substrings that MUST be present (order-independent)
		absent   []string // substrings that MUST NOT be present
		wantZero bool     // result must be empty
	}{
		{
			name:     "title and desc elements",
			input:    `<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"><title>Architecture</title><desc>API to DB via cache</desc></svg>`,
			contains: []string{"Architecture", "API to DB via cache"},
		},
		{
			name: "text elements with labels",
			input: `<svg xmlns="http://www.w3.org/2000/svg">
				<text x="10" y="20">API Gateway</text>
				<text x="100" y="20">PostgreSQL</text>
				<text x="200" y="20">Redis</text>
			</svg>`,
			contains: []string{"API Gateway", "PostgreSQL", "Redis"},
		},
		{
			name: "text with nested tspan is concatenated",
			input: `<svg xmlns="http://www.w3.org/2000/svg">
				<text><tspan>User</tspan> <tspan>Service</tspan></text>
			</svg>`,
			contains: []string{"User Service"},
		},
		{
			name:     "empty SVG has no labels",
			input:    `<svg xmlns="http://www.w3.org/2000/svg"><rect x="0" y="0" width="100" height="100"/></svg>`,
			wantZero: true,
		},
		{
			name:     "invalid XML returns empty, no panic",
			input:    `<svg><not closed`,
			wantZero: true,
		},
		{
			name:     "ignores non-text elements",
			input:    `<svg><text>Label</text><script>alert('x')</script><style>.a{}</style></svg>`,
			contains: []string{"Label"},
			absent:   []string{"alert", ".a{}"},
		},
		{
			name:     "whitespace inside text is collapsed",
			input:    `<svg><text>   hello    world  </text></svg>`,
			contains: []string{"hello world"},
			absent:   []string{"   hello"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractSVGText([]byte(tt.input))
			if tt.wantZero {
				if got != "" {
					t.Errorf("expected empty result, got %q", got)
				}
				return
			}
			for _, want := range tt.contains {
				if !strings.Contains(got, want) {
					t.Errorf("expected %q in result\ngot: %q", want, got)
				}
			}
			for _, bad := range tt.absent {
				if strings.Contains(got, bad) {
					t.Errorf("did NOT expect %q in result\ngot: %q", bad, got)
				}
			}
		})
	}
}

func TestExtractDrawioText(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		contains []string
		wantZero bool
	}{
		{
			name: "mxfile with mxCell value attributes",
			input: `<mxfile><diagram><mxGraphModel><root>
				<mxCell id="1" value="API Gateway" vertex="1"/>
				<mxCell id="2" value="PostgreSQL" vertex="1"/>
				<mxCell id="3" value="Redis"/>
			</root></mxGraphModel></diagram></mxfile>`,
			contains: []string{"API Gateway", "PostgreSQL", "Redis"},
		},
		{
			name: "object wrapper with label attribute",
			input: `<mxfile><diagram><mxGraphModel><root>
				<object label="User Service" id="5"><mxCell vertex="1"/></object>
			</root></mxGraphModel></diagram></mxfile>`,
			contains: []string{"User Service"},
		},
		{
			name:     "HTML-escaped labels are decoded",
			input:    `<mxfile><root><mxCell value="R&amp;D Team"/></root></mxfile>`,
			contains: []string{"R&D Team"},
		},
		{
			name: "cells with no value are ignored",
			input: `<mxfile><root>
				<mxCell id="1" vertex="1"/>
				<mxCell id="2" value=""/>
				<mxCell id="3" value="Real Label"/>
			</root></mxfile>`,
			contains: []string{"Real Label"},
		},
		{
			name:     "empty mxfile has no labels",
			input:    `<mxfile><diagram><mxGraphModel><root/></mxGraphModel></diagram></mxfile>`,
			wantZero: true,
		},
		{
			name:     "SVG-format drawio export",
			input:    `<svg xmlns="http://www.w3.org/2000/svg" content="&lt;mxfile&gt;"><text>Legend</text></svg>`,
			contains: []string{"Legend"}, // falls through to extractSVGText which handles svg root
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractDrawioText([]byte(tt.input))
			if tt.wantZero {
				if got != "" {
					t.Errorf("expected empty, got %q", got)
				}
				return
			}
			for _, want := range tt.contains {
				if !strings.Contains(got, want) {
					t.Errorf("expected %q in result\ngot: %q", want, got)
				}
			}
		})
	}
}

func TestProcessDrawioMacros(t *testing.T) {
	sampleDrawio := `<mxfile><diagram><mxGraphModel><root>
		<mxCell value="Client"/><mxCell value="Server"/><mxCell value="Database"/>
	</root></mxGraphModel></diagram></mxfile>`

	tests := []struct {
		name        string
		html        string
		attachments []ConfluenceAttachment
		files       map[string][]byte
		downloadErr error
		contains    []string
		absent      []string
	}{
		{
			name: "drawio macro with matching attachment extracts labels",
			html: `<ac:structured-macro ac:name="drawio" ac:schema-version="1">
				<ac:parameter ac:name="diagramName">versuch01</ac:parameter>
				<ac:parameter ac:name="revision">1</ac:parameter>
			</ac:structured-macro>`,
			attachments: []ConfluenceAttachment{
				{Title: "versuch01", DownloadLink: "/d/1"},
				{Title: "versuch01.png", DownloadLink: "/d/1png"},
			},
			files: map[string][]byte{
				"/d/1": []byte(sampleDrawio),
			},
			contains: []string{"[Diagram: versuch01]", "Client", "Server", "Database"},
			absent:   []string{"<ac:structured-macro"},
		},
		{
			name: "drawio macro without diagramName still gets a placeholder",
			html: `<ac:structured-macro ac:name="drawio" ac:schema-version="1">
				<ac:parameter ac:name="revision">1</ac:parameter>
			</ac:structured-macro>`,
			contains: []string{"[Diagram:"},
			absent:   []string{"<ac:structured-macro"},
		},
		{
			name: "drawio macro with no matching attachment gets placeholder only",
			html: `<ac:structured-macro ac:name="drawio" ac:schema-version="1">
				<ac:parameter ac:name="diagramName">ghost</ac:parameter>
			</ac:structured-macro>`,
			attachments: []ConfluenceAttachment{
				{Title: "other", DownloadLink: "/d/o"},
			},
			contains: []string{"[Diagram: ghost]"},
			absent:   []string{"<ac:structured-macro"},
		},
		{
			name: "download failure still emits placeholder",
			html: `<ac:structured-macro ac:name="drawio" ac:schema-version="1">
				<ac:parameter ac:name="diagramName">fail</ac:parameter>
			</ac:structured-macro>`,
			attachments: []ConfluenceAttachment{
				{Title: "fail", DownloadLink: "/d/x"},
			},
			downloadErr: errors.New("500"),
			contains:    []string{"[Diagram: fail]"},
			absent:      []string{"<ac:structured-macro"},
		},
		{
			name:     "non-drawio macro is untouched",
			html:     `<ac:structured-macro ac:name="status"><ac:parameter ac:name="title">Done</ac:parameter></ac:structured-macro>`,
			contains: []string{"<ac:structured-macro"}, // left for acTagRe to strip later
		},
		{
			name:     "no macros is noop",
			html:     `<p>plain</p>`,
			contains: []string{"<p>plain</p>"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fetcher := &fakeAttachmentFetcher{
				attachments: tt.attachments,
				files:       tt.files,
				downloadErr: tt.downloadErr,
			}
			got := processDrawioMacros(context.Background(), tt.html, fetcher, "page-1")
			for _, want := range tt.contains {
				if !strings.Contains(got, want) {
					t.Errorf("expected %q in result\ngot: %s", want, got)
				}
			}
			for _, bad := range tt.absent {
				if strings.Contains(got, bad) {
					t.Errorf("did NOT expect %q in result\ngot: %s", bad, got)
				}
			}
		})
	}
}
