package chat

import (
	"context"

	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/siteconfig"
	"github.com/justrag/go-backend/internal/vector"
)

// KBConfigOverrideLister loads a KB's per-KB site_config overrides. Satisfied
// by *kbconfig.Store. Defined here (not imported) so the chat package does not
// depend on internal/kbconfig — app wiring injects the concrete store.
type KBConfigOverrideLister interface {
	ListKBOverrides(ctx context.Context, kbID string) (map[string]*string, error)
}

// forKB returns a request-local copy of the handler whose site-config reader
// and SearchService are overlaid with the KB's per-KB overrides. When the
// handler has no kbConfigStore, the load fails, or the KB has no overrides, it
// returns the receiver unchanged so the common path keeps using the shared
// singletons at zero cost.
//
// The returned *Handler is a shallow copy: Handler holds only interfaces and
// pointers (no inline sync primitives), so copying is safe and the original is
// never mutated — avoiding the data race that swapping the shared reader in
// place would cause.
func (h *Handler) forKB(ctx context.Context, kbID string) *Handler {
	if h.kbConfigStore == nil {
		return h
	}
	overrides, err := h.kbConfigStore.ListKBOverrides(ctx, kbID)
	if err != nil {
		logctx.From(ctx).Warn("chat.kb_config.load_failed", "kb_id", kbID, "error", err)
		return h
	}
	if len(overrides) == 0 {
		return h
	}

	overlay := siteconfig.NewKBOverlay(h.siteConfigReader, overrides)
	hc := *h // shallow copy — request-local
	hc.siteConfigReader = overlay
	if ss, ok := h.searchService.(*vector.SearchService); ok {
		hc.searchService = ss.CloneWithSiteConfigReader(overlay)
	}
	logctx.From(ctx).Debug("chat.kb_config.applied", "kb_id", kbID, "override_count", len(overrides))
	return &hc
}
