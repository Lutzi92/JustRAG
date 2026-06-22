package splitter

import (
	"log/slog"
	"sync"
	"unicode/utf8"

	"github.com/pkoukk/tiktoken-go"
)

// getTokenizer lazily initializes the shared tiktoken encoder exactly once.
// Returns nil when initialization failed — callers fall back to char-based
// estimation (CountTokens) or "no signal" (EncodeBPE).
var getTokenizer = sync.OnceValue(func() *tiktoken.Tiktoken {
	// Serve the cl100k_base vocab from the embedded copy so initialization
	// never reaches the network (see offline_tokenizer.go). Registered here,
	// inside the Once, so it is in place before EncodingForModel resolves and
	// the SetBpeLoader global write is synchronized with the encoder read.
	tiktoken.SetBpeLoader(&offlineBpeLoader{fallback: tiktoken.NewDefaultBpeLoader()})
	enc, err := tiktoken.EncodingForModel("gpt-4")
	if err != nil {
		slog.Warn("failed to initialize tiktoken tokenizer, falling back to char estimation", "error", err)
		return nil
	}
	return enc
})

// CountTokens returns the token count for text using tiktoken (cl100k_base).
// Falls back to len(text)/4 if the tokenizer is unavailable.
func CountTokens(text string) int {
	if text == "" {
		return 0
	}
	if tok := getTokenizer(); tok != nil {
		return len(tok.Encode(text, nil, nil))
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
	tok := getTokenizer()
	if tok == nil {
		return nil
	}
	return tok.Encode(text, nil, nil)
}
