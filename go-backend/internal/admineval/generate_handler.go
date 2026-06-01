package admineval

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/justrag/go-backend/internal/auth"
	"github.com/justrag/go-backend/internal/eval"
	"github.com/justrag/go-backend/internal/httputil"
)

// ---------------------------------------------------------------------------
// Request shape for GenerateGoldenSet
// ---------------------------------------------------------------------------

type generateGoldenSetRequest struct {
	KBID     string `json:"kb_id"`
	Name     string `json:"name"`
	Lang     string `json:"lang"`
	MaxFiles int    `json:"max_files"`
	Model    string `json:"model"`
	Counts   struct {
		Lookup      int `json:"lookup"`
		Complex     int `json:"complex"`
		Enumeration int `json:"enumeration"`
		MultiHop    int `json:"multihop"`
	} `json:"counts"`
}

// ---------------------------------------------------------------------------
// GenerateGoldenSet — POST /api/admin/eval/golden-sets/generate
// ---------------------------------------------------------------------------

// GenerateGoldenSet validates the request, creates a generation job row, and
// enqueues the async corpus-generation task. Returns 201 with the job_id on
// success.
func (h *Handler) GenerateGoldenSet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req generateGoldenSetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if strings.TrimSpace(req.Name) == "" {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "name is required")
		return
	}
	if req.Lang != "de" && req.Lang != "en" {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "lang must be de or en")
		return
	}

	kbID, err := uuid.Parse(req.KBID)
	if err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "invalid kb_id")
		return
	}
	if _, found, err := h.kbStore.GetKBInfo(ctx, kbID); err != nil {
		httputil.WriteInternalErrorCtx(ctx, w, err)
		return
	} else if !found {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "kb not found")
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
		Lang:     req.Lang,
		Name:     strings.TrimSpace(req.Name),
		MaxFiles: req.MaxFiles,
		Model:    req.Model,
		Counts: eval.GenCounts{
			Lookup:      clamp(req.Counts.Lookup),
			Complex:     clamp(req.Counts.Complex),
			Enumeration: clamp(req.Counts.Enumeration),
			MultiHop:    clamp(req.Counts.MultiHop),
		},
	}
	if params.Counts.Lookup+params.Counts.Complex+params.Counts.Enumeration+params.Counts.MultiHop == 0 {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "at least one count must be > 0")
		return
	}

	var createdBy *uuid.UUID
	if user := auth.UserFromContext(ctx); user != nil {
		if id, parseErr := uuid.Parse(user.ID); parseErr == nil && id != uuid.Nil {
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

// ---------------------------------------------------------------------------
// ListGenJobs — GET /api/admin/eval/golden-sets/jobs
// ---------------------------------------------------------------------------

// ListGenJobs returns the most recent 50 generation jobs for polling.
func (h *Handler) ListGenJobs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	list, err := h.genJobStore.List(ctx, 50)
	if err != nil {
		httputil.WriteInternalErrorCtx(ctx, w, err)
		return
	}
	httputil.WriteJSONCtx(ctx, w, http.StatusOK, map[string]any{"jobs": list})
}

// ---------------------------------------------------------------------------
// GetGoldenSet — GET /api/admin/eval/golden-sets/{id}
// ---------------------------------------------------------------------------

// GetGoldenSet returns the full golden set record including its content,
// suitable for view or download.
func (h *Handler) GetGoldenSet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "invalid id")
		return
	}
	gs, err := h.goldenSetStore.Get(ctx, id)
	if err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusNotFound, "golden set not found")
		return
	}
	httputil.WriteJSONCtx(ctx, w, http.StatusOK, gs)
}
