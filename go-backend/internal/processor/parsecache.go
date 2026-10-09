package processor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/justrag/go-backend/internal/chat"
	"github.com/justrag/go-backend/internal/libpaths"
	"github.com/justrag/go-backend/internal/parser"
	"github.com/justrag/go-backend/internal/storage"
)

// parseCacheVersion is folded into ParseConfigHash. Bump it whenever parser or
// page-rebuild code changes its output, so stale cache entries are never read.
const parseCacheVersion = 1

// parseConfigKeys are the global site_config keys that influence a document
// parse: every docling_* option read per request (readDoclingOptions in
// internal/app/worker.go). docling_enabled is deliberately absent: the parser
// chain is built once at startup, so it is covered by the startup-snapshot
// parserIdentity instead. describe_image_model is
// resolved separately through the fast-tier chain.
var parseConfigKeys = []string{
	"docling_picture_description_enabled",
	"docling_picture_description_prompt",
	"docling_picture_description_timeout_seconds",
	"docling_picture_area_threshold",
	"docling_ocr_languages",
	"docling_force_ocr",
	"docling_formula_enrichment_enabled",
	"docling_document_timeout_seconds",
	"docling_table_mode",
}

// ParseCache stores serialized parser.ParseResult objects in object storage
// (P2-R1: existence of the object is the cache check; no table).
type ParseCache struct {
	stor storage.Storage
	// identity describes the parser chain as built at worker startup, e.g.
	// "docling:<base_url>" or "builtin". The chain is fixed for the process
	// lifetime, so it is snapshotted rather than read live from site_config.
	identity string
}

// NewParseCache wraps stor. parserIdentity must reflect the parser chain
// actually installed at startup ("docling:<base_url>" or "builtin").
func NewParseCache(stor storage.Storage, parserIdentity string) *ParseCache {
	return &ParseCache{stor: stor, identity: parserIdentity}
}

// Identity returns the parser-chain identity this cache was built with.
func (c *ParseCache) Identity() string {
	if c == nil {
		return ""
	}
	return c.identity
}

// ParseCacheKey returns users/<ownerID>/parses/<userFileID>/<configHash>.json.
func ParseCacheKey(ownerID, userFileID, configHash string) string {
	return libpaths.ParseCacheKey(ownerID, userFileID, configHash)
}

// ParseCacheDir returns users/<ownerID>/parses/<userFileID>/ (for deletion).
func ParseCacheDir(ownerID, userFileID string) string {
	return libpaths.ParseCacheDir(ownerID, userFileID)
}

// Get reads a cached parse. A miss is (nil, false, nil).
func (c *ParseCache) Get(ctx context.Context, key string) (*parser.ParseResult, bool, error) {
	if c == nil || c.stor == nil {
		return nil, false, nil
	}
	ok, err := c.stor.FileExists(ctx, key)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	raw, err := c.stor.ReadFile(ctx, key)
	if err != nil {
		return nil, false, err
	}
	var r parser.ParseResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, false, fmt.Errorf("parsecache: decode %s: %w", key, err)
	}
	return &r, true, nil
}

// Put stores a parse result.
func (c *ParseCache) Put(ctx context.Context, key string, r *parser.ParseResult) error {
	if c == nil || c.stor == nil {
		return errors.New("parsecache: no storage")
	}
	if r == nil {
		return errors.New("parsecache: nil result")
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return c.stor.StoreFile(ctx, key, raw, "application/json")
}

// ParseConfigHash hashes every parse-affecting global setting (P2-R2): the
// docling_* keys, the startup parser identity, the resolved
// describe_image_model and parseCacheVersion. Sorted key=value lines, sha256
// hex.
func ParseConfigHash(ctx context.Context, reader SiteConfigReader, parserIdentity string) string {
	lines := make([]string, 0, len(parseConfigKeys)+2)
	for _, k := range parseConfigKeys {
		v := ""
		if reader != nil {
			if p, err := reader.GetSiteConfigValue(ctx, k); err == nil && p != nil {
				v = strings.TrimSpace(*p)
			}
		}
		lines = append(lines, k+"="+v)
	}
	model := ""
	if reader != nil {
		model = chat.DescribeImageModel(ctx, reader)
	}
	lines = append(lines, "describe_image_model="+model)
	lines = append(lines, "parser_identity="+parserIdentity)
	lines = append(lines, fmt.Sprintf("parse_cache_version=%d", parseCacheVersion))
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

// parseCacheable reports whether a file may use the parse cache: not a
// spreadsheet (its parse is the tabular ingest), not an image (vision) and not
// audio (STT) — the last two depend on the KB's AI provider.
func parseCacheable(mimeType, fileName string) bool {
	if (&parser.SpreadsheetParser{}).CanParse(mimeType, fileName) ||
		(&parser.ImageParser{}).CanParse(mimeType, fileName) ||
		(&parser.AudioParser{}).CanParse(mimeType, fileName) {
		return false
	}
	return true
}
