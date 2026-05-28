package splitter

import (
	"log/slog"
	"sync"
	"unicode/utf8"

	"github.com/pkoukk/tiktoken-go"
)

var (
	tokenizer     *tiktoken.Tiktoken
	tokenizerOnce sync.Once
)

func initTokenizer() {
	tokenizerOnce.Do(func() {
		enc, err := tiktoken.EncodingForModel("gpt-4")
		if err != nil {
			slog.Warn("failed to initialize tiktoken tokenizer, falling back to char estimation", "error", err)
			return
		}
		tokenizer = enc
	})
}

// CountTokens returns the token count for text using tiktoken (cl100k_base).
// Falls back to len(text)/4 if the tokenizer is unavailable.
func CountTokens(text string) int {
	if text == "" {
		return 0
	}
	initTokenizer()
	if tokenizer != nil {
		return len(tokenizer.Encode(text, nil, nil))
	}
	return utf8.RuneCountInString(text) / 4
}

// EncodeBPE returns the cl100k_base BPE token IDs for text. Empty text
// or an unavailable tokenizer returns nil — callers should treat that
// as "no signal" rather than special-casing. Used by callers that need
// the raw IDs (not just the count): the dynamic-alpha hybrid-search
// heuristic in internal/vector reads the ID distribution to estimate
// query-token rarity (lower IDs ≈ more common tokens in the BPE merge
// order, higher IDs ≈ rarer multilingual / domain-specific tokens).
func EncodeBPE(text string) []int {
	if text == "" {
		return nil
	}
	initTokenizer()
	if tokenizer == nil {
		return nil
	}
	return tokenizer.Encode(text, nil, nil)
}
