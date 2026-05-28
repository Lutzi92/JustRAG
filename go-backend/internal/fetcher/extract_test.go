package fetcher

import (
	"strings"
	"testing"
)

const sampleArticle = `
<html>
<head><title>Example Title</title></head>
<body>
<header><nav>menu menu menu</nav></header>
<article>
<h1>Example Title</h1>
<p>This is the first paragraph of the article body. It has enough text to
be considered the main content by any reasonable readability algorithm.
We need a few more sentences here so the heuristic agrees with us.</p>
<p>Second paragraph with <a href="https://example.com/x">a link</a> in it.</p>
</article>
<footer>cookies cookies cookies</footer>
</body>
</html>`

func TestExtractFindsTitleAndContent(t *testing.T) {
	t.Parallel()
	title, md, err := Extract(sampleArticle, "https://example.com/post")
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if !strings.Contains(title, "Example Title") {
		t.Errorf("title = %q, want substring %q", title, "Example Title")
	}
	if !strings.Contains(md, "first paragraph") {
		t.Errorf("markdown missing body: %q", md)
	}
	if strings.Contains(md, "menu menu menu") {
		t.Errorf("markdown should not include nav: %q", md)
	}
}

func TestExtractEmptyHTMLReturnsError(t *testing.T) {
	t.Parallel()
	_, _, err := Extract("", "https://example.com/")
	if err == nil {
		t.Error("expected error for empty HTML")
	}
}
