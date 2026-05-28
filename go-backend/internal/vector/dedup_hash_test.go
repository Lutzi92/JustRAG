package vector

import (
	"strings"
	"testing"
)

func TestNormalizeContent_LowercaseAndCollapseWhitespace(t *testing.T) {
	t.Parallel()
	got := NormalizeContent("  Hello\nWorld\t\tFoo\n\n  ")
	want := "hello world foo"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestNormalizeContent_EmptyInputs(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"", " ", "\n\t\n"} {
		if got := NormalizeContent(in); got != "" {
			t.Errorf("expected empty for %q, got %q", in, got)
		}
	}
}

func TestNormalizeContent_PreservesPunctuation(t *testing.T) {
	t.Parallel()
	got := NormalizeContent("Hello, World! It's a test.")
	want := "hello, world! it's a test."
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestHashContent_DeterministicAndStable(t *testing.T) {
	t.Parallel()
	h1 := HashContent("Hello World")
	h2 := HashContent("Hello World")
	if h1 != h2 {
		t.Errorf("hash should be deterministic, got %q vs %q", h1, h2)
	}
	if len(h1) != 64 {
		t.Errorf("expected 64-char hex, got %d chars: %q", len(h1), h1)
	}
}

func TestHashContent_NormalizationApplied(t *testing.T) {
	t.Parallel()
	a := HashContent("Hello\nWorld")
	b := HashContent("HELLO  world")
	if a != b {
		t.Errorf("expected same hash after normalization, got %q vs %q", a, b)
	}
}

func TestHashContent_EmptyReturnsEmpty(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"", " ", "\n\t"} {
		if got := HashContent(in); got != "" {
			t.Errorf("expected empty hash for %q, got %q", in, got)
		}
	}
}

func TestHashContent_DifferentTextDifferentHash(t *testing.T) {
	t.Parallel()
	a := HashContent("Hello World")
	b := HashContent("Hello Worlds")
	if a == b {
		t.Errorf("expected different hashes, both got %q", a)
	}
}

func TestHashContent_HexCharsOnly(t *testing.T) {
	t.Parallel()
	h := HashContent("any text")
	for _, r := range h {
		if !strings.ContainsRune("0123456789abcdef", r) {
			t.Errorf("non-hex char in hash: %q", h)
			break
		}
	}
}
