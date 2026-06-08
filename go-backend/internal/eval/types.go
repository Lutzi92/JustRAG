// Package eval provides an offline evaluation harness for the RAG
// retrieval pipeline. It loads a JSONL golden set, runs each question
// through the production vector.SearchService, and computes standard
// retrieval metrics (recall@k, precision@k, MRR, nDCG@k).
//
// This package is intentionally judge-free: all metrics are deterministic
// functions of the retrieved chunk list and the ground-truth file-id set.
// LLM-as-judge evaluation (faithfulness, answer relevance) belongs to a
// future phase.
package eval

import "time"

// Question is one entry in the golden set.
type Question struct {
	ID       string `json:"id"`
	Question string `json:"question"`
	KbID     string `json:"kb_id"`
	Language string `json:"language"`
	// MustCiteFileIDs and MustCiteFileNames are both ground-truth keys for
	// recall scoring. A retrieved chunk counts as relevant if EITHER its
	// file_id is in MustCiteFileIDs OR its file name is in
	// MustCiteFileNames. UUIDs change when a file is deleted + re-uploaded;
	// names typically don't. Authoring goldens by name (or both) makes the
	// set survive a re-ingest without manual UUID rewiring. At least one of
	// the two lists must be non-empty.
	MustCiteFileIDs   []string `json:"must_cite_file_ids,omitempty"`
	MustCiteFileNames []string `json:"must_cite_file_names,omitempty"`
	// QueryType optionally labels the question's intent so per-route
	// metrics can be computed. Allowed values: "lookup", "enumeration",
	// "global_synthesis", "complex_reasoning". Empty = unlabeled.
	QueryType string `json:"query_type,omitempty"`
	Notes     string `json:"notes,omitempty"`
	// ExpectedTools is the optional Phase 2 §2.2 ground-truth list of
	// MCP tool names the agent should have invoked to answer correctly
	// (e.g. ["kb_search"], or ["kb_search","web_search"] for a question
	// requiring external lookup). Empty = the trajectory eval skips
	// tool-selection scoring for this question — the judge falls back
	// to "is the agent's call set defensible".
	ExpectedTools []string `json:"expected_tools,omitempty"`
	// ExpectedKBIDs is the AP-A4 ground-truth list of KB UUIDs that
	// the sub-KB router should select for this question. Empty (the
	// default) implies single-KB intent — the router-eval falls back
	// to []string{KbID}, so existing single-KB golden sets need no
	// migration. Multi-KB rows list every relevant KB in order of
	// expected primary→secondary; the AP-A4 metric is "is the router's
	// top-1 in this list" plus "Jaccard overlap with the full list" for
	// fan-out scoring.
	ExpectedKBIDs []string `json:"expected_kb_ids,omitempty"`
}

// RetrievedChunk is the minimal view the evaluator needs of a search hit.
type RetrievedChunk struct {
	FileID string `json:"file_id"`
	// FileName is the file's user-facing name. Populated by Searcher
	// implementations alongside FileID so the runner can match goldens
	// authored with must_cite_file_names against the retrieved set.
	// Empty is OK — runner falls back to file_id-only matching.
	FileName string  `json:"file_name,omitempty"`
	Score    float64 `json:"score"`
	// ChunkIndex and TotalChunks describe the chunk's position
	// within its source file. Populated by the Searcher
	// implementation from the chunk's `metadata` JSONB
	// (chunkIndex + totalChunks fields written by the processor).
	// Zero on both means "metadata unavailable" — the depth-bucket
	// aggregator skips such chunks. Position-aware retrieval
	// analysis (P9) needs these; the existing recall / MRR / nDCG
	// metrics don't.
	ChunkIndex  int `json:"chunk_index,omitempty"`
	TotalChunks int `json:"total_chunks,omitempty"`
}

// PerQuestionMetrics holds metric values for a single question at a fixed k.
type PerQuestionMetrics struct {
	K              int     `json:"k"`
	RecallAtK      float64 `json:"recall_at_k"`
	PrecisionAtK   float64 `json:"precision_at_k"`
	ReciprocalRank float64 `json:"reciprocal_rank"`
	NDCGAtK        float64 `json:"ndcg_at_k"`
}

// JudgeMetrics holds LLM-as-judge evaluation results for a single question.
// Pointer fields distinguish "not evaluated" (nil) from "evaluated to 0" (zero-valued).
type JudgeMetrics struct {
	Faithfulness     *float64 `json:"faithfulness,omitempty"`
	AnswerRelevance  *float64 `json:"answer_relevance,omitempty"`
	ContextPrecision *float64 `json:"context_precision,omitempty"`
	JudgeErrors      []string `json:"judge_errors,omitempty"`
	Answer           string   `json:"answer,omitempty"`
}

// QuestionReport is the per-question row in the final report.
type QuestionReport struct {
	Question  Question           `json:"question"`
	Retrieved []RetrievedChunk   `json:"retrieved"`
	Metrics   PerQuestionMetrics `json:"metrics"`
	Error     string             `json:"error,omitempty"`
	LatencyMs int64              `json:"latency_ms"`
	Judge     *JudgeMetrics      `json:"judge,omitempty"`
	// Agent records which orchestrator/specialist/tools handled this
	// question during eval. Populated by OrchestratorDispatchAdapter;
	// nil when the eval ran against an adapter that doesn't dispatch
	// through an orchestrator (legacy retrieval-only, plain
	// ProductionContextAdapter) so existing on-disk reports stay
	// byte-stable.
	Agent *AgentTrace `json:"agent,omitempty"`
	// Contents holds the chunk text per retrieved chunk, in the same
	// order as Retrieved. JSON-suppressed via `json:"-"` so the on-disk
	// Report shape is unchanged — Phase 3 §G's ExportRAGAS uses this
	// in-memory slice to populate the "contexts" array of the RAGAS
	// dataset shape, then it's discarded with the Report at process
	// exit.
	Contents []string `json:"-"`
}

// AggregateMetrics summarizes metric values across all non-errored questions at a fixed k.
type AggregateMetrics struct {
	K                    int      `json:"k"`
	Count                int      `json:"count"`
	MeanRecall           float64  `json:"mean_recall"`
	MeanPrecision        float64  `json:"mean_precision"`
	MRR                  float64  `json:"mrr"`
	MeanNDCG             float64  `json:"mean_ndcg"`
	P50Recall            float64  `json:"p50_recall"`
	P95Recall            float64  `json:"p95_recall"`
	MeanFaithfulness     *float64 `json:"mean_faithfulness,omitempty"`
	MeanAnswerRelevance  *float64 `json:"mean_answer_relevance,omitempty"`
	MeanContextPrecision *float64 `json:"mean_context_precision,omitempty"`
	JudgedCount          int      `json:"judged_count,omitempty"`
}

// Report is the full result of an evaluation run.
type Report struct {
	GeneratedAt     time.Time                   `json:"generated_at"`
	GoldenPath      string                      `json:"golden_path"`
	K               int                         `json:"k"`
	Questions       []QuestionReport            `json:"questions"`
	Aggregate       AggregateMetrics            `json:"aggregate"`
	RouteAggregates map[string]AggregateMetrics `json:"route_aggregates,omitempty"`
	// OrchestratorAggregates buckets retrieval metrics by which
	// orchestrator handled each question. Populated when at least one
	// question has a non-nil Agent. Mirrors RouteAggregates in shape so
	// consumers render both with the same code path.
	OrchestratorAggregates map[string]AggregateMetrics `json:"orchestrator_aggregates,omitempty"`
	Errors                 int                         `json:"errors"`
	// DepthBuckets is the position-aware retrieval analysis (P9):
	// per-quartile counts of relevant vs non-relevant chunks
	// retrieved, computed only when the eval CLI is invoked with
	// --depth-buckets. Nil when not requested so existing on-disk
	// report shapes stay byte-stable for consumers diffing runs.
	DepthBuckets *DepthBucketReport `json:"depth_buckets,omitempty"`
	// RoutingAccuracy scores the production query-type classifier against
	// the golden QueryType labels. Non-nil only when the eval dispatched
	// through an orchestrator (Agent populated) AND at least one question
	// carries a golden QueryType; nil otherwise so legacy report shapes
	// stay byte-stable.
	RoutingAccuracy *RoutingAccuracyReport `json:"routing_accuracy,omitempty"`
}
