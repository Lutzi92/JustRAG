package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHashFixture(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "fixture.jsonl")
	if err := os.WriteFile(p, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := HashFixture(p)
	if err != nil {
		t.Fatal(err)
	}
	want := "5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03"
	if got != want {
		t.Errorf("HashFixture = %q, want %q", got, want)
	}
}

func TestHashFixture_MissingFile(t *testing.T) {
	_, err := HashFixture("/nonexistent/path.jsonl")
	if err == nil || !strings.Contains(err.Error(), "open fixture") {
		t.Errorf("expected 'open fixture' error, got %v", err)
	}
}

func TestHashFixtureContent(t *testing.T) {
	got := HashFixtureContent([]byte("hello\n"))
	want := "5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03"
	if got != want {
		t.Errorf("HashFixtureContent = %q, want %q", got, want)
	}
}
