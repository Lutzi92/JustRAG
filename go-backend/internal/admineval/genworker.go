package admineval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/justrag/go-backend/internal/eval"
	"github.com/justrag/go-backend/internal/eval/gen"
	"github.com/justrag/go-backend/internal/jobs"
	"github.com/justrag/go-backend/internal/logctx"
)

// EnqueueGenerate posts the async corpus-generation task.
func EnqueueGenerate(ctx context.Context, client enqueuer, jobID uuid.UUID) error {
	payload, err := json.Marshal(jobs.GenerateGoldenSetPayload{JobID: jobID.String()})
	if err != nil {
		return fmt.Errorf("enqueue generate: marshal: %w", err)
	}
	_, err = client.EnqueueContext(ctx,
		asynq.NewTask(jobs.TypeGenerateGoldenSet, payload),
		asynq.Queue(jobs.QueueHeavy),
		asynq.Timeout(30*time.Minute),
		asynq.MaxRetry(0),
	)
	if err != nil {
		return fmt.Errorf("enqueue generate: %w", err)
	}
	return nil
}

// GenWorker runs corpus-based golden-set generation jobs.
type GenWorker struct {
	jobStore       *eval.GenJobStore
	goldenSetStore *eval.GoldenSetStore
	build          func(kbID, lang, model string) *gen.Generator
}

// NewGenWorker constructs a GenWorker. build is called once per job to
// construct a Generator bound to the requested KB, language, and model.
func NewGenWorker(jobStore *eval.GenJobStore, goldenSetStore *eval.GoldenSetStore, build func(kbID, lang, model string) *gen.Generator) *GenWorker {
	return &GenWorker{jobStore: jobStore, goldenSetStore: goldenSetStore, build: build}
}

// HandleGenerate is the asynq task handler for TypeGenerateGoldenSet tasks.
//
// Failure handling: once the row is marked running, all subsequent errors are
// recorded via MarkFailed and the handler returns nil so asynq does NOT retry
// the job. Terminal-status writes use a detached context (fresh
// context.Background() + 15 s timeout) so a cancelled or timed-out task
// context cannot prevent persisting the outcome.
func (w *GenWorker) HandleGenerate(ctx context.Context, t *asynq.Task) error {
	var p jobs.GenerateGoldenSetPayload
	if err := json.Unmarshal(t.Payload(), &p); err != nil {
		return fmt.Errorf("generate: unmarshal payload: %w", err)
	}
	jobID, err := uuid.Parse(p.JobID)
	if err != nil {
		return fmt.Errorf("generate: bad job id: %w", err)
	}

	log := logctx.From(ctx).With("job_id", jobID)

	// detachedWriteCtx returns a fresh context.Background()-derived context
	// with a short deadline for terminal-status DB writes. Mirrors the same
	// pattern in (*Worker).HandleRun so a cancelled/timed-out task context
	// cannot prevent us from persisting the final status.
	detachedWriteCtx := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), 15*time.Second)
	}

	job, err := w.jobStore.Get(ctx, jobID)
	if err != nil {
		errMsg := fmt.Sprintf("load job: %v", err)
		wctx, wcancel := detachedWriteCtx()
		if mfErr := w.jobStore.MarkFailed(wctx, jobID, errMsg); mfErr != nil {
			log.Error("generate.mark_failed", "error", mfErr)
		}
		wcancel()
		log.Error("generate.load_failed", "error", err)
		return nil // do not re-enqueue
	}

	var params eval.GenJobParams
	if err := json.Unmarshal(job.Params, &params); err != nil {
		wctx, wcancel := detachedWriteCtx()
		_ = w.jobStore.MarkFailed(wctx, jobID, "bad params: "+err.Error())
		wcancel()
		return nil
	}

	if err := w.jobStore.MarkRunning(ctx, jobID); err != nil {
		log.Warn("generate: mark running failed", "error", err)
	}

	log.Info("generate.started", "kb_id", job.KBID, "lang", params.Lang, "model", params.Model)

	g := w.build(job.KBID.String(), params.Lang, params.Model)
	questions, stats, genErr := g.GenerateAll(ctx, job.KBID.String(), params.Lang,
		gen.Counts{
			Lookup:      params.Counts.Lookup,
			Complex:     params.Counts.Complex,
			Enumeration: params.Counts.Enumeration,
			MultiHop:    params.Counts.MultiHop,
			MaxFiles:    params.MaxFiles,
		})
	if genErr != nil {
		wctx, wcancel := detachedWriteCtx()
		_ = w.jobStore.MarkFailed(wctx, jobID, genErr.Error())
		wcancel()
		log.Error("generate.gen_failed", "error", genErr)
		return nil
	}
	if len(questions) == 0 {
		wctx, wcancel := detachedWriteCtx()
		_ = w.jobStore.MarkFailed(wctx, jobID, "no questions produced")
		wcancel()
		log.Warn("generate.no_questions", "kb_id", job.KBID)
		return nil
	}

	content, err := json.Marshal(questions)
	if err != nil {
		wctx, wcancel := detachedWriteCtx()
		_ = w.jobStore.MarkFailed(wctx, jobID, "marshal questions: "+err.Error())
		wcancel()
		return nil
	}
	sum := sha256.Sum256(content)
	gsID, _, err := w.goldenSetStore.Create(ctx, eval.GoldenSet{
		Name: params.Name,
		Description: fmt.Sprintf("auto-generated from corpus — kb=%s lookup=%d complex=%d enumeration=%d multihop=%d",
			job.KBID, stats.Lookup, stats.Complex, stats.Enumeration, stats.MultiHop),
		Content:       json.RawMessage(content),
		ContentHash:   hex.EncodeToString(sum[:]),
		QuestionCount: len(questions),
		CreatedBy:     job.CreatedBy,
		KBID:          job.KBID, // generated set belongs to the corpus KB (strictly per-KB)
	})
	if err != nil {
		wctx, wcancel := detachedWriteCtx()
		_ = w.jobStore.MarkFailed(wctx, jobID, "save golden set: "+err.Error())
		wcancel()
		log.Error("generate.save_failed", "error", err)
		return nil
	}

	wctx, wcancel := detachedWriteCtx()
	completeErr := w.jobStore.MarkCompleted(wctx, jobID, gsID)
	wcancel()
	if completeErr != nil {
		log.Error("generate.mark_completed", "error", completeErr)
		// Rescue into a terminal failed state so the job is never permanently
		// stuck in 'running' (mirrors the same rescue pattern in HandleRun).
		rwctx, rwcancel := detachedWriteCtx()
		if mfErr := w.jobStore.MarkFailed(rwctx, jobID, fmt.Sprintf("mark_completed failed: %v", completeErr)); mfErr != nil {
			log.Error("generate.mark_failed_after_complete", "error", mfErr)
		}
		rwcancel()
		return nil
	}

	log.Info("generate.finished",
		"kb_id", job.KBID,
		"golden_set_id", gsID,
		"questions", len(questions),
		"lookup", stats.Lookup,
		"complex", stats.Complex,
		"enumeration", stats.Enumeration,
		"multihop", stats.MultiHop,
	)
	return nil
}
