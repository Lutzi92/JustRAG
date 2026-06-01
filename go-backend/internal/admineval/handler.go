package admineval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/eval"
	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/logctx"
	storepkg "github.com/justrag/go-backend/internal/store"
)

// ---------------------------------------------------------------------------
// Dependency interfaces
// ---------------------------------------------------------------------------

// runStore is the persistence interface required by the eval handler.
// It is satisfied by *eval.Store; the interface allows handler tests to mock
// without a real database.
type runStore interface {
	Insert(ctx context.Context, r eval.Run) (uuid.UUID, error)
	Get(ctx context.Context, id uuid.UUID) (*eval.Run, error)
	List(ctx context.Context, opts eval.ListOpts) ([]eval.Run, int, error)
	Delete(ctx context.Context, id uuid.UUID) (deleted bool, running bool, err error)
	MarkRunning(ctx context.Context, id uuid.UUID) error
	MarkCompleted(ctx context.Context, id uuid.UUID, report json.RawMessage) error
	MarkFailed(ctx context.Context, id uuid.UUID, errMsg string) error
}

// kbReader is the minimal knowledge-base interface the handler needs.
// GetKBInfo returns the KB name and whether the KB exists.
// A non-nil error signals a DB/IO failure distinct from "not found".
type kbReader interface {
	GetKBInfo(ctx context.Context, id uuid.UUID) (name string, found bool, err error)
}

// siteConfigReader is the minimal site-config interface the handler needs.
// It is satisfied by *chat.PGStore and the eval.SnapshotConfigReader.
type siteConfigReader interface {
	GetSiteConfigValue(ctx context.Context, key string) (*string, error)
}

// enqueuer is an abstraction over *asynq.Client so handler tests can mock
// task enqueueing without a real Redis connection.
type enqueuer interface {
	EnqueueContext(ctx context.Context, task *asynq.Task, opts ...asynq.Option) (*asynq.TaskInfo, error)
}

// goldenSetStore is the persistence interface for eval_golden_sets.
// It is satisfied by *eval.GoldenSetStore; the interface allows handler tests
// to mock without a real database.
type goldenSetStore interface {
	Create(ctx context.Context, g eval.GoldenSet) (uuid.UUID, time.Time, error)
	Get(ctx context.Context, id uuid.UUID) (*eval.GoldenSet, error)
	List(ctx context.Context) ([]eval.GoldenSet, error)
	Delete(ctx context.Context, id uuid.UUID) (bool, error)
}

// ---------------------------------------------------------------------------
// Config snapshot key list (spec §5)
// ---------------------------------------------------------------------------

var snapshotConfigKeys = []string{
	"mmr_lambda", "rerank_blend_alpha",
	"crag_enabled", "crag_grader_model", "crag_min_relevant_chunks",
	"contextual_enrichment", "contextual_enrichment_model",
	"auto_spell_correct",
	"citation_validation_enabled",
	"factcheck_in_chat",
	"query_instruction",
	"rerank_use_chat_template", "rerank_instruction",
	"hnsw_ef_search", "mrl_two_pass_enabled",
	// P7 fast-tier default — when set, every fast-tier task that
	// doesn't have a per-task override (CRAG grader, KG extractor,
	// contextual enricher, factuality / Self-RAG verifier, DAG
	// critic, longmem extractor, KB router) inherits this model.
	"model_tier_fast",
	// Prototypes for dual-arm BM25 and entity-asking alpha override.
	// Both keys are read by the vector search layer; without them in
	// the snapshot, the SnapshotConfigReader returns nil and the
	// search code falls back to defaults (arm disabled, entity alpha
	// inherits). A live eval would silently run with the prototype off.
	"bm25_simple_arm_enabled",
	"bm25_tiered_boost_enabled",
	"rerank_blend_alpha_entity",
	// T1-1 sub-question decomposition. Without these in the snapshot,
	// re-runs of an eval after flipping the flag silently lose the
	// signal — same reasoning as the bm25 keys above.
	"query_decompose_enabled",
	"query_decompose_model",
	// T1-2 ANN-based long-term memory recall. Flips both insert
	// (embed-on-insert) and recall (RecallSemantic) paths.
	"chat_longmem_recall_semantic",
	// T1-3 Mem0-style conflict resolution. Depends on
	// chat_longmem_recall_semantic for the embedding-based
	// candidate lookup.
	"chat_longmem_conflict_resolution",
	"chat_longmem_conflict_model",
	"chat_longmem_conflict_candidates",
	// T1-4 / T1-5 graph-traversal mode. path_mode is the
	// trichotomy (neighbors|ppr|paths); the per-mode tuning keys
	// only matter for the selected branch but we snapshot them all
	// so an eval re-run after flipping the mode picks up the
	// right configuration without re-snapshot churn.
	"chat_graph_routing_path_mode",
	"chat_graph_routing_ppr_damping",
	"chat_graph_routing_ppr_max_iter",
	"chat_graph_routing_ppr_top_entities",
	"chat_graph_routing_paths_max_len",
	"chat_graph_routing_paths_max_paths",
	// T2-4 dynamic alpha hybrid search.
	"hybrid_dynamic_alpha_enabled",
	"hybrid_dynamic_alpha_sensitivity",
	// T2-2 RAPTOR clustering algorithm switch.
	"raptor_clustering_algorithm",
	"raptor_leiden_resolution",
	// T2-3 ECoRAG evidentiality compression.
	"chat_context_compression_enabled",
	"chat_context_compression_min_chunks",
	"chat_context_compression_threshold",
	"chat_context_compression_model",
	// T2-1 long-context (System 2) routing.
	"chat_longcontext_enabled",
	"chat_longcontext_max_tokens",
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

// Handler holds the dependencies for the eval run HTTP endpoints.
type Handler struct {
	store          runStore
	kbStore        kbReader
	cfgReader      siteConfigReader
	asynqClient    enqueuer
	goldenSetStore goldenSetStore
	genJobStore    *eval.GenJobStore
}

// NewHandler creates an eval Handler.
func NewHandler(
	store runStore,
	kbStore kbReader,
	cfgReader siteConfigReader,
	asynqClient enqueuer,
	goldenSetStore goldenSetStore,
	genJobStore *eval.GenJobStore,
) *Handler {
	return &Handler{
		store:          store,
		kbStore:        kbStore,
		cfgReader:      cfgReader,
		asynqClient:    asynqClient,
		goldenSetStore: goldenSetStore,
		genJobStore:    genJobStore,
	}
}

// ---------------------------------------------------------------------------
// resolvedRun holds the fully-resolved parameters after overlaying request
// fields on top of site_config defaults.
// ---------------------------------------------------------------------------

type resolvedRun struct {
	kbID         uuid.UUID
	kbName       string
	goldenSetID  uuid.UUID
	judgeEnabled bool
	topK         int
	label        string
}

// resolveDefaults reads eval defaults from site_configs, overlays explicit
// request fields, and errors if required fields are still missing.
func (h *Handler) resolveDefaults(ctx context.Context, req CreateRunRequest) (resolvedRun, error) {
	var r resolvedRun

	// --- KB ID ---
	if req.KBID != nil {
		r.kbID = *req.KBID
	} else {
		v, err := h.cfgReader.GetSiteConfigValue(ctx, "eval_default_kb_id")
		if err != nil {
			return r, fmt.Errorf("read eval_default_kb_id: %w", err)
		}
		if v == nil || *v == "" {
			return r, fmt.Errorf("kb_id is required (no eval_default_kb_id configured)")
		}
		id, err := uuid.Parse(*v)
		if err != nil {
			return r, fmt.Errorf("eval_default_kb_id is not a valid UUID: %w", err)
		}
		r.kbID = id
	}

	// --- Golden set ID ---
	if req.GoldenSetID != nil {
		r.goldenSetID = *req.GoldenSetID
	} else {
		v, err := h.cfgReader.GetSiteConfigValue(ctx, "eval_default_golden_set_id")
		if err != nil {
			return r, fmt.Errorf("read eval_default_golden_set_id: %w", err)
		}
		if v != nil && *v != "" {
			id, err := uuid.Parse(*v)
			if err != nil {
				return r, fmt.Errorf("eval_default_golden_set_id is not a valid UUID: %w", err)
			}
			r.goldenSetID = id
		}
		// If still nil, the caller will validate below.
	}

	// --- Judge ---
	if req.JudgeEnabled != nil {
		r.judgeEnabled = *req.JudgeEnabled
	} else {
		v, err := h.cfgReader.GetSiteConfigValue(ctx, "eval_default_judge")
		if err != nil {
			return r, fmt.Errorf("read eval_default_judge: %w", err)
		}
		if v != nil {
			r.judgeEnabled = *v == "true" || *v == "1"
		}
		// default false when unset
	}

	// --- Top-k ---
	const defaultTopK = 10
	if req.TopK != nil {
		r.topK = *req.TopK
	} else {
		v, err := h.cfgReader.GetSiteConfigValue(ctx, "eval_default_top_k")
		if err != nil {
			return r, fmt.Errorf("read eval_default_top_k: %w", err)
		}
		if v != nil && *v != "" {
			n, err := strconv.Atoi(*v)
			if err != nil {
				return r, fmt.Errorf("eval_default_top_k is not a valid integer: %w", err)
			}
			r.topK = n
		} else {
			r.topK = defaultTopK
		}
	}

	// --- Label (optional) ---
	r.label = req.Label

	return r, nil
}

// captureConfigSnapshot reads the fixed key list from site_configs and
// returns them as map[string]*string. Absent keys are stored as nil.
func captureConfigSnapshot(ctx context.Context, cfg siteConfigReader) (map[string]*string, error) {
	snap := make(map[string]*string, len(snapshotConfigKeys))
	for _, k := range snapshotConfigKeys {
		v, err := cfg.GetSiteConfigValue(ctx, k)
		if err != nil {
			return nil, fmt.Errorf("capture config snapshot key=%s: %w", k, err)
		}
		snap[k] = v
	}
	return snap, nil
}

// ---------------------------------------------------------------------------
// CreateRun — POST /api/admin/eval/runs
// ---------------------------------------------------------------------------

// CreateRun validates the request, inserts an eval_runs row, and enqueues the
// asynq task. Returns 201 with CreateRunResponse on success.
func (h *Handler) CreateRun(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	log := logctx.From(ctx)

	// 1. Auth: extract triggeredBy from JWT claims.
	user := auth.UserFromContext(ctx)
	if user == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusUnauthorized, "authentication required")
		return
	}

	// 2. Decode JSON body (empty body is allowed — all fields optional at this stage).
	var req CreateRunRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
	}

	// 3. Resolve defaults.
	resolved, err := h.resolveDefaults(ctx, req)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, err.Error())
		return
	}

	// 4. Validate KB exists and fetch its name.
	name, found, err := h.kbStore.GetKBInfo(ctx, resolved.kbID)
	if err != nil {
		log.Error("eval.create_run.kb_lookup", "error", err)
		httputil.WriteInternalErrorCtx(r.Context(), w, err)
		return
	}
	if !found {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, fmt.Sprintf("kb_id %s not found", resolved.kbID))
		return
	}
	resolved.kbName = name

	// 5. Validate golden set exists and fetch its metadata.
	if resolved.goldenSetID == uuid.Nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "golden_set_id is required (provide in request or set eval_default_golden_set_id)")
		return
	}
	gs, err := h.goldenSetStore.Get(ctx, resolved.goldenSetID)
	if err != nil {
		if errors.Is(err, storepkg.ErrNotFound) {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, fmt.Sprintf("golden_set_id %s not found", resolved.goldenSetID))
			return
		}
		log.Error("eval.create_run.golden_set_lookup", "error", err, "golden_set_id", resolved.goldenSetID)
		httputil.WriteInternalErrorCtx(r.Context(), w, err)
		return
	}

	// 6. Capture site-config snapshot.
	snap, err := captureConfigSnapshot(ctx, h.cfgReader)
	if err != nil {
		log.Error("eval.create_run.snapshot", "error", err)
		httputil.WriteInternalErrorCtx(r.Context(), w, err)
		return
	}
	snapJSON, err := json.Marshal(snap)
	if err != nil {
		log.Error("eval.create_run.snapshot_marshal", "error", err)
		httputil.WriteInternalErrorCtx(r.Context(), w, err)
		return
	}

	// 7. Parse triggeredBy UUID (Claims.ID is a string).
	triggeredBy, err := uuid.Parse(user.ID)
	if err != nil {
		// Tolerate non-UUID user IDs by leaving triggeredBy nil.
		log.Warn("eval.create_run.triggered_by_parse", "user_id", user.ID, "error", err)
		triggeredBy = uuid.Nil
	}
	var triggeredByPtr *uuid.UUID
	if triggeredBy != uuid.Nil {
		triggeredByPtr = &triggeredBy
	}

	// 8. Insert eval_runs row.
	run := eval.Run{
		KBID:           resolved.kbID,
		GoldenSetID:    &gs.ID,
		FixtureHash:    gs.ContentHash,
		ConfigSnapshot: json.RawMessage(snapJSON),
		JudgeEnabled:   resolved.judgeEnabled,
		TopK:           resolved.topK,
		Label:          resolved.label,
		TriggeredBy:    triggeredByPtr,
	}
	runID, err := h.store.Insert(ctx, run)
	if err != nil {
		log.Error("eval.create_run.insert", "error", err)
		httputil.WriteInternalErrorCtx(r.Context(), w, err)
		return
	}

	// 9. Enqueue the asynq task. On failure, mark the row failed and return 500.
	if err := EnqueueRun(ctx, h.asynqClient, runID); err != nil {
		log.Error("eval.create_run.enqueue", "run_id", runID, "error", err)
		if mfErr := h.store.MarkFailed(ctx, runID, "enqueue failed: "+err.Error()); mfErr != nil {
			log.Error("eval.create_run.mark_failed", "run_id", runID, "error", mfErr)
		}
		httputil.WriteErrorCtx(r.Context(), w, http.StatusInternalServerError, "failed to enqueue eval run")
		return
	}

	log.Info("eval.run.enqueued",
		"run_id", runID,
		"kb_id", resolved.kbID,
		"judge", resolved.judgeEnabled,
	)

	// 10. Respond 201.
	httputil.WriteJSONCtx(r.Context(), w, http.StatusCreated, CreateRunResponse{
		ID:     runID,
		Status: "queued",
		// CreatedAt is populated by the DB INSERT RETURNING; we don't have it
		// here without a Get round-trip. Use zero — callers use the ID to poll.
	})
}

// ---------------------------------------------------------------------------
// intParam parses a query parameter as an integer with a default and a max cap.
// ---------------------------------------------------------------------------

func intParam(q url.Values, name string, def, max int) int {
	s := q.Get(name)
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

// ---------------------------------------------------------------------------
// ListRuns — GET /api/admin/eval/runs
// ---------------------------------------------------------------------------

// ListRuns returns a paginated list of eval runs with optional status/KB filters.
func (h *Handler) ListRuns(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	q := r.URL.Query()
	limit := intParam(q, "limit", 50, 200)
	offset := intParam(q, "offset", 0, 1_000_000)

	status := q.Get("status")
	var kbIDPtr *uuid.UUID
	if raw := q.Get("kb_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid kb_id: "+err.Error())
			return
		}
		kbIDPtr = &id
	}

	opts := eval.ListOpts{
		Limit:  limit,
		Offset: offset,
		Status: status,
		KBID:   kbIDPtr,
	}

	runs, total, err := h.store.List(ctx, opts)
	if err != nil {
		logctx.From(ctx).Error("eval.list_runs", "error", err)
		httputil.WriteInternalErrorCtx(r.Context(), w, err)
		return
	}

	summaries := make([]RunSummary, 0, len(runs))
	for _, run := range runs {
		s := RunSummary{
			ID:           run.ID,
			Label:        run.Label,
			Status:       run.Status,
			CreatedAt:    run.CreatedAt,
			StartedAt:    run.StartedAt,
			FinishedAt:   run.FinishedAt,
			KBID:         run.KBID,
			JudgeEnabled: run.JudgeEnabled,
			ErrorMessage: run.ErrorMessage,
		}

		// Populate KB name — errors are swallowed; empty name is acceptable.
		if name, _, kbErr := h.kbStore.GetKBInfo(ctx, run.KBID); kbErr == nil {
			s.KBName = name
		}

		// Populate aggregate metrics if report is present.
		if len(run.Report) > 0 {
			s.Aggregate, s.RouteMeanRecall = summarize(run.Report)
		}

		summaries = append(summaries, s)
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, ListRunsResponse{
		Runs:  summaries,
		Total: total,
	})
}

// ---------------------------------------------------------------------------
// GetRun — GET /api/admin/eval/runs/{id}
// ---------------------------------------------------------------------------

// GetRun returns the full run record including the report.
func (h *Handler) GetRun(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	rawID := r.PathValue("id")
	id, err := uuid.Parse(rawID)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid run id: "+err.Error())
		return
	}

	run, err := h.store.Get(ctx, id)
	if err != nil {
		if errors.Is(err, storepkg.ErrNotFound) {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "run not found")
			return
		}
		logctx.From(ctx).Error("eval.get_run", "error", err, "run_id", id)
		httputil.WriteInternalErrorCtx(r.Context(), w, err)
		return
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, run)
}

// ---------------------------------------------------------------------------
// ExportRun — GET /api/admin/eval/runs/{id}/export?compare_with={other_id}
// ---------------------------------------------------------------------------

// ExportRun renders a run (or a comparison of two runs) as a markdown document.
//
// If the compare_with query parameter is present, a two-run comparison is
// emitted via eval.ExportCompareMarkdown. Otherwise, a single-run snapshot is
// emitted via eval.ExportSingleRunMarkdown.
//
// Returns 400 on invalid UUIDs, 404 when a run is not found, 500 on store
// errors. On success: 200 with Content-Type text/markdown; charset=utf-8.
func (h *Handler) ExportRun(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// 1. Parse primary run ID.
	rawID := r.PathValue("id")
	id, err := uuid.Parse(rawID)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid run id: "+err.Error())
		return
	}

	// 2. Fetch primary run.
	a, err := h.store.Get(ctx, id)
	if err != nil {
		if errors.Is(err, storepkg.ErrNotFound) {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "run not found")
			return
		}
		logctx.From(ctx).Error("eval.export_run.get_primary", "error", err, "run_id", id)
		httputil.WriteInternalErrorCtx(r.Context(), w, err)
		return
	}

	// 3. If compare_with is set, fetch the second run and emit a comparison.
	if cmpRaw := r.URL.Query().Get("compare_with"); cmpRaw != "" {
		cmpID, err := uuid.Parse(cmpRaw)
		if err != nil {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid compare_with id: "+err.Error())
			return
		}

		b, err := h.store.Get(ctx, cmpID)
		if err != nil {
			if errors.Is(err, storepkg.ErrNotFound) {
				httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "compare_with run not found")
				return
			}
			logctx.From(ctx).Error("eval.export_run.get_compare", "error", err, "run_id", cmpID)
			httputil.WriteInternalErrorCtx(r.Context(), w, err)
			return
		}

		md := eval.ExportCompareMarkdown(*a, *b)
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, md)
		return
	}

	// 4. Single-run export.
	md := eval.ExportSingleRunMarkdown(*a)
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, md)
}

// ---------------------------------------------------------------------------
// DeleteRun — DELETE /api/admin/eval/runs/{id}
// ---------------------------------------------------------------------------

// DeleteRun removes an eval run. Returns 409 if the run is currently running.
func (h *Handler) DeleteRun(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	rawID := r.PathValue("id")
	id, err := uuid.Parse(rawID)
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid run id: "+err.Error())
		return
	}

	deleted, running, err := h.store.Delete(ctx, id)
	if err != nil {
		logctx.From(ctx).Error("eval.delete_run", "error", err, "run_id", id)
		httputil.WriteInternalErrorCtx(r.Context(), w, err)
		return
	}
	if running {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusConflict, "cannot delete a running run; wait for it to finish or fail")
		return
	}
	if !deleted {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "run not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// CreateGoldenSet — POST /api/admin/eval/golden-sets
// ---------------------------------------------------------------------------

// CreateGoldenSet accepts a multipart/form-data upload of a JSONL fixture file,
// validates and parses it, and inserts a new eval_golden_sets row. Returns 201
// on success, 400 on parse/validation errors, 409 on duplicate name.
func (h *Handler) CreateGoldenSet(w http.ResponseWriter, r *http.Request) {
	// 1. Parse multipart form (max 5 MB).
	if err := r.ParseMultipartForm(5 << 20); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid multipart body: "+err.Error())
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "name is required")
		return
	}
	description := strings.TrimSpace(r.FormValue("description"))

	// 2. Read the uploaded file.
	file, header, err := r.FormFile("file")
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "file field is required")
		return
	}
	defer file.Close()
	if header.Size > (5 << 20) {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "file too large (max 5 MB)")
		return
	}
	raw, err := io.ReadAll(file)
	if err != nil {
		logctx.From(r.Context()).Error("eval.create_golden_set.read_upload", "error", err, "filename", header.Filename)
		httputil.WriteInternalErrorCtx(r.Context(), w, err)
		return
	}

	// 3. Parse and validate JSONL content.
	questions, err := eval.ParseGoldenSetJSONL(bytes.NewReader(raw))
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "jsonl parse error: "+err.Error())
		return
	}
	if len(questions) == 0 {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "fixture is empty")
		return
	}

	// 4. Serialize as a JSON array for the content column.
	contentJSON, err := json.Marshal(questions)
	if err != nil {
		logctx.From(r.Context()).Error("eval.create_golden_set.marshal", "error", err, "question_count", len(questions))
		httputil.WriteInternalErrorCtx(r.Context(), w, err)
		return
	}
	contentHash := eval.HashFixtureContent(raw)

	// 5. Resolve triggeredBy from JWT claims.
	var createdBy *uuid.UUID
	if user := auth.UserFromContext(r.Context()); user != nil {
		if id, parseErr := uuid.Parse(user.ID); parseErr == nil && id != uuid.Nil {
			createdBy = &id
		}
	}

	// 6. Insert.
	id, createdAt, err := h.goldenSetStore.Create(r.Context(), eval.GoldenSet{
		Name:          name,
		Description:   description,
		Content:       contentJSON,
		ContentHash:   contentHash,
		QuestionCount: len(questions),
		CreatedBy:     createdBy,
	})
	if errors.Is(err, eval.ErrGoldenSetNameTaken) {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusConflict, fmt.Sprintf("name %q is already taken", name))
		return
	}
	if err != nil {
		logctx.From(r.Context()).Error("eval.create_golden_set.insert", "error", err, "name", name)
		httputil.WriteInternalErrorCtx(r.Context(), w, err)
		return
	}

	httputil.WriteJSONCtx(r.Context(), w, http.StatusCreated, CreateGoldenSetResponse{
		ID:            id,
		Name:          name,
		ContentHash:   contentHash,
		QuestionCount: len(questions),
		CreatedAt:     createdAt,
	})
}

// ---------------------------------------------------------------------------
// ListGoldenSets — GET /api/admin/eval/golden-sets
// ---------------------------------------------------------------------------

// ListGoldenSets returns all golden sets ordered by created_at DESC. The content
// column is omitted — use the golden set ID to look up full content if needed.
func (h *Handler) ListGoldenSets(w http.ResponseWriter, r *http.Request) {
	sets, err := h.goldenSetStore.List(r.Context())
	if err != nil {
		logctx.From(r.Context()).Error("eval.list_golden_sets", "error", err)
		httputil.WriteInternalErrorCtx(r.Context(), w, err)
		return
	}
	out := make([]GoldenSetSummary, 0, len(sets))
	for _, g := range sets {
		out = append(out, GoldenSetSummary{
			ID:            g.ID,
			Name:          g.Name,
			Description:   g.Description,
			ContentHash:   g.ContentHash,
			QuestionCount: g.QuestionCount,
			CreatedAt:     g.CreatedAt,
		})
	}
	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, ListGoldenSetsResponse{GoldenSets: out})
}

// ---------------------------------------------------------------------------
// DeleteGoldenSet — DELETE /api/admin/eval/golden-sets/{id}
// ---------------------------------------------------------------------------

// DeleteGoldenSet removes a golden set by id. Returns 204 on success, 404 when
// the id is not found, 400 on an invalid UUID.
func (h *Handler) DeleteGoldenSet(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid id")
		return
	}
	deleted, err := h.goldenSetStore.Delete(r.Context(), id)
	if err != nil {
		logctx.From(r.Context()).Error("eval.delete_golden_set", "error", err, "golden_set_id", id)
		httputil.WriteInternalErrorCtx(r.Context(), w, err)
		return
	}
	if !deleted {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "golden set not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
