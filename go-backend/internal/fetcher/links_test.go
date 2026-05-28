package fetcher

import (
	"reflect"
	"sort"
	"testing"
)

func TestExtractLinks(t *testing.T) {
	t.Parallel()
	html := `
<html><body>
<a href="/foo">foo</a>
<a href="https://other.example.com/bar">bar</a>
<a href="mailto:x@y">no</a>
<a href="javascript:alert(1)">no</a>
<a href="#frag">no</a>
<a href="page2.html">page2</a>
</body></html>`
	got := extractLinks(html, "https://example.com/blog/")
	sort.Strings(got)
	want := []string{
		"https://example.com/blog/page2.html",
		"https://example.com/foo",
		"https://other.example.com/bar",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got=%v want=%v", got, want)
	}
}
