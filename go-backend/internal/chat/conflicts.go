package chat

import (
	"context"
	"sort"
	"time"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/prompts"
)

// conflictContentCap bounds one source body inside the detector prompt.
// With the default cap of 12 sources that is ~24k runes of document text,
// which sits comfortably inside a fast-tier context window while still
// carrying the sentence a contradiction actually lives in.
const conflictContentCap = 2000

// MessageConflict is one surfaced disagreement between two cited sources,
// as persisted on the AI message (messages.conflicts) and streamed to the
// frontend. SourceA/SourceB are the [N] citation indices of the turn's own
// source list; FileA/FileB are resolved once here so a client rendering a
// stored message does not have to re-join against the sources array.
type MessageConflict struct {
	Claim   string `json:"claim"`
	SourceA int    `json:"sourceA"`
	SourceB int    `json:"sourceB"`
	// Kind is "contradiction" or "superseded".
	Kind string `json:"kind"`
	// Newer is "a", "b" or "unknown".
	Newer string `json:"newer"`
	FileA string `json:"fileA"`
	FileB string `json:"fileB"`
}

// ConflictReport is the whole per-message conflict blob. A pointer to it is
// the JSONB value of messages.conflicts; nil means the detector did not run
// or found nothing, which is why the SSE frame and the FE badge key on
// len(Conflicts) rather than on the column being present.
type ConflictReport struct {
	Conflicts []MessageConflict `json:"conflicts"`
}

// ConflictConfig is the resolved knob set for conflict surfacing. Resolved
// once per turn from the reader in force for THIS KB, so the Supervisor
// path — which carries no SiteConfigReader, exactly like the
// sufficient-context gate and the tabular router — receives the same
// decision the standard path would make.
type ConflictConfig struct {
	Enabled   bool
	Model     string
	MaxChunks int
	Timeout   time.Duration
}

// ResolveConflictConfig reads one complete ConflictConfig. Single definition
// so the standard path, the Supervisor path and any future caller cannot
// disagree about what the four keys mean.
func ResolveConflictConfig(ctx context.Context, reader SiteConfigReader) ConflictConfig {
	return ConflictConfig{
		Enabled:   ChatConflictSurfacingEnabled(ctx, reader),
		Model:     ChatConflictModel(ctx, reader),
		MaxChunks: ChatConflictMaxChunks(ctx, reader),
		Timeout:   time.Duration(ChatConflictTimeoutMs(ctx, reader)) * time.Millisecond,
	}
}

// ConflictInput is everything DetectConflicts needs about one turn.
type ConflictInput struct {
	KbID     string
	Question string
	Language string
	// Sources is the turn's final source list, already numbered — index
	// alignment with the answer prompt's [N] markers is what makes a
	// returned SourceA/SourceB citable.
	Sources []ChatSource
	Config  ConflictConfig
	// FileDates resolves published_at/created_at for the files involved.
	// Optional: without it every date line reads "unknown" and the model
	// can only report contradictions, never supersession.
	FileDates FileDateLookup
	Emit      func(map[string]any)
	// detect is the injectable seam for ai.DetectSourceConflicts. Unexported
	// on purpose: production call sites leave it nil and get the real
	// detector, while in-package tests (including the Supervisor wiring
	// test, which reaches it through SupervisorChatParams.conflictDetect)
	// drive the pass without an LLM.
	detect detectSourceConflictsFn
}

// DetectConflicts runs the W5-R7 conflict / supersession pass over one
// turn's assembled source set and returns the report to attach to the
// answer (prompt addendum, persisted blob, SSE frame).
//
// Gate, in order:
//   - chat_conflict_surfacing_enabled (per-KB, default off);
//   - at least 2 DISTINCT file ids among the sources. A set of chunks from
//     one file is a single document talking to itself; flagging it as
//     "conflicting sources" would be a category error, and it is also the
//     common case on a small KB, so the gate keeps the extra call off the
//     majority of turns.
//
// Fail-soft everywhere: an LLM error, a parse failure or the
// chat_conflict_timeout_ms deadline all return nil — no addendum, no
// persisted blob, no badge — plus a trajectory event and a metric. The
// answer never waits longer than the configured timeout, and never fails
// because of this pass.
func DetectConflicts(ctx context.Context, resolver *ai.ConfigResolver, in ConflictInput) *ConflictReport {
	detect := in.detect
	if detect == nil {
		detect = ai.DetectSourceConflicts
	}
	return detectConflictsWith(ctx, detect, resolver, in)
}

// detectSourceConflictsFn is the injectable seam for ai.DetectSourceConflicts.
type detectSourceConflictsFn func(ctx context.Context, resolver *ai.ConfigResolver, kbID, question string, sources []ai.ConflictSource, lang, modelOverride string) (*ai.ConflictFindings, error)

func detectConflictsWith(ctx context.Context, detect detectSourceConflictsFn, resolver *ai.ConfigResolver, in ConflictInput) *ConflictReport {
	if !in.Config.Enabled {
		return nil
	}
	if distinctFileCount(in.Sources) < 2 {
		observability.RecordConflictSurfacing("skipped_single_file")
		return nil
	}

	// Re-check AFTER the cap: the full set can span two files while the
	// top-scoring MaxChunks of it all come from one, and it is the capped
	// list the model actually sees.
	picked := pickConflictSources(in.Sources, in.Config.MaxChunks)
	if distinctFileCount(picked) < 2 {
		observability.RecordConflictSurfacing("skipped_single_file")
		return nil
	}

	dates := conflictSourceDates(ctx, in.FileDates, picked)
	blocks := make([]ai.ConflictSource, len(picked))
	for i, s := range picked {
		blocks[i] = ai.ConflictSource{
			Idx:      s.Index,
			Name:     s.FileName,
			DateLine: renderConflictDateLine(in.Language, dates[s.FileID]),
			Content:  truncateRunes(s.Content, conflictContentCap),
		}
	}

	timeout := in.Config.Timeout
	if timeout <= 0 {
		timeout = time.Duration(defaultConflictTimeoutMs) * time.Millisecond
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	findings, err := detect(callCtx, resolver, in.KbID, in.Question, blocks, in.Language, in.Config.Model)
	if err != nil {
		outcome := "error"
		if callCtx.Err() != nil && ctx.Err() == nil {
			outcome = "timeout"
		}
		observability.RecordConflictSurfacing(outcome)
		emitConflictTrajectory(in.Emit, outcome, 0)
		logctx.From(ctx).Warn("rag.conflict_surfacing.failed",
			"error", err, "outcome", outcome, "kb_id", in.KbID, "sources", len(blocks))
		return nil
	}

	report := buildConflictReport(findings, picked)
	if report == nil {
		observability.RecordConflictSurfacing("none")
		emitConflictTrajectory(in.Emit, "none", 0)
		return nil
	}
	observability.RecordConflictSurfacing("found")
	emitConflictTrajectory(in.Emit, "found", len(report.Conflicts))
	logctx.From(ctx).Info("rag.conflict_surfacing.found",
		"conflicts", len(report.Conflicts), "sources", len(blocks), "kb_id", in.KbID)
	return report
}

// buildConflictReport resolves the model's source numbers back to file
// names. A row whose index is not in the picked set is dropped a second
// time here: ai.DetectSourceConflicts validated against the same numbering,
// so this is a belt-and-braces guard that also gives the name lookup a
// total function.
func buildConflictReport(f *ai.ConflictFindings, picked []ChatSource) *ConflictReport {
	if f == nil || len(f.Conflicts) == 0 {
		return nil
	}
	byIdx := make(map[int]string, len(picked))
	for _, s := range picked {
		byIdx[s.Index] = s.FileName
	}
	out := &ConflictReport{}
	for _, c := range f.Conflicts {
		nameA, okA := byIdx[c.SourceA]
		nameB, okB := byIdx[c.SourceB]
		if !okA || !okB {
			continue
		}
		out.Conflicts = append(out.Conflicts, MessageConflict{
			Claim:   c.Claim,
			SourceA: c.SourceA,
			SourceB: c.SourceB,
			Kind:    c.Kind,
			Newer:   c.Newer,
			FileA:   nameA,
			FileB:   nameB,
		})
	}
	if len(out.Conflicts) == 0 {
		return nil
	}
	return out
}

// ConflictAddendumText renders the report as the answer-prompt block, or ""
// when there is nothing to say. Nil-safe so call sites can append
// unconditionally.
func ConflictAddendumText(lang string, report *ConflictReport) string {
	if report == nil || len(report.Conflicts) == 0 {
		return ""
	}
	entries := make([]prompts.ConflictAddendumEntry, len(report.Conflicts))
	for i, c := range report.Conflicts {
		entries[i] = prompts.ConflictAddendumEntry{
			Claim: c.Claim,
			IdxA:  c.SourceA,
			IdxB:  c.SourceB,
			FileA: c.FileA,
			FileB: c.FileB,
			Kind:  c.Kind,
			Newer: c.Newer,
		}
	}
	return prompts.ConflictAddendum(lang, entries)
}

// distinctFileCount counts distinct non-empty file ids.
func distinctFileCount(sources []ChatSource) int {
	seen := make(map[string]struct{}, len(sources))
	for _, s := range sources {
		if s.FileID == "" {
			continue
		}
		seen[s.FileID] = struct{}{}
	}
	return len(seen)
}

// pickConflictSources takes the top `max` sources by score and restores
// citation order. Score order decides WHICH sources are compared (the cap
// has to drop something, and the weakest matches are the ones least likely
// to carry the answer); citation order decides how they are PRESENTED, so
// the numbering the model reads runs the same direction as the numbering in
// the answer prompt.
func pickConflictSources(sources []ChatSource, max int) []ChatSource {
	if max <= 0 {
		max = defaultConflictMaxChunks
	}
	cp := make([]ChatSource, len(sources))
	copy(cp, sources)
	if len(cp) > max {
		sort.SliceStable(cp, func(i, j int) bool { return cp[i].Score > cp[j].Score })
		cp = cp[:max]
	}
	sort.SliceStable(cp, func(i, j int) bool { return cp[i].Index < cp[j].Index })
	return cp
}

// conflictSourceDates resolves the picked sources' file dates in one batch
// query. Dates already present on the sources (the orchestrator tail runs
// enrichSourceDates before persisting, but PrepareChatContext runs BEFORE
// that enrichment) are reused as-is; only the gaps trigger the lookup.
func conflictSourceDates(ctx context.Context, l FileDateLookup, sources []ChatSource) map[string]FileDates {
	out := make(map[string]FileDates, len(sources))
	var missing []string
	seen := make(map[string]struct{}, len(sources))
	for _, s := range sources {
		if s.FileID == "" {
			continue
		}
		if _, dup := seen[s.FileID]; dup {
			continue
		}
		seen[s.FileID] = struct{}{}
		if s.CreatedAt != nil {
			out[s.FileID] = FileDates{CreatedAt: *s.CreatedAt, PublishedAt: s.PublishedAt}
			continue
		}
		missing = append(missing, s.FileID)
	}
	if l == nil || len(missing) == 0 {
		return out
	}
	dates, err := l.FileDatesByIDs(ctx, missing)
	if err != nil {
		// Fail-soft, same contract as enrichSourceDates: an unknown date
		// only costs supersession direction, never the whole pass.
		logctx.From(ctx).Warn("rag.conflict_surfacing.date_lookup_failed", "error", err, "file_count", len(missing))
		return out
	}
	for id, d := range dates {
		out[id] = d
	}
	return out
}

// renderConflictDateLine renders one source's date for the prompt:
// published_at when the origin carries one, else the ingest timestamp, else
// the localized "unknown" the system prompt tells the model to read as "do
// not guess a direction".
func renderConflictDateLine(lang string, d FileDates) string {
	if d.PublishedAt != nil {
		return d.PublishedAt.UTC().Format("2006-01-02")
	}
	if !d.CreatedAt.IsZero() {
		return d.CreatedAt.UTC().Format("2006-01-02")
	}
	if lang == "de" {
		return "unbekannt"
	}
	return "unknown"
}

// conflictsForWire projects a report onto the value the `conflicts` SSE
// frame and the non-streaming JSON response carry: the bare array, so a
// client reads `event.conflicts[i].fileA` and not a doubly-nested
// `conflicts.conflicts`. Returns nil when there is nothing to report, which
// is what keeps the key absent on the overwhelming majority of turns.
//
// Note the deliberate asymmetry with the PERSISTED shape: messages.conflicts
// stores the whole ConflictReport object (`{"conflicts": [...]}`), because a
// JSONB blob that is a bare array cannot grow a sibling field later without
// a migration.
func conflictsForWire(report *ConflictReport) []MessageConflict {
	if report == nil || len(report.Conflicts) == 0 {
		return nil
	}
	return report.Conflicts
}

func emitConflictTrajectory(emit func(map[string]any), outcome string, count int) {
	emitTrajectory(emit,
		TrajectoryEvent{Stage: "conflict_surfacing", Reason: outcome, Findings: count},
		map[string]any{"conflictSurfacing": map[string]any{"outcome": outcome, "count": count}},
	)
}
