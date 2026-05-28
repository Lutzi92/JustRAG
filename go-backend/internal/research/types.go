package research

// ResearchStatus represents the current phase of a research agent run.
type ResearchStatus string

const (
	StatusInitializing     ResearchStatus = "initializing"
	StatusDecomposing      ResearchStatus = "decomposing"
	StatusPlanning         ResearchStatus = "planning"
	StatusExecuting        ResearchStatus = "executing"
	StatusObserving        ResearchStatus = "observing"
	StatusRefining         ResearchStatus = "refining"
	StatusGeneratingReport ResearchStatus = "generating_report"
	StatusComplete         ResearchStatus = "complete"
	StatusError            ResearchStatus = "error"
	StatusCancelled        ResearchStatus = "cancelled"
)

// Finding holds a synthesized piece of information extracted from search results.
type Finding struct {
	Content        string      `json:"content"`
	Sources        []SourceRef `json:"sources"`
	RelevanceScore float64     `json:"relevanceScore"`
}

// SourceRef identifies the origin document (KB file or URL) of a finding.
type SourceRef struct {
	FileName     string `json:"fileName"`
	FileID       string `json:"fileId"`
	Pages        []int  `json:"pages,omitempty"`
	ChunkContent string `json:"chunkContent,omitempty"`
	URL          string `json:"url,omitempty"`
}

// PlanStep is a single unit of work within the research plan.
type PlanStep struct {
	ID                 string `json:"id"`
	Action             string `json:"action"` // "search", "synthesize", "refine_query"
	Query              string `json:"query,omitempty"`
	Status             string `json:"status"` // "pending", "in_progress", "complete", "failed"
	Result             string `json:"result,omitempty"`
	DocumentsProcessed int    `json:"documentsProcessed,omitempty"`
}

// ProgressEvent is streamed to the caller to report agent progress.
type ProgressEvent struct {
	Type       string     `json:"type"` // "started", "planning", "searching", "synthesizing", "finding", "complete", "error"
	Message    string     `json:"message"`
	StepNumber int        `json:"stepNumber"`
	TotalSteps int        `json:"totalSteps"`
	Findings   []Finding  `json:"findings,omitempty"`
	Plan       []PlanStep `json:"plan,omitempty"`
	Report     string     `json:"report,omitempty"`
	Timestamp  int64      `json:"timestamp"`
}

// Options configures a research agent run.
type Options struct {
	MaxSteps                           int
	Language                           string
	ChunksPerSearch                    int
	FileIDs                            []string
	Mode                               string // "kb" or "web"
	AbortKey                           string // Redis key to check for abort signal
	MaxConsecutiveStepsWithoutFindings int    // default 2; values <= 0 use the default 2
}
