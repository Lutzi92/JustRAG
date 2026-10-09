package processor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/justrag/go-backend/internal/chat"
	"github.com/justrag/go-backend/internal/logctx"
)

// fingerprintVersion is bumped whenever the fingerprint's input set or
// encoding changes, so old fingerprints never match new ones.
const fingerprintVersion = 1

// FingerprintInputs is the resolved, ordered input set (exported for tests/logs).
type FingerprintInputs map[string]string

// Drift-guard scope: the guard test covers per-KB registry keys only
// (siteconfig.All with RequiresReingest). Global-only ingest keys that shape
// the index (parse options, late_chunking_*, hype_*, embedding identity) are
// enumerated by hand in IndexFingerprintInputs; add new ones there.

// fingerprintExcluded lists per-KB RequiresReingest registry keys that are
// deliberately NOT part of the index fingerprint, with the reason. The drift
// guard test requires every such registry key to be either a fingerprint
// input or listed here.
var fingerprintExcluded = map[string]string{
	"tabular_profile_llm_enabled":        "spreadsheets are never copied (P2-R3)",
	"tabular_profile_llm_threshold":      "spreadsheets are never copied (P2-R3)",
	"tabular_profile_model":              "spreadsheets are never copied (P2-R3)",
	"tabular_profile_sample_rows":        "spreadsheets are never copied (P2-R3)",
	"tabular_max_rows":                   "spreadsheets are never copied (P2-R3)",
	"tabular_embed_max_rows":             "spreadsheets are never copied (P2-R3)",
	"tabular_column_values_max_distinct": "spreadsheets are never copied (P2-R3)",
}

// IndexFingerprintInputs resolves every P2-R4 input for kbID through the
// same per-KB overlay ProcessFile uses (withKBConfig), the AI resolver
// (ProviderID/EmbeddingModel/EmbeddingDimensions), KB chunk config +
// language. Conditional sub-keys are included only when their parent flag is
// on (so toggling an inert sub-setting does not break reuse).
func (p *Processor) IndexFingerprintInputs(ctx context.Context, kbID string, chunkSize, chunkOverlap int) (FingerprintInputs, error) {
	if p.aiResolver == nil {
		return nil, errors.New("processor: fingerprint: no AI resolver")
	}
	p = p.withKBConfig(ctx, kbID)
	rc, err := p.aiResolver.Resolve(ctx, kbID)
	if err != nil {
		return nil, fmt.Errorf("processor: fingerprint: resolve embedding config: %w", err)
	}
	rawLang, _ := p.resolveKBLanguages(ctx, kbID)
	r := p.siteConfigReader

	in := FingerprintInputs{
		"fingerprint_version":  strconv.Itoa(fingerprintVersion),
		"embedding_provider":   rc.ProviderID,
		"embedding_base_url":   rc.BaseURL,
		"embedding_model":      rc.EmbeddingModel,
		"embedding_dimensions": strconv.Itoa(rc.EmbeddingDimensions),
		"chunk_size":           strconv.Itoa(chunkSize),
		"chunk_overlap":        strconv.Itoa(chunkOverlap),
		"language":             rawLang,
		"parse_config":         ParseConfigHash(ctx, r, p.parseCache.Identity()),
	}
	b := strconv.FormatBool

	pc := chat.ParentChildEnabled(ctx, r)
	in["parent_child_enabled"] = b(pc)
	if pc {
		in["parent_chunk_size"] = strconv.Itoa(chat.ParentChildParentChunkSize(ctx, r))
		in["child_chunk_size"] = strconv.Itoa(chat.ParentChildChildChunkSize(ctx, r))
	}
	enrich := resolveEnrichmentEnabled(ctx, r)
	in["contextual_enrichment"] = b(enrich)
	if enrich {
		in["contextual_enrichment_model"] = rc.EffectiveChatModel(resolveEnrichmentModel(ctx, r))
	}
	late := resolveLateChunkingEnabled(ctx, r)
	in["late_chunking_enabled"] = b(late)
	if late {
		in["late_chunking_max_input_tokens"] = strconv.Itoa(resolveLateChunkingMaxInputTokens(ctx, r))
	}
	hype := resolveHyPEEnabled(ctx, r)
	in["hype_enabled"] = b(hype)
	if hype {
		in["hype_model"] = rc.EffectiveChatModel(resolveHyPEModel(ctx, r))
		in["hype_questions_per_chunk"] = strconv.Itoa(chat.HyPEQuestionsPerChunk(ctx, r))
	}
	rap := chat.RaptorEnabled(ctx, r)
	in["raptor_enabled"] = b(rap)
	if rap {
		in["raptor_min_chunks"] = strconv.Itoa(chat.RaptorMinChunks(ctx, r))
		in["raptor_max_levels"] = strconv.Itoa(chat.RaptorMaxLevels(ctx, r))
		in["raptor_branching_factor"] = strconv.Itoa(chat.RaptorBranchingFactor(ctx, r))
		in["raptor_summary_model"] = rc.EffectiveChatModel(chat.RaptorSummaryModel(ctx, r))
		in["raptor_clustering_algorithm"] = chat.RaptorClusteringAlgorithm(ctx, r)
		in["raptor_leiden_resolution"] = strconv.FormatFloat(chat.RaptorLeidenResolution(ctx, r), 'g', -1, 64)
	}
	kgOn := resolveKGExtractionEnabled(ctx, r)
	in["kg_extraction_enabled"] = b(kgOn)
	if kgOn {
		in["kg_extraction_model"] = rc.EffectiveChatModel(resolveKGExtractionModel(ctx, r))
	}
	return in, nil
}

// IndexFingerprint = sha256 hex over sorted "k=v\n" lines of the inputs.
func (p *Processor) IndexFingerprint(ctx context.Context, kbID string, chunkSize, chunkOverlap int) (string, error) {
	in, err := p.IndexFingerprintInputs(ctx, kbID, chunkSize, chunkOverlap)
	if err != nil {
		return "", err
	}
	lines := make([]string, 0, len(in))
	for k, v := range in {
		lines = append(lines, k+"="+v+"\n")
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "")))
	return hex.EncodeToString(sum[:]), nil
}

// ingestDegradedKey carries a per-run flag in the context. Per-run state
// cannot live on the Processor (shared across concurrent ingests).
type ingestDegradedKey struct{}

func withIngestDegradedFlag(ctx context.Context) context.Context {
	return context.WithValue(ctx, ingestDegradedKey{}, new(atomic.Bool))
}

// markIngestDegraded notes that this run produced a weaker index than its
// config promises (a chunk stored without its enrichment prefix, a failed
// HyPE/RAPTOR stage). A degraded run must not become a copy donor.
func markIngestDegraded(ctx context.Context) {
	if f, ok := ctx.Value(ingestDegradedKey{}).(*atomic.Bool); ok {
		f.Store(true)
	}
}

func ingestDegraded(ctx context.Context) bool {
	f, ok := ctx.Value(ingestDegradedKey{}).(*atomic.Bool)
	return ok && f.Load()
}

// snapshotIndexFingerprint computes the fingerprint at the start of a library
// file's ingest. "" (non-library file or computation failure) means "do not
// record".
func (p *Processor) snapshotIndexFingerprint(ctx context.Context, in ProcessFileInput) string {
	if in.UserFileID == "" {
		return ""
	}
	fp, err := p.IndexFingerprint(ctx, in.KBID, in.ChunkSize, in.ChunkOverlap)
	if err != nil {
		logctx.From(ctx).Warn("processor: index fingerprint not computed",
			"fileId", in.FileID, "error", err)
		return ""
	}
	return fp
}

// recordIndexFingerprint stores the start-of-run snapshot once the index is
// fully built. Best-effort: an empty snapshot, a degraded run or a store
// failure leaves files.index_fingerprint NULL (the file is simply not a copy
// donor). Callers invoke it only for status "completed", and at the very end
// of the path (after the HyPE/RAPTOR tail on the flat path).
func (p *Processor) recordIndexFingerprint(ctx context.Context, in ProcessFileInput, snapshot string) {
	if snapshot == "" || in.UserFileID == "" {
		return
	}
	if ingestDegraded(ctx) {
		logctx.From(ctx).Info("processor: degraded ingest, index fingerprint not recorded", "fileId", in.FileID)
		return
	}
	if err := p.store.SetIndexFingerprint(ctx, in.FileID, snapshot); err != nil {
		logctx.From(ctx).Warn("processor: index fingerprint not recorded",
			"fileId", in.FileID, "error", err)
	}
}
