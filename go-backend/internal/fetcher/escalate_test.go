package fetcher

import (
	"strings"
	"testing"
)

func TestShouldEscalate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		res  *Result
		want bool
	}{
		{
			name: "200 with substantial markdown",
			res:  &Result{StatusCode: 200, Markdown: largeBody(800)},
			want: false,
		},
		{
			name: "403 with cloudflare marker",
			res:  &Result{StatusCode: 403, HTML: `<title>Just a moment...</title>`},
			want: true,
		},
		{
			name: "503 with anubis marker",
			res:  &Result{StatusCode: 503, HTML: `meta name="anubis_version"`},
			want: true,
		},
		{
			name: "200 but tiny markdown and JS-heavy body",
			res: &Result{
				StatusCode: 200,
				Markdown:   "x",
				HTML:       `<html><body><div id="root"></div><script></script><script></script><script></script><script></script><script></script><script></script></body></html>`,
			},
			want: true,
		},
		{
			name: "404 dead URL — do NOT escalate",
			res:  &Result{StatusCode: 404, HTML: ""},
			want: false,
		},
		{
			name: "nil result",
			res:  nil,
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := shouldEscalate(tc.res); got != tc.want {
				t.Errorf("shouldEscalate=%v want=%v", got, tc.want)
			}
		})
	}
}

func largeBody(n int) string {
	return strings.Repeat("a", n)
}
