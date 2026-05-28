package confluence

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeUserResolver struct {
	users map[string]string
	errs  map[string]error
	calls int
}

func (f *fakeUserResolver) LookupUser(_ context.Context, key string) (string, error) {
	f.calls++
	if err, ok := f.errs[key]; ok {
		return "", err
	}
	if name, ok := f.users[key]; ok {
		return name, nil
	}
	return "", errors.New("user not found")
}

func TestResolveUserMentions(t *testing.T) {
	resolver := &fakeUserResolver{
		users: map[string]string{
			"abc123": "John Smith",
			"def456": "Jane Doe",
			"cloud1": "Cloud User",
		},
		errs: map[string]error{
			"broken": errors.New("500 server error"),
		},
	}

	tests := []struct {
		name     string
		html     string
		contains []string // substrings that MUST appear in result
		absent   []string // substrings that MUST NOT appear in result
	}{
		{
			name:     "no mentions is unchanged",
			html:     "<p>Hello world</p>",
			contains: []string{"<p>Hello world</p>"},
		},
		{
			name:     "userkey-only mention resolves to display name",
			html:     `<p>cc <ac:link><ri:user ri:userkey="abc123"/></ac:link></p>`,
			contains: []string{"@John Smith"},
			absent:   []string{"<ri:user", "<ac:link", "abc123"},
		},
		{
			name:     "account-id flavor (cloud) also resolves",
			html:     `<ac:link><ri:user ri:account-id="cloud1"/></ac:link>`,
			contains: []string{"@Cloud User"},
			absent:   []string{"<ri:user"},
		},
		{
			name:     "mention with plain-text-link-body is preserved verbatim when resolver succeeds",
			html:     `<ac:link><ri:user ri:userkey="abc123"/><ac:plain-text-link-body><![CDATA[@Johnny]]></ac:plain-text-link-body></ac:link>`,
			contains: []string{"@John Smith"}, // resolver wins over stale inline body
			absent:   []string{"<ri:user", "<ac:plain-text-link-body"},
		},
		{
			name:     "mention with body falls back to body text when resolver fails",
			html:     `<ac:link><ri:user ri:userkey="broken"/><ac:plain-text-link-body><![CDATA[@Broken User]]></ac:plain-text-link-body></ac:link>`,
			contains: []string{"@Broken User"},
			absent:   []string{"<ri:user"},
		},
		{
			name:     "unknown key without body falls back to the key itself",
			html:     `<ac:link><ri:user ri:userkey="unknown"/></ac:link>`,
			contains: []string{"@unknown"},
			absent:   []string{"<ri:user"},
		},
		{
			name:     "multiple mentions, repeated key uses cache",
			html:     `<p><ac:link><ri:user ri:userkey="abc123"/></ac:link> and <ac:link><ri:user ri:userkey="abc123"/></ac:link> and <ac:link><ri:user ri:userkey="def456"/></ac:link></p>`,
			contains: []string{"@John Smith", "@Jane Doe"},
			absent:   []string{"<ri:user", "abc123", "def456"},
		},
		{
			name:     "malformed ri:user tag is left alone",
			html:     `<ac:link><ri:user ri:userkey=></ac:link>`,
			contains: []string{"<ri:user"}, // regex won't match — untouched
		},
		{
			name:     "whitespace variations inside the mention",
			html:     `<ac:link>  <ri:user  ri:userkey="abc123" />  </ac:link>`,
			contains: []string{"@John Smith"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := resolveUserMentions(context.Background(), tt.html, resolver)
			for _, want := range tt.contains {
				if !strings.Contains(got, want) {
					t.Errorf("expected result to contain %q\ngot: %s", want, got)
				}
			}
			for _, bad := range tt.absent {
				if strings.Contains(got, bad) {
					t.Errorf("expected result NOT to contain %q\ngot: %s", bad, got)
				}
			}
		})
	}
}

func TestResolveUserMentions_ReturnsResolvedNames(t *testing.T) {
	resolver := &fakeUserResolver{
		users: map[string]string{
			"a": "Alice",
			"b": "Bob",
			"c": "Carol",
		},
	}
	// Alice appears twice; Bob once; d is unknown; c appears once.
	html := `<ac:link><ri:user ri:userkey="a"/></ac:link>
<ac:link><ri:user ri:userkey="b"/></ac:link>
<ac:link><ri:user ri:userkey="a"/></ac:link>
<ac:link><ri:user ri:userkey="d"/></ac:link>
<ac:link><ri:user ri:userkey="c"/></ac:link>`

	_, names := resolveUserMentions(context.Background(), html, resolver)

	want := []string{"Alice", "Bob", "Carol"}
	if len(names) != len(want) {
		t.Fatalf("expected %d unique resolved names, got %d (%v)", len(want), len(names), names)
	}
	for i, n := range want {
		if names[i] != n {
			t.Errorf("names[%d] = %q, want %q (full list: %v)", i, names[i], n, names)
		}
	}
}

func TestResolveUserMentions_CachesLookups(t *testing.T) {
	resolver := &fakeUserResolver{
		users: map[string]string{"abc": "Alice"},
	}

	html := strings.Repeat(`<ac:link><ri:user ri:userkey="abc"/></ac:link>`, 5)
	_, _ = resolveUserMentions(context.Background(), html, resolver)

	if resolver.calls != 1 {
		t.Errorf("expected 1 LookupUser call for 5 repeated mentions, got %d", resolver.calls)
	}
}

func TestResolveUserMentions_NilResolverIsSafe(t *testing.T) {
	// A page with a mention but no resolver should NOT panic. The mention
	// falls back to its body text if present, or the key otherwise.
	html := `<p>Hi <ac:link><ri:user ri:userkey="abc"/><ac:plain-text-link-body><![CDATA[@Someone]]></ac:plain-text-link-body></ac:link></p>`
	got, _ := resolveUserMentions(context.Background(), html, nil)
	if !strings.Contains(got, "@Someone") {
		t.Errorf("expected @Someone fallback, got: %s", got)
	}
}
