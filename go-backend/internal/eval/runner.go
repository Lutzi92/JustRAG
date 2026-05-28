package eval

import (
	"context"
	"fmt"
	"strconv"
	"time"
)

// Searcher is the minimum interface RunEval needs. vector.SearchService is
// adapted to this in cmd/eval/main.go.
type Searcher interface {
	// Search returns up to k retrieved chunks for q, ordered by descending
	// relevance. Callers must not assume more than k entries even if the
	// underlying service returned more.
	Search(ctx context.Context, q Question, k int) ([]RetrievedChunk, error)
}

// agentTracer is the optional surface RunEval uses to attach a per-question
// AgentTrace record onto each QuestionReport. Adapters that don't dispatch
// through an orchestrator (legacy search, plain ProductionContextAdapter)
// simply don't implement it; QuestionReport.Agent stays nil in that case.
type agentTracer interface {
	AgentTraceForQuestion(questionID string) *AgentTrace
}

// RunEval iterates questions sequentially (concurrency currently ignored —
// reserved for a future knob) and returns a populated Report.
//
// Per-question search errors are captured as QuestionReport.Error and DO NOT
// abort the run. The returned error is non-nil only if something structural
// (e.g. ctx cancellation) prevented iteration from completing.
func RunEval(ctx context.Context, searcher Searcher, questions []Question, k int, concurrency int) (Report, error) {
	if k <= 0 {
		return Report{}, fmt.Errorf("eval: invalid k=%d, must be > 0", k)
	}
	_ = concurrency // reserved; see plan
	rep := Report{
		GeneratedAt: time.Now().UTC(),
		K:           k,
		Questions:   make([]QuestionReport, 0, len(questions)),
	}
	tracer, _ := searcher.(agentTracer)
	for _, q := range questions {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		qr := runOne(ctx, searcher, q, k)
		if tracer != nil {
			qr.Agent = tracer.AgentTraceForQuestion(q.ID)
		}
		if qr.Error != "" {
			rep.Errors++
		}
		rep.Questions = append(rep.Questions, qr)
	}
	rep.Aggregate = Aggregate(rep.Questions, k)
	rep.RouteAggregates = AggregateByRoute(rep.Questions, k)
	rep.OrchestratorAggregates = AggregateByOrchestrator(rep.Questions, k)
	return rep, nil
}

func runOne(ctx context.Context, searcher Searcher, q Question, k int) QuestionReport {
	start := time.Now()
	chunks, err := searcher.Search(ctx, q, k)
	latencyMs := time.Since(start).Milliseconds()

	qr := QuestionReport{
		Question:  q,
		LatencyMs: latencyMs,
		Metrics:   PerQuestionMetrics{K: k},
	}
	if err != nil {
		qr.Error = err.Error()
		return qr
	}
	if k < len(chunks) {
		chunks = chunks[:k]
	}
	qr.Retrieved = chunks

	// Golden labels can name relevant files by file_id (UUID), file_name, or
	// both. We assign a synthetic canonical token to each labeled file (F0,
	// F1, …) and map every retrieved chunk to that token if its file_id or
	// file_name matches any alias. The metric helpers then operate on those
	// tokens unchanged.
	//
	// Rationale: file_ids are unstable across delete + re-upload — the file's
	// UUID is regenerated, so a golden authored purely by UUID loses every
	// matching chunk whenever the operator re-ingests. Names usually survive
	// re-ingest; authoring goldens by name (or both) makes the set
	// re-ingest-resilient.
	//
	// Caveat: if MustCiteFileIDs and MustCiteFileNames both reference the
	// same logical file, this loop can't dedupe them (no DB lookup here), so
	// the truth set ends up size 2 and recall deflates to 0.5 even when the
	// retrieved chunk is correct. Author goldens with EITHER ids or names per
	// file, not both — see the field doc on Question.
	aliasToCanonical, truth := buildTruth(q)
	tokens := make([]string, len(chunks))
	for i, c := range chunks {
		tokens[i] = canonicalToken(c, aliasToCanonical)
	}

	qr.Metrics = PerQuestionMetrics{
		K:              k,
		RecallAtK:      Recall(tokens, truth),
		PrecisionAtK:   Precision(tokens, truth),
		ReciprocalRank: ReciprocalRank(tokens, truth),
		NDCGAtK:        NDCG(tokens, truth),
	}
	return qr
}

// buildTruth assigns a synthetic canonical token (F0, F1, …) to every
// distinct alias in the question's must_cite lists. Returns the alias→token
// map plus the truth set of canonical tokens. File-id aliases are stored
// bare; file-name aliases are stored with a "name:" prefix to keep the two
// namespaces disjoint.
func buildTruth(q Question) (map[string]string, map[string]struct{}) {
	aliases := make(map[string]string, len(q.MustCiteFileIDs)+len(q.MustCiteFileNames))
	next := 0
	register := func(key string) {
		if _, ok := aliases[key]; ok {
			return
		}
		aliases[key] = "F" + strconv.Itoa(next)
		next++
	}
	for _, id := range q.MustCiteFileIDs {
		register(id)
	}
	for _, name := range q.MustCiteFileNames {
		register("name:" + name)
	}
	truth := make(map[string]struct{}, len(aliases))
	for _, tok := range aliases {
		truth[tok] = struct{}{}
	}
	return aliases, truth
}

// canonicalToken returns the truth token a retrieved chunk maps to, or the
// empty string when neither its file_id nor file_name appears in the truth
// aliases. Empty tokens never match any non-empty truth entry, so they drop
// out of recall / precision / MRR / nDCG naturally.
func canonicalToken(c RetrievedChunk, aliases map[string]string) string {
	if c.FileID != "" {
		if tok, ok := aliases[c.FileID]; ok {
			return tok
		}
	}
	if c.FileName != "" {
		if tok, ok := aliases["name:"+c.FileName]; ok {
			return tok
		}
	}
	return ""
}

func toSet(xs []string) map[string]struct{} {
	s := make(map[string]struct{}, len(xs))
	for _, x := range xs {
		s[x] = struct{}{}
	}
	return s
}

// JudgeConfig controls optional LLM-as-judge evaluation. Zero value means
// "no judging" — RunEvalWithJudge produces only retrieval metrics.
type JudgeConfig struct {
	// Enabled turns the judge layer on. Answer generation and all three judge
	// prompts run per question, sequentially.
	Enabled bool

	// Judge runs the three judge prompts. Required when Enabled is true.
	Judge *Judge

	// AnswerCompleter generates the AI answer per question. Required when
	// Enabled is true.
	AnswerCompleter Completer

	// ContentLoader loads chunk content + file-name slices for a given
	// retrieval result. Required when Enabled is true. Return values must
	// have len equal to len(chunks) and ordering matching input.
	ContentLoader func(ctx context.Context, chunks []RetrievedChunk) (contents []string, fileNames []string, err error)
}

// RunEvalWithJudge is the judge-aware variant. When cfg.Enabled is false,
// behaves identically to RunEval. Keeps the Phase 2.1 RunEval signature
// unchanged.
func RunEvalWithJudge(ctx context.Context, searcher Searcher, questions []Question, k int, concurrency int, cfg JudgeConfig) (Report, error) {
	if k <= 0 {
		return Report{}, fmt.Errorf("eval: invalid k=%d, must be > 0", k)
	}
	if cfg.Enabled {
		if cfg.Judge == nil {
			return Report{}, fmt.Errorf("eval: cfg.Judge is required when cfg.Enabled is true")
		}
		if cfg.AnswerCompleter == nil {
			return Report{}, fmt.Errorf("eval: cfg.AnswerCompleter is required when cfg.Enabled is true")
		}
		if cfg.ContentLoader == nil {
			return Report{}, fmt.Errorf("eval: cfg.ContentLoader is required when cfg.Enabled is true")
		}
	}
	_ = concurrency
	rep := Report{
		GeneratedAt: time.Now().UTC(),
		K:           k,
		Questions:   make([]QuestionReport, 0, len(questions)),
	}
	tracer, _ := searcher.(agentTracer)
	for _, q := range questions {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		qr := runOne(ctx, searcher, q, k)
		if tracer != nil {
			qr.Agent = tracer.AgentTraceForQuestion(q.ID)
		}

		if qr.Error == "" && cfg.Enabled {
			contents, fileNames, err := cfg.ContentLoader(ctx, qr.Retrieved)
			switch {
			case err != nil:
				qr.Judge = &JudgeMetrics{JudgeErrors: []string{fmt.Sprintf("content_loader: %v", err)}}
			default:
				qr.Contents = contents
				answer, aerr := GenerateAnswer(ctx, cfg.AnswerCompleter, q.Question, qr.Retrieved, contents, fileNames, q.Language)
				if aerr != nil {
					qr.Judge = &JudgeMetrics{JudgeErrors: []string{fmt.Sprintf("answer: %v", aerr)}}
				} else {
					jm := cfg.Judge.Evaluate(ctx, q, answer, qr.Retrieved, contents)
					qr.Judge = &jm
				}
			}
		}

		if qr.Error != "" {
			rep.Errors++
		}
		rep.Questions = append(rep.Questions, qr)
	}
	rep.Aggregate = Aggregate(rep.Questions, k)
	rep.RouteAggregates = AggregateByRoute(rep.Questions, k)
	rep.OrchestratorAggregates = AggregateByOrchestrator(rep.Questions, k)
	return rep, nil
}
