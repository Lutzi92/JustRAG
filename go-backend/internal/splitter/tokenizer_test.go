package splitter

import "testing"

func TestCountTokens_NonEmpty(t *testing.T) {
	t.Parallel()
	count := CountTokens("Hello, world! This is a test.")
	if count <= 0 {
		t.Errorf("expected positive token count, got %d", count)
	}
	if count >= 20 {
		t.Errorf("expected fewer than 20 tokens for short string, got %d", count)
	}
	t.Logf("token count for test string: %d", count)
}

func TestCountTokens_Empty(t *testing.T) {
	t.Parallel()
	count := CountTokens("")
	if count != 0 {
		t.Errorf("expected 0 for empty string, got %d", count)
	}
}

func TestCountTokens_BasicSentence(t *testing.T) {
	t.Parallel()
	count := CountTokens("The quick brown fox jumps over the lazy dog.")
	if count <= 0 {
		t.Errorf("expected positive token count, got %d", count)
	}
	t.Logf("token count for basic sentence: %d", count)
}
