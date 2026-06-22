package splitter

import (
	_ "embed"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/pkoukk/tiktoken-go"
)

// cl100kBaseVocab is the cl100k_base BPE vocabulary, embedded so tokenization
// never depends on a network fetch. Upstream tiktoken-go downloads this file
// from openaipublic.blob.core.windows.net on first use and falls back to a
// crude char estimate when the host is unreachable — which silently degrades
// every cl100k-based signal (dynamic-alpha token rarity, embedding/context
// token budgets) in air-gapped or proxy-restricted deployments, and makes the
// test suite non-hermetic off-network. Embedding the vocab makes tokenization
// deterministic and offline-safe in both prod and CI.
//
// SHA-256 223921b76ee99bde995b7ff738513eef100fb51d18c93597a113bcffe865b2a7
// (the canonical OpenAI cl100k_base.tiktoken, 100256 merge entries).
//
//go:embed assets/cl100k_base.tiktoken
var cl100kBaseVocab []byte

// cl100kBlobPath is the URL tiktoken-go uses as the cache/lookup key for the
// cl100k_base encoding (see encoding.go in pkoukk/tiktoken-go). We match on it
// to serve the embedded copy and delegate any other encoding to the default
// (network) loader, preserving upstream behaviour for vocabularies we don't ship.
const cl100kBlobPath = "https://openaipublic.blob.core.windows.net/encodings/cl100k_base.tiktoken"

// offlineBpeLoader serves the embedded cl100k_base vocabulary without any
// network access, delegating other encodings to tiktoken-go's default loader.
type offlineBpeLoader struct {
	fallback tiktoken.BpeLoader
}

func (l *offlineBpeLoader) LoadTiktokenBpe(tiktokenBpeFile string) (map[string]int, error) {
	if tiktokenBpeFile == cl100kBlobPath {
		return parseTiktokenBpe(cl100kBaseVocab)
	}
	return l.fallback.LoadTiktokenBpe(tiktokenBpeFile)
}

// parseTiktokenBpe parses the "base64(token) rank" line format used by the
// .tiktoken vocabulary files (mirrors loadTiktokenBpe in pkoukk/tiktoken-go).
func parseTiktokenBpe(contents []byte) (map[string]int, error) {
	ranks := make(map[string]int, 100256)
	for line := range strings.SplitSeq(string(contents), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("malformed tiktoken bpe line: %q", line)
		}
		token, err := base64.StdEncoding.DecodeString(parts[0])
		if err != nil {
			return nil, fmt.Errorf("decode tiktoken token %q: %w", parts[0], err)
		}
		rank, err := strconv.Atoi(parts[1])
		if err != nil {
			return nil, fmt.Errorf("parse tiktoken rank %q: %w", parts[1], err)
		}
		ranks[string(token)] = rank
	}
	return ranks, nil
}
