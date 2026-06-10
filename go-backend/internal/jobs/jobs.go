// Package jobs defines the canonical queue names and task type constants used
// across the application. Feature packages import jobs to reference these
// constants without pulling in the worker package, breaking potential import
// cycles.
package jobs

import "time"

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
	TypeGenerateGoldenSet         = "generate-golden-set"
)

// taskTimeouts maps each task type to a generous per-task asynq.Timeout.
// Without a timeout a task wedged on I/O that ignores cancellation holds a
// worker slot until process restart; with one, asynq cancels the handler
// context after the deadline and frees the slot. Values are deliberately
// far above observed p99 runtimes — they exist to reclaim wedged slots,
// not to enforce SLOs.
var taskTimeouts = map[string]time.Duration{
	TypeFileProcessing:            2 * time.Hour, // large PDFs + OCR + enrichment + RAPTOR
	TypeTextProcessing:            1 * time.Hour,
	TypeURLProcessing:             30 * time.Minute,
	TypeRSSPoll:                   30 * time.Minute,
	TypeReEmbedding:               2 * time.Hour, // same pipeline as file-processing
	TypeResearchExecution:         2 * time.Hour, // multi-iteration agent loops
	TypeAcademicResearchExecution: 2 * time.Hour,
	TypeContentGeneration:         1 * time.Hour,
	TypeConfluenceSync:            4 * time.Hour, // full-space crawls
	TypeCrawl:                     2 * time.Hour,
	TypeEvalRun:                   2 * time.Hour, // matches the explicit value at the enqueue site
	TypeRAGASSample:               15 * time.Minute,
	TypeReEmbedUserMemory:         1 * time.Hour,
	TypeGenerateGoldenSet:         2 * time.Hour,
}

// TimeoutFor returns the asynq.Timeout duration for taskType. Unknown task
// types get a conservative 2h default so a forgotten map entry still yields
// a bounded slot hold instead of an unbounded one.
func TimeoutFor(taskType string) time.Duration {
	if d, ok := taskTimeouts[taskType]; ok {
		return d
	}
	return 2 * time.Hour
}
