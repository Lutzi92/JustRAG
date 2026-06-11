package admineval

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/eval"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/siteconfig"
	storepkg "github.com/justrag/go-backend/internal/store"
)

// pathKB parses the {id} path value as a KB UUID, writing a 400 on failure.
func pathKB(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid kb id")
		return uuid.Nil, false
	}
	return id, true
}

// --- Golden sets ---

// ListGoldenSetsForKB → GET /api/kb/{id}/eval/golden-sets
func (h *Handler) ListGoldenSetsForKB(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID, ok := pathKB(w, r)
	if !ok {
		return
	}
	sets, err := h.goldenSetStore.ListByKB(ctx, kbID)
	if err != nil {
		logctx.From(ctx).Error("eval.kb.list_golden_sets", "error", err, "kb_id", kbID)
		httputil.WriteInternalErrorCtx(ctx, w, err)
		return
	}
	out := make([]GoldenSetSummary, 0, len(sets))
	for _, g := range sets {
		out = append(out, GoldenSetSummary{
			ID: g.ID, Name: g.Name, Description: g.Description,
			ContentHash: g.ContentHash, QuestionCount: g.QuestionCount, CreatedAt: g.CreatedAt,
		})
	}
	httputil.WriteJSONCtx(ctx, w, http.StatusOK, ListGoldenSetsResponse{GoldenSets: out})
}

// CreateGoldenSetForKB → POST /api/kb/{id}/eval/golden-sets (multipart upload)
func (h *Handler) CreateGoldenSetForKB(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID, ok := pathKB(w, r)
	if !ok {
		return
	}
	if err := r.ParseMultipartForm(5 << 20); err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "invalid multipart body: "+httputil.SanitizeError(err))
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "name is required")
		return
	}
	description := strings.TrimSpace(r.FormValue("description"))
	file, header, err := r.FormFile("file")
	if err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "file field is required")
		return
	}
	defer file.Close()
	if header.Size > (5 << 20) {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "file too large (max 5 MB)")
		return
	}
	raw, err := io.ReadAll(file)
	if err != nil {
		httputil.WriteInternalErrorCtx(ctx, w, err)
		return
	}
	questions, err := eval.ParseGoldenSetJSONL(bytes.NewReader(raw))
	if err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "jsonl parse error: "+httputil.SanitizeError(err))
		return
	}
	if len(questions) == 0 {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "fixture is empty")
		return
	}
	contentJSON, err := json.Marshal(questions)
	if err != nil {
		httputil.WriteInternalErrorCtx(ctx, w, err)
		return
	}
	var createdBy *uuid.UUID
	if user := auth.UserFromContext(ctx); user != nil {
		if id, perr := uuid.Parse(user.ID); perr == nil && id != uuid.Nil {
			createdBy = &id
		}
	}
	id, createdAt, err := h.goldenSetStore.Create(ctx, eval.GoldenSet{
		KBID: kbID, Name: name, Description: description, Content: contentJSON,
		ContentHash: eval.HashFixtureContent(raw), QuestionCount: len(questions), CreatedBy: createdBy,
	})
	if errors.Is(err, eval.ErrGoldenSetNameTaken) {
		httputil.WriteErrorCtx(ctx, w, http.StatusConflict, fmt.Sprintf("name %q is already taken", name))
		return
	}
	if err != nil {
		httputil.WriteInternalErrorCtx(ctx, w, err)
		return
	}
	httputil.WriteJSONCtx(ctx, w, http.StatusCreated, CreateGoldenSetResponse{
		ID: id, Name: name, ContentHash: eval.HashFixtureContent(raw), QuestionCount: len(questions), CreatedAt: createdAt,
	})
}

// getOwnedGoldenSet fetches a golden set and enforces it belongs to kbID.
// Returns nil + writes 404 when missing or owned by another KB.
func (h *Handler) getOwnedGoldenSet(w http.ResponseWriter, r *http.Request, kbID uuid.UUID, gsID uuid.UUID) *eval.GoldenSet {
	ctx := r.Context()
	gs, err := h.goldenSetStore.Get(ctx, gsID)
	if err != nil {
		if errors.Is(err, storepkg.ErrNotFound) {
			httputil.WriteErrorCtx(ctx, w, http.StatusNotFound, "golden set not found")
			return nil
		}
		httputil.WriteInternalErrorCtx(ctx, w, err)
		return nil
	}
	if gs.KBID != kbID {
		// Do not leak existence across KBs.
		httputil.WriteErrorCtx(ctx, w, http.StatusNotFound, "golden set not found")
		return nil
	}
	return gs
}

// GetGoldenSetForKB → GET /api/kb/{id}/eval/golden-sets/{gsId}
func (h *Handler) GetGoldenSetForKB(w http.ResponseWriter, r *http.Request) {
	kbID, ok := pathKB(w, r)
	if !ok {
		return
	}
	gsID, err := uuid.Parse(r.PathValue("gsId"))
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid golden set id")
		return
	}
	gs := h.getOwnedGoldenSet(w, r, kbID, gsID) // 404 on cross-KB or missing
	if gs == nil {
		return
	}
	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, gs)
}

// DeleteGoldenSetForKB → DELETE /api/kb/{id}/eval/golden-sets/{gsId}
func (h *Handler) DeleteGoldenSetForKB(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID, ok := pathKB(w, r)
	if !ok {
		return
	}
	gsID, err := uuid.Parse(r.PathValue("gsId"))
	if err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "invalid golden set id")
		return
	}
	if h.getOwnedGoldenSet(w, r, kbID, gsID) == nil {
		return
	}
	if _, err := h.goldenSetStore.Delete(ctx, gsID); err != nil {
		httputil.WriteInternalErrorCtx(ctx, w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- Runs ---

// kbCreateRunRequest is the KB-scoped run body (no kb_id — it comes from path).
type kbCreateRunRequest struct {
	GoldenSetID  uuid.UUID `json:"golden_set_id"`
	JudgeEnabled bool      `json:"judge_enabled"`
	TopK         int       `json:"top_k"`
	Label        string    `json:"label"`
}

// CreateRunForKB → POST /api/kb/{id}/eval/runs
func (h *Handler) CreateRunForKB(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID, ok := pathKB(w, r)
	if !ok {
		return
	}
	user := auth.UserFromContext(ctx)
	if user == nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusUnauthorized, "authentication required")
		return
	}

	var kreq kbCreateRunRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&kreq); err != nil {
			httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "invalid JSON: "+httputil.SanitizeError(err))
			return
		}
	}
	if kreq.GoldenSetID == uuid.Nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "golden_set_id is required")
		return
	}
	if kreq.TopK <= 0 {
		kreq.TopK = 10
	}

	// Golden set must belong to this KB.
	gs := h.getOwnedGoldenSet(w, r, kbID, kreq.GoldenSetID)
	if gs == nil {
		return
	}

	// Per-KB in-flight guard.
	active, err := h.store.HasActiveRun(ctx, kbID)
	if err != nil {
		httputil.WriteInternalErrorCtx(ctx, w, err)
		return
	}
	if active {
		httputil.WriteErrorCtx(ctx, w, http.StatusConflict, "an eval is already queued or running for this KB")
		return
	}

	// Merge per-KB overrides into the config snapshot so the eval tests live KB
	// settings. Falls back to the global reader when no override lister is wired.
	var snapReader siteConfigReader = h.cfgReader
	if h.kbOverrides != nil {
		overrides, oerr := h.kbOverrides.ListKBOverrides(ctx, kbID.String())
		if oerr != nil {
			logctx.From(ctx).Warn("eval.kb.create_run.override_load_failed", "kb_id", kbID, "error", oerr)
		} else if len(overrides) > 0 {
			snapReader = siteconfig.NewKBOverlay(h.cfgReader, overrides)
		}
	}
	snap, err := captureConfigSnapshot(ctx, snapReader)
	if err != nil {
		httputil.WriteInternalErrorCtx(ctx, w, err)
		return
	}
	snapJSON, err := json.Marshal(snap)
	if err != nil {
		httputil.WriteInternalErrorCtx(ctx, w, err)
		return
	}

	var triggeredBy *uuid.UUID
	if id, perr := uuid.Parse(user.ID); perr == nil && id != uuid.Nil {
		triggeredBy = &id
	}

	runID, err := h.store.Insert(ctx, eval.Run{
		KBID:           kbID,
		GoldenSetID:    &gs.ID,
		FixtureHash:    gs.ContentHash,
		ConfigSnapshot: json.RawMessage(snapJSON),
		JudgeEnabled:   kreq.JudgeEnabled,
		TopK:           kreq.TopK,
		Label:          kreq.Label,
		TriggeredBy:    triggeredBy,
	})
	if err != nil {
		httputil.WriteInternalErrorCtx(ctx, w, err)
		return
	}
	if err := EnqueueRun(ctx, h.asynqClient, runID); err != nil {
		if mfErr := h.store.MarkFailed(ctx, runID, "enqueue failed: "+err.Error()); mfErr != nil {
			logctx.From(ctx).Error("eval.kb.create_run.mark_failed", "run_id", runID, "error", mfErr)
		}
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "failed to enqueue eval run")
		return
	}
	httputil.WriteJSONCtx(ctx, w, http.StatusCreated, CreateRunResponse{ID: runID, Status: "queued"})
}

// ListRunsForKB → GET /api/kb/{id}/eval/runs (always filtered to the path KB)
func (h *Handler) ListRunsForKB(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID, ok := pathKB(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	opts := eval.ListOpts{
		Limit:  intParam(q, "limit", 50, 200),
		Offset: intParam(q, "offset", 0, 1_000_000),
		Status: q.Get("status"),
		KBID:   &kbID, // forced — ignore any body/query kb_id
	}
	runs, total, err := h.store.List(ctx, opts)
	if err != nil {
		httputil.WriteInternalErrorCtx(ctx, w, err)
		return
	}
	summaries := make([]RunSummary, 0, len(runs))
	for _, run := range runs {
		s := RunSummary{
			ID: run.ID, Label: run.Label, Status: run.Status, CreatedAt: run.CreatedAt,
			StartedAt: run.StartedAt, FinishedAt: run.FinishedAt, KBID: run.KBID,
			JudgeEnabled: run.JudgeEnabled, ErrorMessage: run.ErrorMessage,
		}
		if name, _, kbErr := h.kbStore.GetKBInfo(ctx, run.KBID); kbErr == nil {
			s.KBName = name
		}
		if len(run.Report) > 0 {
			s.Aggregate, s.RouteMeanRecall = summarize(run.Report)
		}
		summaries = append(summaries, s)
	}
	httputil.WriteJSONCtx(ctx, w, http.StatusOK, ListRunsResponse{Runs: summaries, Total: total})
}

// getOwnedRun fetches a run and enforces it belongs to kbID.
func (h *Handler) getOwnedRun(w http.ResponseWriter, r *http.Request, kbID uuid.UUID, runID uuid.UUID) *eval.Run {
	ctx := r.Context()
	run, err := h.store.Get(ctx, runID)
	if err != nil {
		if errors.Is(err, storepkg.ErrNotFound) {
			httputil.WriteErrorCtx(ctx, w, http.StatusNotFound, "run not found")
			return nil
		}
		httputil.WriteInternalErrorCtx(ctx, w, err)
		return nil
	}
	if run.KBID != kbID {
		httputil.WriteErrorCtx(ctx, w, http.StatusNotFound, "run not found")
		return nil
	}
	return run
}

// GetRunForKB → GET /api/kb/{id}/eval/runs/{runId}
func (h *Handler) GetRunForKB(w http.ResponseWriter, r *http.Request) {
	kbID, ok := pathKB(w, r)
	if !ok {
		return
	}
	runID, err := uuid.Parse(r.PathValue("runId"))
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid run id")
		return
	}
	run := h.getOwnedRun(w, r, kbID, runID)
	if run == nil {
		return
	}
	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, run)
}

// DeleteRunForKB → DELETE /api/kb/{id}/eval/runs/{runId}
func (h *Handler) DeleteRunForKB(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID, ok := pathKB(w, r)
	if !ok {
		return
	}
	runID, err := uuid.Parse(r.PathValue("runId"))
	if err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "invalid run id")
		return
	}
	if h.getOwnedRun(w, r, kbID, runID) == nil {
		return
	}
	_, running, err := h.store.Delete(ctx, runID)
	if err != nil {
		httputil.WriteInternalErrorCtx(ctx, w, err)
		return
	}
	if running {
		httputil.WriteErrorCtx(ctx, w, http.StatusConflict, "cannot delete a running run")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ExportRunForKB → GET /api/kb/{id}/eval/runs/{runId}/export?compare_with=
func (h *Handler) ExportRunForKB(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID, ok := pathKB(w, r)
	if !ok {
		return
	}
	runID, err := uuid.Parse(r.PathValue("runId"))
	if err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "invalid run id")
		return
	}
	a := h.getOwnedRun(w, r, kbID, runID)
	if a == nil {
		return
	}
	if cmpRaw := r.URL.Query().Get("compare_with"); cmpRaw != "" {
		cmpID, perr := uuid.Parse(cmpRaw)
		if perr != nil {
			httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "invalid compare_with id")
			return
		}
		b := h.getOwnedRun(w, r, kbID, cmpID) // cross-KB compare blocked here
		if b == nil {
			return
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		_, _ = io.WriteString(w, eval.ExportCompareMarkdown(*a, *b))
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	_, _ = io.WriteString(w, eval.ExportSingleRunMarkdown(*a))
}

// GenerateGoldenSetForKB → POST /api/kb/{id}/eval/golden-sets/generate
// Same as the admin GenerateGoldenSet but kb_id comes from the path, not the body.
func (h *Handler) GenerateGoldenSetForKB(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID, ok := pathKB(w, r)
	if !ok {
		return
	}
	var greq generateGoldenSetRequest
	if err := json.NewDecoder(r.Body).Decode(&greq); err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	greq.KBID = kbID.String() // force path KB; ignore any body kb_id
	if strings.TrimSpace(greq.Name) == "" {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "name is required")
		return
	}
	if greq.Lang != "de" && greq.Lang != "en" {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "lang must be de or en")
		return
	}
	const capN = 200
	clamp := func(n int) int {
		if n < 0 {
			return 0
		}
		if n > capN {
			return capN
		}
		return n
	}
	params := eval.GenJobParams{
		Lang: greq.Lang, Name: strings.TrimSpace(greq.Name), MaxFiles: greq.MaxFiles, Model: greq.Model,
		Counts: eval.GenCounts{
			Lookup: clamp(greq.Counts.Lookup), Complex: clamp(greq.Counts.Complex),
			Enumeration: clamp(greq.Counts.Enumeration), MultiHop: clamp(greq.Counts.MultiHop),
		},
	}
	if params.Counts.Lookup+params.Counts.Complex+params.Counts.Enumeration+params.Counts.MultiHop == 0 {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "at least one count must be > 0")
		return
	}
	var createdBy *uuid.UUID
	if user := auth.UserFromContext(ctx); user != nil {
		if id, perr := uuid.Parse(user.ID); perr == nil && id != uuid.Nil {
			createdBy = &id
		}
	}
	jobID, err := h.genJobStore.Create(ctx, kbID, params, createdBy)
	if err != nil {
		httputil.WriteInternalErrorCtx(ctx, w, err)
		return
	}
	if err := EnqueueGenerate(ctx, h.asynqClient, jobID); err != nil {
		_ = h.genJobStore.MarkFailed(ctx, jobID, "enqueue failed: "+err.Error())
		httputil.WriteInternalErrorCtx(ctx, w, err)
		return
	}
	httputil.WriteJSONCtx(ctx, w, http.StatusCreated, map[string]any{"job_id": jobID, "status": "queued"})
}

// ListGenJobsForKB → GET /api/kb/{id}/eval/golden-sets/jobs
// Filters the generation jobs to this KB. genJobStore.List returns recent jobs
// across KBs; filter by kb_id here.
//
// eval.GenJob has field KBID uuid.UUID (confirmed from internal/eval/genjob_store.go).
func (h *Handler) ListGenJobsForKB(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID, ok := pathKB(w, r)
	if !ok {
		return
	}
	list, err := h.genJobStore.List(ctx, 50)
	if err != nil {
		httputil.WriteInternalErrorCtx(ctx, w, err)
		return
	}
	filtered := make([]eval.GenJob, 0, len(list))
	for _, j := range list {
		if j.KBID == kbID {
			filtered = append(filtered, j)
		}
	}
	httputil.WriteJSONCtx(ctx, w, http.StatusOK, map[string]any{"jobs": filtered})
}
