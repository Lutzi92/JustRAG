package eval

import (
	"context"

	"github.com/justrag/go-backend/internal/chat"
)

// SnapshotConfigReader implements the site-config reader interface expected by
// chat.PrepareChatContext and its dependencies, returning values from an
// in-memory snapshot captured at run dispatch. This ensures mid-run admin
// edits do not taint evaluation results.
type SnapshotConfigReader struct {
	values map[string]*string
}

// NewSnapshotConfigReader wraps a values map. A nil map is treated as empty.
func NewSnapshotConfigReader(values map[string]*string) *SnapshotConfigReader {
	if values == nil {
		values = map[string]*string{}
	}
	return &SnapshotConfigReader{values: values}
}

// GetSiteConfigValue returns the frozen value for key, or nil if absent.
// Matches the existing reader contract: a nil return means "not set" so
// callers fall through to defaults.
func (s *SnapshotConfigReader) GetSiteConfigValue(_ context.Context, key string) (*string, error) {
	v, ok := s.values[key]
	if !ok {
		return nil, nil
	}
	return v, nil
}

// Compile-time assertion that SnapshotConfigReader satisfies chat.SiteConfigReader.
var _ chat.SiteConfigReader = (*SnapshotConfigReader)(nil)
