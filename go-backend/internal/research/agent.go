package research

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/safego"
	"github.com/justrag/go-backend/internal/vector"
)

// researchEventBufferSize is the capacity of the ProgressEvent channel
// returned by Agent.Run. emit() does a non-blocking send and drops events
// (with a slog.Warn) when the buffer is full, so this is purely a smoothing
// buffer to absorb bursts when the SSE consumer is momentarily slow. 100
// covers a full multi-step research run's typical event volume; if it
// becomes a bottleneck for slow clients, expose it via Options.
const researchEventBufferSize = 100

// ---------------------------------------------------------------------------
// Agent state
// ---------------------------------------------------------------------------

// ResearchState holds mutable state for a single agent run.
type ResearchState struct {
	Goal                            string
	Findings                        []Finding
	Plan                            []PlanStep
	StepsTaken                      int
	MaxSteps                        int
	Status                          ResearchStatus
	Language                        string
	ConsecutiveStepsWithoutFindings int
}

// ---------------------------------------------------------------------------
// Agent
// ---------------------------------------------------------------------------

// Agent orchestrates a multi-step research loop: decompose → plan → execute →
// refine → generate report. Progress events are streamed via a channel.
type Agent struct {
	kbID string
	goal string
	opts Options
	// state is owned exclusively by the goroutine started in Run(); once.Do
	// guarantees at most one such goroutine ever exists and no other code
	// path touches these fields, so the lack of a mutex is safe. Anyone
	// adding an external accessor (e.g. a Status() method) MUST add a mutex
	// or atomic snapshot first — the current lock-free access is only valid
	// under single-goroutine ownership.
	state     ResearchState
	resolver  *ai.ConfigResolver
	searchSvc vector.Searcher
	webClient *WebClient // optional — enables the "web" mode branch
	redis     *redis.Client
	events    chan ProgressEvent
	once      sync.Once // guards Run to prevent multiple invocations
}

// NewAgent creates a new Agent ready to be Run. webClient is optional —
// when nil, "web" mode silently degrades to the KB search path instead
// of crashing. Pass a non-nil WebClient (see NewWebClient) to enable the
// Google + Fetcher branch for Mode == "web".
func NewAgent(
	kbID, goal string,
	opts Options,
	resolver *ai.ConfigResolver,
	searchSvc vector.Searcher,
	rdb *redis.Client,
	webClient *WebClient,
) *Agent {
	maxSteps := opts.MaxSteps
	if maxSteps <= 0 {
		maxSteps = 10
	}
	chunksPerSearch := opts.ChunksPerSearch
	if chunksPerSearch <= 0 {
		chunksPerSearch = 5
	}
	lang := opts.Language
	if lang == "" {
		lang = "en"
	}
	mode := opts.Mode
	if mode == "" {
		mode = "kb"
	}
	maxNoProgress := opts.MaxConsecutiveStepsWithoutFindings
	if maxNoProgress <= 0 {
		maxNoProgress = 2
	}

	return &Agent{
		kbID: kbID,
		goal: goal,
		opts: Options{
			MaxSteps:                           maxSteps,
			Language:                           lang,
			ChunksPerSearch:                    chunksPerSearch,
			FileIDs:                            opts.FileIDs,
			Mode:                               mode,
			AbortKey:                           opts.AbortKey,
			MaxConsecutiveStepsWithoutFindings: maxNoProgress,
		},
		state: ResearchState{
			Goal:     goal,
			MaxSteps: maxSteps,
			Status:   StatusInitializing,
			Language: lang,
		},
		resolver:  resolver,
		searchSvc: searchSvc,
		webClient: webClient,
		redis:     rdb,
		// Allocate the events channel here rather than inside Run's once.Do so
		// the field is never nil after construction. A receive on a nil channel
		// blocks forever; allocating at construction removes that footgun for
		// any caller that reads the channel before Run establishes it.
		events: make(chan ProgressEvent, researchEventBufferSize),
	}
}

// ---------------------------------------------------------------------------
// Run — main entry point
// ---------------------------------------------------------------------------

// Run starts the research loop in a background goroutine and returns a channel
// of progress events. The channel is closed when research completes or is
// cancelled/aborted. Run may only be called once; subsequent calls return the
// same channel.
func (a *Agent) Run(ctx context.Context) <-chan ProgressEvent {
	a.once.Do(func() {
		safego.GoCtx(ctx, func() {
			defer close(a.events)

			a.emit(ProgressEvent{
				Type:    "started",
				Message: fmt.Sprintf("Starting research: %s", a.goal),
			})

			// 1. Decompose goal into initial sub-questions / search steps.
			a.state.Status = StatusDecomposing
			a.decompose(ctx)
			if a.isAborted(ctx) {
				a.setStatus(StatusCancelled, "Research cancelled")
				return
			}

			// 2. Main research loop.
			a.state.Status = StatusPlanning
			for a.state.StepsTaken < a.state.MaxSteps && a.state.Status != StatusComplete {
				if a.isAborted(ctx) {
					a.setStatus(StatusCancelled, "Research cancelled")
					return
				}

				a.state.StepsTaken++

				// Plan if there are no pending steps left.
				if !a.hasPendingSteps() {
					a.state.Status = StatusPlanning
					a.planNextSteps(ctx)
				}

				if a.isAborted(ctx) {
					a.setStatus(StatusCancelled, "Research cancelled")
					return
				}

				// Execute up to 3 pending steps.
				findingsBefore := len(a.state.Findings)
				a.state.Status = StatusExecuting
				a.executeSteps(ctx)
				findingsAdded := len(a.state.Findings) - findingsBefore

				// Deterministic convergence guard: stop early when consecutive
				// iterations add no new (non-duplicate) findings.
				if findingsAdded > 0 {
					a.state.ConsecutiveStepsWithoutFindings = 0
				} else {
					a.state.ConsecutiveStepsWithoutFindings++
					if a.opts.MaxConsecutiveStepsWithoutFindings > 0 &&
						a.state.ConsecutiveStepsWithoutFindings >= a.opts.MaxConsecutiveStepsWithoutFindings {
						a.emit(ProgressEvent{
							Type:    "convergence",
							Message: fmt.Sprintf("Stopping early: %d consecutive steps without new findings", a.state.ConsecutiveStepsWithoutFindings),
						})
						a.setStatus(StatusComplete, "Converged: no new findings")
						break
					}
				}

				// Observe: check whether findings are sufficient.
				if len(a.state.Findings) >= 2 {
					a.state.Status = StatusRefining
					a.refineStrategy(ctx)
				}
			}

			// 3. Generate final report.
			a.state.Status = StatusGeneratingReport
			a.generateReport(ctx)
		})
	})

	return a.events
}

// ---------------------------------------------------------------------------
// decompose — break the goal into sub-questions
// ---------------------------------------------------------------------------

func (a *Agent) decompose(ctx context.Context) {
	words := strings.Fields(a.goal)
	if len(words) < 4 {
		// Short goal: treat it as a single search step directly.
		a.addSearchStep(a.goal)
		return
	}

	a.emit(ProgressEvent{
		Type:    "planning",
		Message: "Decomposing goal into sub-questions",
	})

	prompt := fmt.Sprintf(
		`Break this research goal into 3-5 focused sub-questions: "%s"`+"\n\n"+
			`Return JSON only: {"subQuestions": ["question1", "question2", ...]}`,
		a.goal,
	)
	system := "You are a research planning assistant. Respond with valid JSON only."

	result, err := ai.GenerateCompletion(ctx, a.resolver, prompt, system, a.kbID, false)
	if err != nil || result == nil {
		// Fall back to searching the full goal.
		a.addSearchStep(a.goal)
		return
	}

	var parsed struct {
		SubQuestions []string `json:"subQuestions"`
	}
	if err := parseJSON(result.Content, &parsed); err != nil || len(parsed.SubQuestions) == 0 {
		a.addSearchStep(a.goal)
		return
	}

	for _, q := range parsed.SubQuestions {
		q = strings.TrimSpace(q)
		if q != "" {
			a.addSearchStep(q)
		}
	}
}

// ---------------------------------------------------------------------------
// planNextSteps — ask LLM what to search for next
// ---------------------------------------------------------------------------

func (a *Agent) planNextSteps(ctx context.Context) {
	a.emit(ProgressEvent{
		Type:    "planning",
		Message: "Planning next research steps",
		Plan:    a.state.Plan,
	})

	findingsSummary := summarizeFindings(a.state.Findings, 1000)

	prompt := fmt.Sprintf(
		"Research goal: %s\n\nFindings so far:\n%s\n\n"+
			"What should I search for next to answer the goal?\n"+
			`Return JSON only: {"steps": [{"action": "search", "query": "..."}], "isComplete": false}`,
		a.goal, findingsSummary,
	)
	system := "You are a research planning assistant. Respond with valid JSON only."

	result, err := ai.GenerateCompletion(ctx, a.resolver, prompt, system, a.kbID, false)
	if err != nil || result == nil {
		return
	}

	var parsed struct {
		Steps      []struct{ Action, Query string } `json:"steps"`
		IsComplete bool                             `json:"isComplete"`
	}
	if err := parseJSON(result.Content, &parsed); err != nil {
		return
	}

	if parsed.IsComplete {
		a.state.Status = StatusComplete
		return
	}

	for _, s := range parsed.Steps {
		action := strings.ToLower(strings.TrimSpace(s.Action))
		if action == "" {
			action = "search"
		}
		q := strings.TrimSpace(s.Query)
		if q == "" {
			continue
		}
		a.state.Plan = append(a.state.Plan, PlanStep{
			ID:     newStepID(),
			Action: action,
			Query:  q,
			Status: "pending",
		})
	}
}

// ---------------------------------------------------------------------------
// executeSteps — run up to 3 pending steps
// ---------------------------------------------------------------------------

func (a *Agent) executeSteps(ctx context.Context) {
	executed := 0
	for i := range a.state.Plan {
		if executed >= 3 {
			break
		}
		step := &a.state.Plan[i]
		if step.Status != "pending" {
			continue
		}

		step.Status = "in_progress"
		executed++

		switch step.Action {
		case "search":
			a.executeSearch(ctx, step)
		default:
			// Unknown action — mark complete with no result.
			step.Status = "complete"
		}

		if a.isAborted(ctx) {
			return
		}
	}
}

func (a *Agent) executeSearch(ctx context.Context, step *PlanStep) {
	a.emit(ProgressEvent{
		Type:    "searching",
		Message: fmt.Sprintf("Searching: %s", step.Query),
	})

	// Web mode: Google + Fetcher → synthesize from real page content.
	// Falls through to KB search if the web client isn't wired.
	if a.opts.Mode == "web" && a.webClient != nil {
		a.executeWebSearch(ctx, step)
		return
	}

	chunks, err := a.search(ctx, step.Query)
	if err != nil || len(chunks) == 0 {
		step.Status = "failed"
		step.Result = "no results found"
		return
	}

	step.DocumentsProcessed = len(chunks)

	// Synthesize findings from retrieved chunks.
	finding, err := a.synthesize(ctx, step.Query, chunks)
	if err != nil || finding == nil {
		step.Status = "complete"
		return
	}

	// Deduplicate against existing findings.
	if !a.isDuplicate(finding) {
		a.state.Findings = append(a.state.Findings, *finding)
		a.emit(ProgressEvent{
			Type:     "finding",
			Message:  "New finding discovered",
			Findings: []Finding{*finding},
		})
	}

	step.Status = "complete"
	step.Result = finding.Content
}

// ---------------------------------------------------------------------------
// search — dispatch to KB or web
// ---------------------------------------------------------------------------

func (a *Agent) search(ctx context.Context, query string) ([]vector.SearchChunk, error) {
	limit := a.opts.ChunksPerSearch
	if limit <= 0 {
		limit = 5
	}

	opts := vector.SearchOptions{
		FileIDs: a.opts.FileIDs,
	}

	result, err := a.searchSvc.Search(ctx, a.kbID, query, limit, opts)
	if err != nil {
		return nil, err
	}
	return result.Chunks, nil
}

// ---------------------------------------------------------------------------
// synthesize — ask LLM to extract key facts from chunks
// ---------------------------------------------------------------------------

func (a *Agent) synthesize(ctx context.Context, query string, chunks []vector.SearchChunk) (*Finding, error) {
	if len(chunks) == 0 {
		return nil, nil
	}

	a.emit(ProgressEvent{
		Type:    "synthesizing",
		Message: fmt.Sprintf("Synthesizing %d chunks for: %s", len(chunks), query),
	})

	// Build a compact document block for the prompt.
	var sb strings.Builder
	sources := make([]SourceRef, 0, len(chunks))
	for i, c := range chunks {
		sb.WriteString(fmt.Sprintf("[Doc %d | %s]\n%s\n\n", i+1, c.FileName, c.Content))
		sources = append(sources, SourceRef{
			FileName:     c.FileName,
			FileID:       c.FileID,
			ChunkContent: truncate(c.Content, 200),
		})
	}

	prompt := fmt.Sprintf(
		"Extract key facts from the following documents that answer this query: %s\n\nDocuments:\n%s\n"+
			`Return JSON only: {"facts": ["fact1", "fact2"], "needsMoreInfo": false}`,
		query, sb.String(),
	)
	system := "You are a research assistant. Extract factual information concisely. Respond with valid JSON only."

	result, err := ai.GenerateCompletion(ctx, a.resolver, prompt, system, a.kbID, false)
	if err != nil || result == nil {
		return nil, err
	}

	var parsed struct {
		Facts         []string `json:"facts"`
		NeedsMoreInfo bool     `json:"needsMoreInfo"`
	}
	if err := parseJSON(result.Content, &parsed); err != nil || len(parsed.Facts) == 0 {
		return nil, nil
	}

	content := strings.Join(parsed.Facts, " ")
	finding := &Finding{
		Content:        content,
		Sources:        sources,
		RelevanceScore: 0.8, // default; could be derived from chunk scores
	}

	// Use average chunk score as relevance if available.
	if len(chunks) > 0 {
		var total float64
		for _, c := range chunks {
			total += c.Score
		}
		finding.RelevanceScore = total / float64(len(chunks))
	}

	return finding, nil
}

// ---------------------------------------------------------------------------
// refineStrategy — check if goal is achieved
// ---------------------------------------------------------------------------

func (a *Agent) refineStrategy(ctx context.Context) {
	a.emit(ProgressEvent{
		Type:    "synthesizing",
		Message: "Evaluating research completeness",
	})

	findingsSummary := summarizeFindings(a.state.Findings, 1500)

	prompt := fmt.Sprintf(
		"Research goal: %s\n\nFindings so far:\n%s\n\n"+
			"Are these findings sufficient to fully answer the research goal?\n"+
			`Return JSON only: {"isComplete": false, "reasoning": "...", "additionalSteps": [{"action": "search", "query": "..."}]}`,
		a.goal, findingsSummary,
	)
	system := "You are a research quality evaluator. Respond with valid JSON only."

	result, err := ai.GenerateCompletion(ctx, a.resolver, prompt, system, a.kbID, false)
	if err != nil || result == nil {
		return
	}

	var parsed struct {
		IsComplete      bool   `json:"isComplete"`
		Reasoning       string `json:"reasoning"`
		AdditionalSteps []struct {
			Action string `json:"action"`
			Query  string `json:"query"`
		} `json:"additionalSteps"`
	}
	if err := parseJSON(result.Content, &parsed); err != nil {
		return
	}

	if parsed.IsComplete {
		a.state.Status = StatusComplete
		return
	}

	// Add any additional steps the LLM suggests.
	for _, s := range parsed.AdditionalSteps {
		action := strings.TrimSpace(s.Action)
		if action == "" {
			action = "search"
		}
		q := strings.TrimSpace(s.Query)
		if q == "" {
			continue
		}
		a.state.Plan = append(a.state.Plan, PlanStep{
			ID:     newStepID(),
			Action: action,
			Query:  q,
			Status: "pending",
		})
	}
}

// ---------------------------------------------------------------------------
// generateReport — compile all findings into a markdown report
// ---------------------------------------------------------------------------

func (a *Agent) generateReport(ctx context.Context) {
	a.emit(ProgressEvent{
		Type:    "synthesizing",
		Message: "Generating final report",
	})

	findingsSummary := summarizeFindingsWithSources(a.state.Findings, 3000)

	// The citation instruction is mode-aware: web-mode findings carry
	// SourceRef.URL (no file IDs), KB-mode findings carry filenames.
	// Tell the model to cite whichever is present so deep-web reports
	// don't drop URL references.
	citationHint := "Cite sources using [Source: filename] for KB documents and [Source: title — URL] for web pages, matching whatever appears in the findings list above."

	prompt := fmt.Sprintf(
		"Compile the following research findings into a comprehensive markdown report that fully answers this goal:\n\n"+
			"Goal: %s\n\nFindings:\n%s\n\n"+
			"Format as markdown with sections. %s",
		a.goal, findingsSummary, citationHint,
	)
	system := "You are a research report writer. Produce a well-structured markdown report."

	result, err := ai.GenerateCompletion(ctx, a.resolver, prompt, system, a.kbID, false)
	if err != nil || result == nil {
		report := a.fallbackReport()
		a.emit(ProgressEvent{
			Type:    "complete",
			Message: "Research complete (fallback report)",
			Report:  report,
			Plan:    a.state.Plan,
		})
		a.state.Status = StatusComplete
		return
	}

	a.state.Status = StatusComplete
	a.emit(ProgressEvent{
		Type:     "complete",
		Message:  "Research complete",
		Report:   result.Content,
		Findings: a.state.Findings,
		Plan:     a.state.Plan,
	})
}

// fallbackReport builds a minimal report from findings when LLM is unavailable.
// Renders URL sources for web-mode findings and filename sources for
// KB-mode findings, so deep-web reports still include clickable URLs
// when the LLM call fails.
func (a *Agent) fallbackReport() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Research Report\n\n**Goal:** %s\n\n## Findings\n\n", a.goal)
	for i, f := range a.state.Findings {
		fmt.Fprintf(&sb, "### Finding %d\n\n%s\n\n", i+1, f.Content)
		for _, src := range f.Sources {
			switch {
			case src.URL != "":
				label := src.FileName
				if label == "" {
					label = src.URL
				}
				fmt.Fprintf(&sb, "- [%s](%s)\n", label, src.URL)
			case src.FileName != "":
				fmt.Fprintf(&sb, "- [Source: %s]\n", src.FileName)
			}
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// summarizeFindingsWithSources is the citation-aware variant of
// summarizeFindings used by generateReport. For each finding it appends
// the distinct source references (filename or URL) so the report writer
// LLM has the metadata it needs to produce inline citations — including
// URL citations for web-mode deep research, which the basic
// summarizeFindings strips away.
func summarizeFindingsWithSources(findings []Finding, maxLen int) string {
	var sb strings.Builder
	for i, f := range findings {
		line := fmt.Sprintf("%d. %s\n", i+1, f.Content)
		// Build a compact source suffix. Keep unique URLs and file names
		// so duplicates (e.g. two chunks from the same page) don't bloat
		// the prompt.
		seen := map[string]bool{}
		var refs []string
		for _, src := range f.Sources {
			switch {
			case src.URL != "":
				key := "url:" + src.URL
				if seen[key] {
					continue
				}
				seen[key] = true
				title := src.FileName
				if title == "" {
					title = src.URL
				}
				refs = append(refs, fmt.Sprintf("%s — %s", title, src.URL))
			case src.FileName != "":
				key := "file:" + src.FileName
				if seen[key] {
					continue
				}
				seen[key] = true
				refs = append(refs, src.FileName)
			}
		}
		if len(refs) > 0 {
			line += "   Sources: " + strings.Join(refs, "; ") + "\n"
		}
		if sb.Len()+len(line) > maxLen {
			break
		}
		sb.WriteString(line)
	}
	if sb.Len() == 0 {
		return "(none yet)"
	}
	return sb.String()
}

// ---------------------------------------------------------------------------
// isAborted — single source of truth for cancellation checks
// ---------------------------------------------------------------------------

// isAborted reports whether the agent should stop. The caller is expected to
// wire abort detection (Redis abort key, HTTP client disconnect, …) into ctx
// cancellation — see worker.watchAbortKey for the worker path. Inline Redis
// polling was removed: it duplicated the worker's watcher and added 4–6
// synchronous Redis round-trips per agent iteration.
func (a *Agent) isAborted(ctx context.Context) bool {
	return ctx.Err() != nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func (a *Agent) hasPendingSteps() bool {
	for _, s := range a.state.Plan {
		if s.Status == "pending" {
			return true
		}
	}
	return false
}

func (a *Agent) addSearchStep(query string) {
	a.state.Plan = append(a.state.Plan, PlanStep{
		ID:     newStepID(),
		Action: "search",
		Query:  query,
		Status: "pending",
	})
}

// isDuplicate returns true when the finding's content is too similar to an
// existing finding (Jaccard similarity > 0.8 on word sets).
func (a *Agent) isDuplicate(f *Finding) bool {
	for _, existing := range a.state.Findings {
		if jaccardSimilarity(existing.Content, f.Content) > 0.8 {
			return true
		}
	}
	return false
}

// emit sends a ProgressEvent on the channel, adding the current timestamp.
// It is non-blocking: if the buffer is full the event is dropped rather than
// causing the goroutine to stall. A drop signals that the SSE relay isn't
// keeping up with the agent — log it so an operator can correlate gaps in
// the client's event stream with backpressure rather than guessing.
func (a *Agent) emit(evt ProgressEvent) {
	evt.Timestamp = time.Now().UnixMilli()
	evt.StepNumber = a.state.StepsTaken
	evt.TotalSteps = a.state.MaxSteps
	select {
	case a.events <- evt:
	default:
		observability.RecordResearchEventDropped()
		slog.Warn("research agent dropped progress event (buffer full)",
			"kbId", a.kbID,
			"step", a.state.StepsTaken,
			"eventType", evt.Type,
		)
	}
}

func (a *Agent) setStatus(status ResearchStatus, msg string) {
	a.state.Status = status
	evtType := string(status)
	a.emit(ProgressEvent{
		Type:    evtType,
		Message: msg,
	})
}

// ---------------------------------------------------------------------------
// Package-level helpers
// ---------------------------------------------------------------------------

// newStepID generates a short time-based identifier for a plan step.
func newStepID() string {
	return fmt.Sprintf("step-%d", time.Now().UnixNano())
}

// summarizeFindings converts findings to a compact string for prompt injection.
func summarizeFindings(findings []Finding, maxLen int) string {
	var sb strings.Builder
	for i, f := range findings {
		line := fmt.Sprintf("%d. %s\n", i+1, f.Content)
		if sb.Len()+len(line) > maxLen {
			break
		}
		sb.WriteString(line)
	}
	if sb.Len() == 0 {
		return "(none yet)"
	}
	return sb.String()
}

// truncate returns s truncated to n bytes, appending "…" if truncated.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// jaccardSimilarity computes word-level Jaccard similarity between two strings.
func jaccardSimilarity(a, b string) float64 {
	setA := wordSet(a)
	setB := wordSet(b)
	if len(setA) == 0 && len(setB) == 0 {
		return 1.0
	}

	var intersection int
	for w := range setA {
		if setB[w] {
			intersection++
		}
	}
	union := len(setA) + len(setB) - intersection
	if union == 0 {
		return 0
	}
	return math.Round(float64(intersection)/float64(union)*1000) / 1000
}

func wordSet(s string) map[string]bool {
	words := strings.Fields(strings.ToLower(s))
	set := make(map[string]bool, len(words))
	for _, w := range words {
		set[w] = true
	}
	return set
}

// parseJSON tries to unmarshal v from text, falling back to extracting the
// first {...} or [...] substring when the full text is not valid JSON.
func parseJSON(text string, v any) error {
	text = strings.TrimSpace(text)
	if err := json.Unmarshal([]byte(text), v); err == nil {
		return nil
	}

	// Try to find embedded JSON object.
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start >= 0 && end > start {
		if err := json.Unmarshal([]byte(text[start:end+1]), v); err == nil {
			return nil
		}
	}

	// Try array.
	start = strings.Index(text, "[")
	end = strings.LastIndex(text, "]")
	if start >= 0 && end > start {
		if err := json.Unmarshal([]byte(text[start:end+1]), v); err == nil {
			return nil
		}
	}

	return fmt.Errorf("parseJSON: no valid JSON found in: %.80s", text)
}
