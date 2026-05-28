// Package jobs defines the canonical queue names and task type constants used
// across the application. Feature packages import jobs to reference these
// constants without pulling in the worker package, breaking potential import
// cycles.
package jobs

// Queue names.
const (
	QueueQuick = "rag-quick"
	QueueHeavy = "rag-heavy"
	QueueBatch = "rag-batch"
)

// Task types.
const (
	TypeFileProcessing            = "file-processing"
	TypeTextProcessing            = "text-processing"
	TypeURLProcessing             = "url-processing"
	TypeRSSPoll                   = "rss-poll"
	TypeReEmbedding               = "re-embedding"
	TypeResearchExecution         = "research-execution"
	TypeAcademicResearchExecution = "academic-research-execution"
	TypeContentGeneration         = "content-generation"
	TypeConfluenceSync            = "confluence-sync"
	TypeCrawl                     = "crawl"
	TypeEvalRun                   = "eval-run"
	TypeRAGASSample               = "ragas-sample"
	TypeReEmbedUserMemory         = "re-embed-user-memory"
)
