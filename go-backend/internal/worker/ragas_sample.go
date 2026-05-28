package worker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hibiken/asynq"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/eval"
	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/observability"
)

// NewRAGASSampleHandler is the test-friendly factory: it takes a Completer
// directly so unit tests can supply a deterministic fake without spinning up
// a resolver. Production code should call NewRAGASSampleHandlerForResolver,
// which constructs a per-sample Completer adapter pinned to the payload's KbID.
func NewRAGASSampleHandler(completer eval.Completer) asynq.HandlerFunc {
	judge := eval.NewJudge(completer)
	return func(ctx context.Context, task *asynq.Task) error {
		var payload jobs.RAGASSamplePayload
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			return fmt.Errorf("ragas_sample: unmarshal payload: %w", err)
		}
		runJudge(ctx, judge, payload)
		return nil
	}
}

// NewRAGASSampleHandlerForResolver is the production factory: it wraps the
// per-KB chat-model resolver in a per-sample Completer adapter. Each task
// constructs its own adapter so the resolver's KbID-keyed model selection
// takes effect — different KBs may run the judge against different models.
func NewRAGASSampleHandlerForResolver(resolver *ai.ConfigResolver) asynq.HandlerFunc {
	return func(ctx context.Context, task *asynq.Task) error {
		var payload jobs.RAGASSamplePayload
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			return fmt.Errorf("ragas_sample: unmarshal payload: %w", err)
		}
		completer := perSampleCompleter{resolver: resolver, kbID: payload.KbID}
		judge := eval.NewJudge(completer)
		runJudge(ctx, judge, payload)
		return nil
	}
}

// runJudge is the shared body of both factories. Extracted so the
// outcome-classification and telemetry-emission logic has a single source
// of truth — divergence between test and production paths would be a
// silent bug.
func runJudge(ctx context.Context, judge *eval.Judge, payload jobs.RAGASSamplePayload) {
	q := eval.Question{
		KbID:     payload.KbID,
		Language: payload.Language,
		Question: payload.Question,
	}
	chunks := make([]eval.RetrievedChunk, 0, len(payload.Chunks))
	contents := make([]string, 0, len(payload.Chunks))
	for _, c := range payload.Chunks {
		chunks = append(chunks, eval.RetrievedChunk{FileID: c.FileID, Score: c.Score})
		contents = append(contents, c.Content)
	}

	metrics := judge.Evaluate(ctx, q, payload.Answer, chunks, contents)

	// Outcome semantics: completed when at least one judge metric was
	// produced; error when all three failed.
	outcome := "completed"
	if metrics.Faithfulness == nil && metrics.AnswerRelevance == nil && metrics.ContextPrecision == nil {
		outcome = "error"
		logctx.From(ctx).Warn("ragas_sample: all judge prompts failed",
			"message_id", payload.MessageID,
			"errors", metrics.JudgeErrors,
		)
	} else if len(metrics.JudgeErrors) > 0 {
		// Partial failure — at least one metric came back. Log the
		// errors but keep outcome="completed" so the histograms count
		// is the correct denominator for the partial metrics.
		logctx.From(ctx).Info("ragas_sample: partial judge failure",
			"message_id", payload.MessageID,
			"errors", metrics.JudgeErrors,
		)
	}

	observability.RecordRAGASSample(outcome,
		metrics.Faithfulness, metrics.AnswerRelevance, metrics.ContextPrecision)
}

// perSampleCompleter is a thin eval.Completer wrapper around
// ai.GenerateCompletion bound to a specific KbID. Constructed per
// task so each sample resolves its KB's configured chat model.
type perSampleCompleter struct {
	resolver *ai.ConfigResolver
	kbID     string
}

func (c perSampleCompleter) Complete(ctx context.Context, prompt, systemPrompt string) (string, error) {
	// Mirror the eval CLI's judgeCompleterAdapter: judge prompts must run
	// at temperature=0.0 so the production-sampling histograms are
	// numerically comparable to the offline eval. ai.GenerateCompletion
	// hardcodes temperature=0.2 — fine for answer generation but wrong
	// for scored judge prompts. The empty modelOverride keeps the
	// per-KB chat model.
	res, err := ai.GenerateCompletionWithModelDeterministic(ctx, c.resolver, prompt, systemPrompt, c.kbID, false, "")
	if err != nil {
		return "", err
	}
	return res.Content, nil
}
