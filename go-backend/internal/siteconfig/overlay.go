package siteconfig

import "context"

// baseReader is the minimal per-key reader the overlay wraps (the global
// site_config store). chat.PGStore and any SiteConfigReader satisfy it.
type baseReader interface {
	GetSiteConfigValue(ctx context.Context, key string) (*string, error)
}

// KBOverlayReader resolves a per-KB override for registry keys and delegates
// everything else to the global base reader. Resolution order for a registry
// key: kb override (if present) → global. Non-registry keys always hit global,
// so a malformed override map can never influence operational/security keys.
//
// It implements both the per-key reader contract and BatchReader, so the
// vector search hot path (which type-asserts to BatchReader) still does a
// single round-trip while seeing per-KB values.
type KBOverlayReader struct {
	base      baseReader
	overrides map[string]*string // only registry keys are consulted
}

// Compile-time guard: the overlay must satisfy BatchReader so the vector
// search hot path (which type-asserts to BatchReader) keeps its single
// round-trip while seeing per-KB values. If BatchReader's signature drifts,
// this fails to compile rather than silently degrading to N per-key reads.
var _ BatchReader = (*KBOverlayReader)(nil)

// NewKBOverlay builds an overlay over base. overrides is the KB's
// kb_site_configs map; nil is treated as empty.
func NewKBOverlay(base baseReader, overrides map[string]*string) *KBOverlayReader {
	if overrides == nil {
		overrides = map[string]*string{}
	}
	return &KBOverlayReader{base: base, overrides: overrides}
}

// GetSiteConfigValue returns the per-KB override for registry keys when set,
// else the global value.
func (r *KBOverlayReader) GetSiteConfigValue(ctx context.Context, key string) (*string, error) {
	if IsPerKB(key) {
		if v, ok := r.overrides[key]; ok {
			return v, nil
		}
	}
	return r.base.GetSiteConfigValue(ctx, key)
}

// GetSiteConfigValues fetches the global batch in one round-trip (when the base
// supports it) then applies per-KB overrides on registry keys.
func (r *KBOverlayReader) GetSiteConfigValues(ctx context.Context, keys []string) (map[string]*string, error) {
	out := make(map[string]*string, len(keys))
	if br, ok := r.base.(BatchReader); ok {
		base, err := br.GetSiteConfigValues(ctx, keys)
		if err != nil {
			return nil, err
		}
		for k, v := range base {
			out[k] = v
		}
	} else {
		for _, k := range keys {
			v, err := r.base.GetSiteConfigValue(ctx, k)
			if err != nil {
				return nil, err
			}
			out[k] = v
		}
	}
	for _, k := range keys {
		if IsPerKB(k) {
			if v, ok := r.overrides[k]; ok {
				out[k] = v
			}
		}
	}
	return out, nil
}
