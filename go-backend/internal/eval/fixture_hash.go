package eval

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

// HashFixture returns the lowercase hex sha256 of the file at path. Used to
// stamp each eval run so runs using the same fixture can be safely compared.
func HashFixture(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open fixture %q: %w", path, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash fixture %q: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// HashFixtureContent returns the lowercase hex sha256 of the given bytes.
// Used when fixture content is in-memory (uploaded via HTTP) rather than on disk.
func HashFixtureContent(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
