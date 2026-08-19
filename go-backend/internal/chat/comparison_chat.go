package chat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/justrag/go-backend/internal/agents"
	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/chatattach"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/prompts"
	"github.com/justrag/go-backend/internal/safego"
	"github.com/justrag/go-backend/internal/vector"
)

// willRunComparison is the dispatch predicate: an explicit attachment plus at
// least one comparison mode, gated by the feature flag. Unit-testable in
// isolation from the streaming path.
func willRunComparison(enabled bool, attachmentID string, modes []string) bool {
	return enabled && attachmentID != "" && len(modes) > 0
}

// renderFindingsForSummary renders findings as a compact numbered list for the
// summary LLM (and as injectable follow-up context).
func renderFindingsForSummary(findings []Finding) string {
	if len(findings) == 0 {
		return "No issues were found comparing the uploaded document against the knowledge base."
	}
	var b strings.Builder
	for i, f := range findings {
		fmt.Fprintf(&b, "%d. [%s/%s] %s", i+1, f.Mode, f.Severity, f.Issue)
		if f.UploadQuote != "" {
			fmt.Fprintf(&b, " (uploaded: %q)", f.UploadQuote)
		}
		if len(f.CitedFileIDs) > 0 {
			fmt.Fprintf(&b, " (cf. files: %s)", strings.Join(f.CitedFileIDs, ", "))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// shouldInjectFollowUpContext decides whether a turn carries a prior upload
// that must be injected into the system prompt. Named rather than inline so
// the rule is testable on its own: a test of buildFollowUpContext alone stays
// green when the call site's condition is wrong, which is how the "follow-up
// questions keep working" promise could break silently.
//
// Three parts, all required: an attachment id rides along; this is NOT a fresh
// comparison run (fresh runs get the document through the comparison
// orchestrator instead, and re-injecting would duplicate it); and an
// attachment store is actually wired.
func shouldInjectFollowUpContext(attachmentID string, runComparison, hasStore bool) bool {
	return attachmentID != "" && !runComparison && hasStore
}

// buildFollowUpContext renders a capped block of the uploaded document + prior
// findings, injected into follow-up turns so the user can ask about the upload
// without re-uploading.
func buildFollowUpContext(att chatattach.Attachment) string {
	const maxRunes = 3000
	text := att.FullText
	if len([]rune(text)) > maxRunes {
		text = truncateRunes(text, maxRunes) + "…"
	}
	var b strings.Builder
	b.WriteString("UPLOADED DOCUMENT (\"")
	b.WriteString(att.Filename)
	b.WriteString("\"):\n")
	b.WriteString(text)
	if len(att.Findings) > 0 {
		b.WriteString("\n\nPRIOR COMPARISON FINDINGS:\n")
		b.WriteString(renderFindingsForSummary(att.Findings))
	}
	return b.String()
}

// searchFn matches vector.Searcher.Search.
type searchFn func(ctx context.Context, kbID, query string, limit int, opts vector.SearchOptions) (*vector.SearchResult, error)

// structuredFn returns the raw JSON content from a structured-output LLM call.
type structuredFn func(ctx context.Context, prompt, system, kbID, model string, spec *ai.StructuredSpec) (string, error)

// ComparisonChatParams configures one comparison turn.
type ComparisonChatParams struct {
	KbID            string
	ChatID          string
	UserMsgID       string
	Language        string
	Model           string
	Modes           []string
	Sections        []string
	MaxSections     int
	Concurrency     int
	PeersPerSection int
}

type comparisonResult struct {
	Findings         []Finding
	Sources          []vector.SearchChunk
	SectionsAnalyzed int
	Truncated        bool
}

func severityRank(s string) int {
	switch s {
	case "high":
		return 0
	case "medium":
		return 1
	default:
		return 2
	}
}

func runComparisonEngine(ctx context.Context, p ComparisonChatParams, search searchFn, structured structuredFn, emit func(map[string]any)) (comparisonResult, error) {
	sections := p.Sections
	truncated := false
	if p.MaxSections > 0 && len(sections) > p.MaxSections {
		sections = sections[:p.MaxSections]
		truncated = true
		logctx.From(ctx).Warn("comparison: section cap hit; truncating",
			"total", len(p.Sections), "cap", p.MaxSections)
	}

	conc := p.Concurrency
	if conc < 1 {
		conc = 1
	}
	sem := make(chan struct{}, conc)
	var mu sync.Mutex
	var wg sync.WaitGroup
	var findings []Finding
	sourceByID := map[string]vector.SearchChunk{}
	// One error slot per section for the panic recovery below. Written only by
	// the owning goroutine (Go 1.22+ per-iteration loop vars), read after Wait.
	sectionErrs := make([]error, len(sections))

launch:
	for i := range sections {
		// Cancellation-aware acquire: a cancelled request must stop launching
		// new section workers rather than queue each one just to fail. Already-
		// launched workers finish; partial results are fine (fail-open design).
		// Matches the semaphore idiom in corpus_table.go / multipass.go.
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			break launch
		}
		wg.Add(1)
		// Go 1.22+ per-iteration loop vars: capturing i directly is safe.
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			// Registered last so it runs FIRST on unwind, stopping the panic
			// before it escapes the goroutine. A section worker calls out to
			// search, an LLM endpoint and json.Unmarshal — a shape surprise
			// from any of them would otherwise take the whole process down,
			// contradicting the fail-open contract the per-section error
			// handling below already implements. Every shared-state mutation
			// under `mu` uses defer-unlock so an unwinding section can't leave
			// the mutex held.
			defer safego.RecoverError(&sectionErrs[i])

			sectionText := sections[i]
			peers, err := search(ctx, p.KbID, sectionText, p.PeersPerSection, vector.SearchOptions{})
			if err != nil {
				logctx.From(ctx).Warn("comparison: peer search failed", "section", i, "error", err)
				peers = &vector.SearchResult{}
			}
			peerBlock, peerFileIDs := formatPeers(peers.Chunks)

			localFindings := 0
			for _, mode := range p.Modes {
				system := prompts.ComparisonModePrompt(mode, p.Language)
				prompt := buildComparisonPrompt(sectionText, peerBlock, p.Language)
				raw, serr := structured(ctx, prompt, system, p.KbID, p.Model, comparisonFindingsSpec())
				if serr != nil {
					logctx.From(ctx).Warn("comparison: check failed", "section", i, "mode", mode, "error", serr)
					continue
				}
				var payload comparisonFindingsPayload
				if jerr := json.Unmarshal([]byte(strings.TrimSpace(raw)), &payload); jerr != nil {
					logctx.From(ctx).Warn("comparison: bad findings json", "section", i, "mode", mode, "error", jerr)
					continue
				}
				// Lock released via defer inside a closure, not a plain
				// Unlock: the recovery below means a panic in here unwinds
				// instead of ending the process, and a mutex left locked would
				// hang every other section on Lock and wedge wg.Wait forever.
				func() {
					mu.Lock()
					defer mu.Unlock()
					for _, f := range payload.Findings {
						cited := f.CitedFileIDs
						if len(cited) == 0 {
							cited = peerFileIDs
						}
						findings = append(findings, Finding{
							Mode: mode, Severity: f.Severity, SectionIdx: i,
							UploadQuote: f.UploadQuote, Issue: f.Issue,
							CitedFileIDs: cited, CitedQuote: f.CitedQuote,
						})
						localFindings++
					}
				}()
			}

			// Same defer-unlock rationale as above — and it matters more here,
			// because emit is a caller-supplied sink (the SSE relay) whose
			// panics this goroutine does not control.
			func() {
				mu.Lock()
				defer mu.Unlock()
				for _, c := range peers.Chunks {
					if c.ID != "" {
						sourceByID[c.ID] = c
					}
				}
				// emit under the mutex: it's a shared sink (the SSE relay / test
				// collector) and the engine fans out over sections, so concurrent
				// emit calls would race an unsynchronized callback.
				emitTrajectory(emit, TrajectoryEvent{
					Stage: "compare_section", Step: i + 1, Findings: localFindings,
				}, nil)
			}()
		}()
	}
	wg.Wait()

	// Surface recovered panics: fail-open means the turn still answers with
	// whatever the healthy sections produced, but a crashed section must not
	// vanish silently.
	for i, serr := range sectionErrs {
		if serr != nil {
			logctx.From(ctx).Error("comparison: section worker panicked", "section", i, "error", serr)
		}
	}

	sort.SliceStable(findings, func(a, b int) bool {
		return severityRank(findings[a].Severity) < severityRank(findings[b].Severity)
	})

	sources := make([]vector.SearchChunk, 0, len(sourceByID))
	for _, c := range sourceByID {
		sources = append(sources, c)
	}

	return comparisonResult{
		Findings: findings, Sources: sources,
		SectionsAnalyzed: len(sections), Truncated: truncated,
	}, nil
}

func formatPeers(chunks []vector.SearchChunk) (string, []string) {
	var b strings.Builder
	var ids []string
	seen := map[string]bool{}
	for _, c := range chunks {
		b.WriteString("[file:")
		b.WriteString(c.FileID)
		b.WriteString("] ")
		b.WriteString(c.Content)
		b.WriteString("\n\n")
		if c.FileID != "" && !seen[c.FileID] {
			seen[c.FileID] = true
			ids = append(ids, c.FileID)
		}
	}
	return b.String(), ids
}

func buildComparisonPrompt(section, peerBlock, lang string) string {
	if lang == "de" {
		return "HOCHGELADENER ABSCHNITT:\n" + section +
			"\n\nVORHANDENE KB-PASSAGEN:\n" + peerBlock +
			"\nMelde Befunde gemäß Schema."
	}
	return "UPLOADED SECTION:\n" + section +
		"\n\nEXISTING KB PASSAGES:\n" + peerBlock +
		"\nReport findings per the schema."
}

// ComparisonDeps bundles the injectable dependencies for RunComparisonChat.
type ComparisonDeps struct {
	Store      chatattach.Store
	Search     searchFn
	Structured structuredFn
}

// ErrAttachmentForbidden is returned when the caller does not own the attachment.
var ErrAttachmentForbidden = errors.New("comparison: attachment not owned by caller")

// RunComparisonChat loads the attachment, runs the fan-out engine, persists findings
// back onto the attachment, and returns a ChatContext with the cited chunks populated.
func RunComparisonChat(ctx context.Context, deps ComparisonDeps, attachmentID, userID string, params ComparisonChatParams, emit func(map[string]any)) (*ChatContext, []Finding, error) {
	att, err := deps.Store.Get(ctx, attachmentID)
	if err != nil {
		return nil, nil, err
	}
	if att.UserID != userID {
		return nil, nil, ErrAttachmentForbidden
	}
	params.Sections = att.Sections
	if params.KbID == "" {
		params.KbID = att.KbID
	}

	res, err := runComparisonEngine(ctx, params, deps.Search, deps.Structured, emit)
	if err != nil {
		return nil, nil, err
	}

	att.Findings = res.Findings
	if _, perr := deps.Store.Put(ctx, att); perr != nil {
		logctx.From(ctx).Warn("comparison: failed to persist findings", "error", perr)
	}

	emit(map[string]any{"comparisonFindings": res.Findings})

	// FinalChunks is what the eval harness scores for recall (and what the
	// user-facing Sources field is a lossy projection of). The other
	// orchestrators (supervisor/plan-execute/agentic) set FinalChunks +
	// derive Sources via buildChatSourcesAndContext; replicate that contract
	// so this orchestrator is not scored 0.000 recall.
	sources, contextText := buildChatSourcesAndContext(res.Sources)
	cc := &ChatContext{
		Sources:     sources,
		Context:     contextText,
		FinalChunks: res.Sources,
	}
	return cc, res.Findings, nil
}

// comparisonSummaryPromptFor builds the system prompt for the comparison
// summary. As its own function because two paths need it: the standard path
// sets it directly on the ChatContext, and the team path hands it to
// RunTeamChat as KbSystemPrompt so the specialists see the findings in their
// system prompt.
func comparisonSummaryPromptFor(kbSystemPrompt, lang string, findings []Finding) string {
	var b strings.Builder
	if kbSystemPrompt != "" {
		b.WriteString(kbSystemPrompt)
		b.WriteString("\n\n")
	}
	b.WriteString(prompts.ComparisonSummaryPrompt(lang))
	b.WriteString("\n\n")
	b.WriteString(renderFindingsForSummary(findings))
	return b.String()
}

// mergeComparisonChunks merges the comparison stage's peer chunks with the
// team's. As its own function so the merge is testable: without it,
// RunTeamChat would fully overwrite the comparison stage's FinalChunks, and
// every finding source it found would drop out of citation validation and
// the source list.
func mergeComparisonChunks(teamChunks, comparisonChunks []vector.SearchChunk) []vector.SearchChunk {
	return agents.MergeChunksRRF(0, teamChunks, comparisonChunks)
}
