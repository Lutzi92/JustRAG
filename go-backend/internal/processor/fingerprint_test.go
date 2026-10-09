package processor

import (
	"context"
	"testing"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/parser"
	"github.com/justrag/go-backend/internal/siteconfig"
)

// fpConfigStore is an ai.ConfigStore with one active provider and one
// embedding model, both adjustable per test.
type fpConfigStore struct {
	providerID string
	model      string
	dims       int
	chatModel  string
}

func (s fpConfigStore) GetActiveAIProvider(context.Context) (*ai.AIProviderInfo, error) {
	return &ai.AIProviderInfo{ID: s.providerID, Name: "p", BaseURL: "http://x"}, nil
}

func (s fpConfigStore) GetAIProviderByID(context.Context, string) (*ai.AIProviderInfo, error) {
	return nil, nil
}

func (s fpConfigStore) GetAIModelsByProvider(context.Context, string) ([]ai.AIModelInfo, error) {
	// First chat model = the resolved KB ChatModel; the named extras are
	// models the provider offers (so an explicit model key can resolve).
	first := "chat-m"
	if s.chatModel != "" {
		first = s.chatModel
	}
	out := []ai.AIModelInfo{{Name: first}}
	for _, n := range []string{"chat-m", "e1", "e2", "h1", "h2", "k1", "k2"} {
		if n != first {
			out = append(out, ai.AIModelInfo{Name: n})
		}
	}
	return append(out, ai.AIModelInfo{Name: s.model, IsEmbedding: true, Dimensions: s.dims}), nil
}

func (s fpConfigStore) GetKBModelOverrides(context.Context, string) (*ai.KBModelOverrides, error) {
	return nil, nil
}

func fpProcessor(cs fpConfigStore, cfg map[string]string, st ProcessorStore) *Processor {
	vals := map[string]*string{}
	for k, v := range cfg {
		vals[k] = strPtr(v)
	}
	p := NewProcessor(parser.DefaultFactoryWith(nil), ai.NewConfigResolver(cs), nil, st)
	p.SetSiteConfigReader(&fakeSiteConfigReader{values: vals})
	return p
}

func fpBase() fpConfigStore { return fpConfigStore{providerID: "prov-1", model: "emb-a", dims: 1024} }

func fingerprintOf(t *testing.T, cs fpConfigStore, cfg map[string]string, size, overlap int) string {
	t.Helper()
	fp, err := fpProcessor(cs, cfg, &mockStore{}).IndexFingerprint(context.Background(), "kb-1", size, overlap)
	if err != nil {
		t.Fatal(err)
	}
	return fp
}

func TestIndexFingerprint_Sensitivity(t *testing.T) {
	base := fpBase()
	on := map[string]string{
		"contextual_enrichment": "true", "contextual_enrichment_model": "e1",
		"hype_enabled": "true", "hype_model": "h1",
		"raptor_enabled": "true", "kg_extraction_enabled": "true", "kg_extraction_model": "k1",
	}
	with := func(over map[string]string) map[string]string {
		m := map[string]string{}
		for k, v := range on {
			m[k] = v
		}
		for k, v := range over {
			m[k] = v
		}
		return m
	}
	tests := []struct {
		name     string
		cs       fpConfigStore
		cfg      map[string]string
		size, ov int
	}{
		{"embedding model", fpConfigStore{providerID: "prov-1", model: "emb-b", dims: 1024}, on, 512, 100},
		{"dims", fpConfigStore{providerID: "prov-1", model: "emb-a", dims: 2048}, on, 512, 100},
		{"provider", fpConfigStore{providerID: "prov-2", model: "emb-a", dims: 1024}, on, 512, 100},
		{"chunk size", base, on, 256, 100},
		{"chunk overlap", base, on, 512, 50},
		{"parent_child on", base, with(map[string]string{"parent_child_enabled": "true"}), 512, 100},
		{"parent size (pc on)", base, with(map[string]string{"parent_child_enabled": "true", "parent_chunk_size": "3000"}), 512, 100},
		{"enrichment off", base, with(map[string]string{"contextual_enrichment": "false"}), 512, 100},
		{"enrichment model", base, with(map[string]string{"contextual_enrichment_model": "e2"}), 512, 100},
		{"late chunking", base, with(map[string]string{"late_chunking_enabled": "true"}), 512, 100},
		{"late chunking tokens", base, with(map[string]string{"late_chunking_enabled": "true", "late_chunking_max_input_tokens": "4096"}), 512, 100},
		{"hype off", base, with(map[string]string{"hype_enabled": "false"}), 512, 100},
		{"hype model", base, with(map[string]string{"hype_model": "h2"}), 512, 100},
		{"raptor off", base, with(map[string]string{"raptor_enabled": "false"}), 512, 100},
		{"raptor max levels (on)", base, with(map[string]string{"raptor_max_levels": "7"}), 512, 100},
		{"kg off", base, with(map[string]string{"kg_extraction_enabled": "false"}), 512, 100},
		{"kg model", base, with(map[string]string{"kg_extraction_model": "k2"}), 512, 100},
		{"parse config", base, with(map[string]string{"docling_force_ocr": "true"}), 512, 100},
	}
	ref := fingerprintOf(t, base, on, 512, 100)
	if ref != fingerprintOf(t, base, on, 512, 100) {
		t.Fatal("fingerprint is not deterministic")
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := fingerprintOf(t, tc.cs, tc.cfg, tc.size, tc.ov); got == ref {
				t.Errorf("fingerprint unchanged by %s", tc.name)
			}
		})
	}
}

func TestIndexFingerprint_Insensitivity(t *testing.T) {
	base := fpBase()
	off := map[string]string{"contextual_enrichment": "false"} // kg/raptor/hype default off
	ref := fingerprintOf(t, base, off, 512, 100)
	for _, tc := range []struct{ key, val string }{
		{"kg_extraction_model", "other"},       // KG off
		{"raptor_max_levels", "9"},             // RAPTOR off
		{"hype_model", "other"},                // HyPE off
		{"contextual_enrichment_model", "oth"}, // enrichment off
		{"late_chunking_max_input_tokens", "1"},
		{"parent_chunk_size", "9999"},
		{"embedding_batch_size", "7"},
		{"ingest_enrich_concurrency", "3"},
		{"kg_extraction_concurrency", "9"},
		{"ingest_screening_enabled", "false"},
		{"tabular_large_file_concurrency", "4"},
	} {
		cfg := map[string]string{tc.key: tc.val}
		for k, v := range off {
			cfg[k] = v
		}
		if got := fingerprintOf(t, base, cfg, 512, 100); got != ref {
			t.Errorf("%s changed the fingerprint", tc.key)
		}
	}
}

// Drift guard: every per-KB RequiresReingest registry key must feed the
// fingerprint or be explicitly excluded with a reason.
func TestIndexFingerprint_DriftGuard(t *testing.T) {
	// Everything on, so every conditional sub-key is present too.
	allOn := map[string]string{
		"parent_child_enabled": "true", "contextual_enrichment": "true",
		"late_chunking_enabled": "true", "hype_enabled": "true",
		"raptor_enabled": "true", "kg_extraction_enabled": "true",
	}
	in, err := fpProcessor(fpBase(), allOn, &mockStore{}).IndexFingerprintInputs(context.Background(), "kb-1", 512, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range siteconfig.All() {
		if !f.RequiresReingest {
			continue
		}
		_, inFP := in[f.Key]
		reason, excluded := fingerprintExcluded[f.Key]
		if !inFP && !excluded {
			t.Errorf("RequiresReingest key %q is neither a fingerprint input nor in fingerprintExcluded", f.Key)
		}
		if inFP && excluded {
			t.Errorf("key %q is both a fingerprint input and excluded", f.Key)
		}
		if excluded && reason == "" {
			t.Errorf("excluded key %q has no reason", f.Key)
		}
	}
	for k := range fingerprintExcluded {
		if fld, ok := siteconfig.Field(k); !ok || !fld.RequiresReingest {
			t.Errorf("fingerprintExcluded lists %q which is not a RequiresReingest registry key", k)
		}
	}
}

func fpInput(userFileID string) ProcessFileInput {
	return ProcessFileInput{FileID: "f1", FileName: "doc.txt", MimeType: "text/plain", KBID: "kb-1",
		UserFileID: userFileID, OwnerUserID: "o1"}
}

func TestRecordIndexFingerprint(t *testing.T) {
	ctx := withIngestDegradedFlag(context.Background())
	t.Run("library file writes the snapshot", func(t *testing.T) {
		st := &mockStore{}
		p := fpProcessor(fpBase(), nil, st)
		snap := p.snapshotIndexFingerprint(ctx, fpInput("uf-1"))
		if snap == "" {
			t.Fatal("empty snapshot")
		}
		p.recordIndexFingerprint(ctx, fpInput("uf-1"), snap)
		if st.fingerprints["f1"] != snap {
			t.Fatal("snapshot not written")
		}
	})
	t.Run("non-library skips", func(t *testing.T) {
		st := &mockStore{}
		p := fpProcessor(fpBase(), nil, st)
		if p.snapshotIndexFingerprint(ctx, fpInput("")) != "" {
			t.Fatal("snapshot for non-library file")
		}
		p.recordIndexFingerprint(ctx, fpInput(""), "x")
		if len(st.fingerprints) != 0 {
			t.Fatal("fingerprint written for non-library file")
		}
	})
	t.Run("snapshot failure means no record", func(t *testing.T) {
		st := &mockStore{}
		p := NewProcessor(parser.DefaultFactoryWith(nil), ai.NewConfigResolver(noProviderConfigStore{}), nil, st)
		snap := p.snapshotIndexFingerprint(ctx, fpInput("uf-1"))
		if snap != "" {
			t.Fatal("snapshot despite resolver failure")
		}
		p.recordIndexFingerprint(ctx, fpInput("uf-1"), snap)
		if len(st.fingerprints) != 0 {
			t.Fatal("fingerprint written without snapshot")
		}
	})
	t.Run("degraded run writes none", func(t *testing.T) {
		st := &mockStore{}
		p := fpProcessor(fpBase(), nil, st)
		dctx := withIngestDegradedFlag(context.Background())
		markIngestDegraded(dctx)
		p.recordIndexFingerprint(dctx, fpInput("uf-1"), "snap")
		if len(st.fingerprints) != 0 {
			t.Fatal("fingerprint written for a degraded run")
		}
	})
}

// The effective LLM model (fast-tier chain empty -> the KB's chat model) is
// part of the fingerprint for every enabled LLM stage.
func TestIndexFingerprint_EffectiveModel(t *testing.T) {
	a := fpConfigStore{providerID: "prov-1", model: "emb-a", dims: 1024, chatModel: "chat-1"}
	b := a
	b.chatModel = "chat-2"
	for name, tc := range map[string]struct {
		cfg     map[string]string
		changes bool
	}{
		"enrichment on, no model keys": {map[string]string{"contextual_enrichment": "true"}, true},
		"hype on, no model keys":       {map[string]string{"contextual_enrichment": "false", "hype_enabled": "true"}, true},
		"raptor on, no model keys":     {map[string]string{"contextual_enrichment": "false", "raptor_enabled": "true"}, true},
		"kg on, no model keys":         {map[string]string{"contextual_enrichment": "false", "kg_extraction_enabled": "true"}, true},
		"all LLM stages off":           {map[string]string{"contextual_enrichment": "false"}, false},
	} {
		t.Run(name, func(t *testing.T) {
			same := fingerprintOf(t, a, tc.cfg, 512, 100) == fingerprintOf(t, b, tc.cfg, 512, 100)
			if same == tc.changes {
				t.Errorf("KB chat model change: fingerprint equal = %v, want %v", same, !tc.changes)
			}
		})
	}
	// An explicit model that the provider offers wins over the KB chat model.
	cfg := map[string]string{"contextual_enrichment": "true", "contextual_enrichment_model": "chat-m"}
	if fingerprintOf(t, a, cfg, 512, 100) != fingerprintOf(t, b, cfg, 512, 100) {
		t.Error("explicit available model should be independent of the KB chat model")
	}
}

// End to end through ProcessFile: only a completed run writes it.
func TestProcessFile_FingerprintOnlyOnCompleted(t *testing.T) {
	// An empty document completes with "no chunks produced" and never needs
	// the embedder.
	for _, tc := range []struct {
		name       string
		userFileID string
		want       bool
	}{
		{"library completed", "uf-1", true},
		{"non-library completed", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := &mockStore{}
			p := fpProcessor(fpBase(), nil, st)
			in := fpInput(tc.userFileID)
			in.FilePath = writeTempText(t, "   ")
			if err := p.ProcessFile(context.Background(), in); err != nil {
				t.Fatalf("ProcessFile: %v", err)
			}
			if got := st.fingerprints["f1"] != ""; got != tc.want {
				t.Errorf("fingerprint written = %v, want %v (statuses %v)", got, tc.want, st.statuses)
			}
		})
	}
}
