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

import (
	"time"

	"github.com/justrag/go-backend/internal/chat"
)

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
	// TabularExpected is the optional Ruling R75 ground-truth flag for
	// TabularRouterRates: true means this question targets a spreadsheet
	// region the deterministic tabular SQL router is expected to engage
	// on (a materialised table region); false means it deliberately does
	// not (a form-field or dropdown-list region rendered as text, or a
	// negative case) and must be excluded from both the fire-rate
	// numerator and denominator even if the router happens to fire on it
	// anyway. A *bool (not bool) so "absent" is distinguishable from
	// "false" — TabularRouterRates only switches from the legacy
	// query_type-based eligibility rule to this flag when at least one
	// question in the run carries it.
	TabularExpected *bool `json:"tabular_expected,omitempty"`
	// Turns marks this row as a conversation: a sequence of follow-up
	// turns sharing KbID/Language, expanded by ExpandTurns into one
	// Question per turn (ID "<id>#t<n>") carrying History. When Turns is
	// non-empty, the top-level Question/MustCite* fields are not
	// required — ground truth lives per turn (see validateTurns).
	Turns []Turn `json:"turns,omitempty"`
	// History is populated by ExpandTurns on the per-turn Questions it
	// produces; it is never authored directly in a golden-set file. The
	// json tag is kept so a report round-trips it.
	History []HistoryEntry `json:"history,omitempty"`
	// TurnKind is populated by ExpandTurns on the per-turn Questions it
	// produces (copied from the originating Turn.Kind); it is never
	// authored directly. The json tag is kept so a report round-trips it.
	TurnKind string `json:"turn_kind,omitempty"`
	// ExpectedPoints is the optional W4-R5 ground-truth list of short
	// statements a complete answer must contain (2-6 recommended). When
	// non-empty and --judge is on, a fourth "coverage" LLM judge scores
	// coverage = covered/len(ExpectedPoints) via one structured call
	// returning a boolean per point (same tolerant truncate/pad-with-
	// warning parsing as context_precision, W4-R1). Empty (the default)
	// skips the coverage judge entirely — JudgeMetrics.Coverage stays
	// nil. The loader caps this at 12 points, 300 runes each.
	ExpectedPoints []string `json:"expected_points,omitempty"`
}

// HistoryEntry is one prior conversation turn, replayed as chat history so
// a multi-turn golden question can be scored without any real chat rows.
type HistoryEntry struct {
	Role    string `json:"role"` // "user" | "ai"
	Content string `json:"content"`
	// Sources lists the file names the "ai" message cited, so an
	// answer_ref follow-up can default its ground truth to them.
	Sources []string `json:"sources,omitempty"`
}

// Turn is one turn of an authored multi-turn conversation row (Question.Turns).
type Turn struct {
	Question string `json:"question"`
	// Kind labels the turn's conversational shape; one of the TurnKind*
	// constants. See ValidTurnKind.
	Kind      string `json:"kind"`
	QueryType string `json:"query_type,omitempty"`
	// MustCiteFileNames is required for every kind except answer_ref,
	// where it defaults to the previous turn's AnswerSources (a
	// retrieval-free reformat carries over the prior answer's sources).
	MustCiteFileNames []string `json:"must_cite_file_names,omitempty"`
	// Answer is the assistant reply used as history for later turns.
	Answer        string   `json:"answer,omitempty"`
	AnswerSources []string `json:"answer_sources,omitempty"`
	Notes         string   `json:"notes,omitempty"`
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
	// JudgeWarnings records non-fatal tolerance events (e.g. a
	// context_precision boolean-count mismatch that was truncated/padded
	// rather than dropped). Unlike JudgeErrors, a warning does not leave
	// the corresponding metric pointer nil.
	JudgeWarnings []string `json:"judge_warnings,omitempty"`
	Answer        string   `json:"answer,omitempty"`
	// Coverage is the W4-R5 fourth judge's score over Question.ExpectedPoints
	// (covered/len(ExpectedPoints)). Nil when the question carries no
	// ExpectedPoints (judge not called) or when the coverage call failed
	// (recorded in JudgeErrors instead).
	Coverage *float64 `json:"coverage,omitempty"`
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
	// Conflicts is the W5-R7 conflict / supersession report the chat
	// pipeline produced for this question, in EXACTLY the shape a chat
	// turn persists and streams (chat.ConflictsForWire — the bare array).
	// Nil/absent means the pass did not run (flag off, fewer than two
	// distinct files, abstain, timeout) or ran and found nothing, which is
	// why the flag rate is measured as "questions with >= 1 entry" and not
	// from the key's presence. Omitted from the JSON when empty, so every
	// pre-Wave-5 report shape stays byte-stable.
	Conflicts []chat.MessageConflict `json:"conflicts,omitempty"`
	// Contents holds the chunk text per retrieved chunk, in the same
	// order as Retrieved. JSON-suppressed via `json:"-"` so the on-disk
	// Report shape is unchanged — Phase 3 §G's ExportRAGAS uses this
	// in-memory slice to populate the "contexts" array of the RAGAS
	// dataset shape, then it's discarded with the Report at process
	// exit.
	Contents []string `json:"-"`
	// CondensedQuery is the standalone query a multi-turn adapter
	// (MultiTurnAdapter) condensed this turn's Question into before
	// retrieval, so a report reviewer can see what was actually searched
	// for. Empty when the searcher doesn't condense (single-turn
	// questions, legacy/production adapters without history) — existing
	// on-disk report shapes stay byte-stable.
	CondensedQuery string `json:"condensed_query,omitempty"`
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
	// MeanCoverage is the W4-R5 coverage judge's mean over questions that
	// carry ExpectedPoints, averaged over non-nil values only. Nil when
	// no question in the run had a non-nil Coverage.
	MeanCoverage *float64 `json:"mean_coverage,omitempty"`
	JudgedCount  int      `json:"judged_count,omitempty"`
	// Per-metric judged counts: how many questions actually contributed a
	// non-nil value to the corresponding mean above. JudgedCount alone
	// hides a metric-specific drop (e.g. an unparseable answer-relevance
	// response that left faithfulness/precision intact for the same
	// question) — see W4-R2.
	FaithfulnessN     int `json:"faithfulness_n,omitempty"`
	AnswerRelevanceN  int `json:"answer_relevance_n,omitempty"`
	ContextPrecisionN int `json:"context_precision_n,omitempty"`
	// CoverageN is how many questions actually contributed a non-nil
	// Coverage value to MeanCoverage (i.e. carried ExpectedPoints AND the
	// coverage judge call succeeded).
	CoverageN int `json:"coverage_n,omitempty"`
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
	// TabularRouterFireRate is fired / (questions whose golden query_type
	// is lookup or complex_reasoning — the golden schema has no
	// "aggregation" label, see TabularRouterRates) — how often the
	// deterministic spreadsheet router engaged on a question shape it's
	// built for. Nil when no question in the run carries an eligible
	// query_type, so legacy report shapes stay byte-stable.
	TabularRouterFireRate *float64 `json:"tabular_router_fire_rate,omitempty"`
	// TabularSQLErrorRate is (sql_error + validator_rejected) / fired —
	// of the turns the router actually attempted, how often it ended in
	// an unusable statement. Nil when no question fired.
	TabularSQLErrorRate *float64 `json:"tabular_sql_error_rate,omitempty"`
	// TurnKindAggregates buckets retrieval metrics by the golden set's
	// per-turn TurnKind label (Wave 2 Task 3 multi-turn replay). Nil when
	// no question in the run carries a TurnKind (i.e. no golden row used
	// `turns`), so legacy report shapes stay byte-stable. Mirrors
	// RouteAggregates/OrchestratorAggregates in shape.
	TurnKindAggregates map[string]AggregateMetrics `json:"turn_kind_aggregates,omitempty"`
}
