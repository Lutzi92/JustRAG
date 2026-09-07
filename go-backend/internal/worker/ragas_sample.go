package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hibiken/asynq"

	"github.com/justrag/go-backend/internal/ai"
	"github.com/justrag/go-backend/internal/eval"
	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/observability"
	"github.com/justrag/go-backend/internal/ragassamples"
)

// NewRAGASSampleHandler is the test-friendly factory: it takes a Completer
// directly so unit tests can supply a deterministic fake without spinning up
// a resolver. Production code should call NewRAGASSampleHandlerForResolver,
// which constructs a per-sample Completer adapter pinned to the payload's KbID.
func NewRAGASSampleHandler(completer eval.Completer, store ragassamples.Store) asynq.HandlerFunc {
	judge := eval.NewJudge(completer)
	return func(ctx context.Context, task *asynq.Task) error {
		var payload jobs.RAGASSamplePayload
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			return fmt.Errorf("ragas_sample: unmarshal payload: %w", err)
		}
		runJudge(ctx, judge, completer, store, payload)
		return nil
	}
}

// NewRAGASSampleHandlerForResolver is the production factory: it wraps the
// per-KB chat-model resolver in a per-sample Completer adapter. Each task
// constructs its own adapter so the resolver's KbID-keyed model selection
// takes effect — different KBs may run the judge against different models.
// store may be nil (persistence not wired, e.g. a worker with no main pool),
// in which case the task degrades to the pre-0072 Prometheus-only behaviour.
func NewRAGASSampleHandlerForResolver(resolver *ai.ConfigResolver, store ragassamples.Store) asynq.HandlerFunc {
	return func(ctx context.Context, task *asynq.Task) error {
		var payload jobs.RAGASSamplePayload
		if err := json.Unmarshal(task.Payload(), &payload); err != nil {
			return fmt.Errorf("ragas_sample: unmarshal payload: %w", err)
		}
		completer := perSampleCompleter{resolver: resolver, kbID: payload.KbID}
		judge := eval.NewJudge(completer)
		runJudge(ctx, judge, completer, store, payload)
		return nil
	}
}

// judgeModelNamer is implemented by completers that can report which model
// their judge prompts ran against. The name is persisted per sample because a
// score is only comparable against other scores from the same judge: a
// deployment that retunes model_tier_fast (or a KB with its own chat model)
// otherwise produces a silent level shift in the historical means.
type judgeModelNamer interface {
	JudgeModel(ctx context.Context) string
}

// judgeModelName returns the completer's model, or "" when it cannot name one
// (the test factory's fake, or a resolver failure). An unnamed judge still
// writes its row — the scores are worth more than the attribution.
func judgeModelName(ctx context.Context, completer eval.Completer) string {
	if n, ok := completer.(judgeModelNamer); ok {
		return n.JudgeModel(ctx)
	}
	return ""
}

// nonEmpty maps an empty id to nil so the nullable columns land as SQL NULL
// rather than as a cast error on "".
func nonEmpty(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

// runJudge is the shared body of both factories. Extracted so the
// outcome-classification and telemetry-emission logic has a single source
// of truth — divergence between test and production paths would be a
// silent bug.
func runJudge(ctx context.Context, judge *eval.Judge, completer eval.Completer, store ragassamples.Store, payload jobs.RAGASSamplePayload) {
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

	// Persist the sample (migration 0072). The histograms above stay the
	// alerting surface; the row is the attribution surface behind them — which
	// KB, which message, which judge model, and (for an all-nil row) why.
	//
	// A write failure is logged and swallowed: returning an error would make
	// asynq retry the task, and every retry re-runs the three paid judge
	// prompts to produce the same row. A missing row is a reporting gap, not a
	// reason to spend the tokens again.
	if store == nil {
		return
	}
	sample := ragassamples.Sample{
		MessageID:        nonEmpty(payload.MessageID),
		KbID:             nonEmpty(payload.KbID),
		Faithfulness:     metrics.Faithfulness,
		AnswerRelevance:  metrics.AnswerRelevance,
		ContextPrecision: metrics.ContextPrecision,
		// Always nil here: the coverage judge only runs on a question that
		// carries expected points, which a sampled production turn never has.
		Coverage:    metrics.Coverage,
		JudgeModel:  judgeModelName(ctx, completer),
		JudgeErrors: metrics.JudgeErrors,
		SampledAt:   time.Now(),
	}
	if err := store.Insert(ctx, sample); err != nil {
		logctx.From(ctx).Warn("ragas_sample: persisting the sample failed",
			"message_id", payload.MessageID,
			"kb_id", payload.KbID,
			"error", err,
		)
	}
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

// JudgeModel reports the model this completer's judge prompts run against, so
// the persisted row records which judge produced the scores. It resolves the
// same way Complete does (the KB's chat model, no override); the resolver
// caches, so this is not a second round trip in practice. A resolve failure
// yields "" rather than an error — an unattributed row still beats no row.
func (c perSampleCompleter) JudgeModel(ctx context.Context) string {
	config, err := c.resolver.Resolve(ctx, c.kbID)
	if err != nil {
		return ""
	}
	return config.ChatModel
}
