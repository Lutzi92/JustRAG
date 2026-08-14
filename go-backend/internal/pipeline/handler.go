package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/justrag/go-backend/internal/httputil"
	"github.com/justrag/go-backend/internal/logctx"
	"github.com/justrag/go-backend/internal/siteconfig"
)

// store loads and writes a KB's per-KB site_config overrides. Satisfied by
// *kbconfig.Store (same surface internal/kbconfig uses).
//
// It deliberately omits DeleteKey. Applying a preset must be able to SET the
// bundle's keys and nothing else — never to clear one — and the cheapest way
// to guarantee that is to give this package no way to express a deletion.
type store interface {
	ListKBOverrides(ctx context.Context, kbID string) (map[string]*string, error)
	// UpsertBatch writes every pair in one statement (see
	// kbconfig.Store.UpsertBatch: a single INSERT … ON CONFLICT DO UPDATE).
	// That single statement is what makes an apply all-or-nothing — Postgres
	// wraps it in its own transaction, so a 22-key apply cannot half-land.
	UpsertBatch(ctx context.Context, kbID string, kv map[string]*string) error
}

// globalReader fetches global site_config values, in batch and per-key.
// Satisfied by *chat.PGStore. The per-key method is required so h.global can
// also serve as siteconfig.NewKBOverlay's base reader below — BatchReader
// alone (just GetSiteConfigValues) does not satisfy that interface.
type globalReader interface {
	siteconfig.BatchReader
	GetSiteConfigValue(ctx context.Context, key string) (*string, error)
}

// Handler serves the read-only workflow projection.
type Handler struct {
	store  store
	global globalReader
}

// NewHandler constructs the workflow Handler.
func NewHandler(s store, g globalReader) *Handler { return &Handler{store: s, global: g} }

// GetWorkflow handles GET /api/kb/{id}/workflow?lane=…
//
// Mounted on kbAdminChain (KB role admin). Read-only: node edits go through the
// existing PUT /api/kb/{id}/settings so validation stays in one place.
func (h *Handler) GetWorkflow(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID := r.PathValue("id")

	lane := LaneComplex
	if raw := r.URL.Query().Get("lane"); raw != "" {
		lane = Lane(raw)
		if _, ok := lane.queryType(); !ok {
			httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "unbekannter lane-Parameter")
			return
		}
	}

	overrides, err := h.store.ListKBOverrides(ctx, kbID)
	if err != nil {
		logctx.From(ctx).Error("pipeline.workflow.list_overrides", "error", err, "kb_id", kbID)
		httputil.WriteInternalErrorCtx(ctx, w, err)
		return
	}

	overlay := siteconfig.NewKBOverlay(h.global, overrides)

	g, err := Project(ctx, overlay, h.global, lane)
	if err != nil {
		logctx.From(ctx).Error("pipeline.workflow.project", "error", err, "kb_id", kbID)
		httputil.WriteInternalErrorCtx(ctx, w, err)
		return
	}

	if err := h.annotatePreset(ctx, g, overlay, overrides); err != nil {
		logctx.From(ctx).Error("pipeline.workflow.preset_base", "error", err, "kb_id", kbID)
		httputil.WriteInternalErrorCtx(ctx, w, err)
		return
	}

	httputil.WriteJSONCtx(ctx, w, http.StatusOK, g)
}

// annotatePreset fills PresetBase / PresetBaseKnown / Deviations.
//
// overlay (not the bare global reader) resolves workflow_preset so a KB's own
// marker wins; overrides is the KB's RAW override map, and that difference is
// the whole design decision behind Deviations:
//
// The canvas renders „Basis: Hohe Präzision · 3 Abweichungen" — "you started
// here and changed three things". That is a statement about what THIS KB sets,
// so it is computed against the KB's own overrides, not against the effective
// values the overlay resolves. Two consequences, both intended:
//
//   - Resetting a bundle key back to inherit counts as a deviation. It should:
//     the preset pinned that key on this KB and this KB no longer pins it, so
//     a later change to the deployment default would move the KB off the
//     preset silently. Comparing effective values would hide that whenever the
//     global happens to agree with the bundle today.
//   - Conversely a key the KB never pinned is measured against the code
//     default (Deviations' documented fallback), not the deployment global, so
//     a deployment whose global differs from the code default can show a
//     deviation for a key nobody touched. That over-reports rather than
//     under-reports, and "this KB does not pin what the preset pinned" is
//     still true in that case — the safe direction for a badge whose job is to
//     tell an admin their configuration has drifted.
func (h *Handler) annotatePreset(ctx context.Context, g *ProjectedGraph, overlay globalReader, overrides map[string]*string) error {
	base, known, err := PresetBaseFor(ctx, overlay)
	if err != nil {
		return err
	}
	g.PresetBase = base
	g.PresetBaseKnown = known
	if !known || base == "" {
		return nil // nothing to compare against; Deviations stays empty
	}
	p, ok := PresetByID(base)
	if !ok {
		// Unreachable: found==true means PresetByID resolved it. Kept as a
		// guard rather than an assumption, so a future change to
		// PresetBaseFor's contract degrades to "no deviations" instead of
		// panicking on a zero-value bundle.
		g.PresetBaseKnown = false
		return nil
	}
	g.Deviations = Deviations(p.Bundle, overrides)
	return nil
}

type applyPresetRequest struct {
	Preset string `json:"preset"`
}

// ApplyPreset handles POST /api/kb/{id}/workflow/preset.
//
// Mounted on kbAdminChain, like GET /api/kb/{id}/workflow: it rewrites the
// KB's answering pipeline, which the four-role model places squarely on admin
// ("how the KB is processed and answered"), not edit.
//
// The write is all-or-nothing in both directions: every check runs before
// anything is written (planApply), and the write itself is one UpsertBatch —
// a single INSERT … ON CONFLICT statement, hence one implicit transaction.
// There is no intermediate state in which some of the bundle has landed.
func (h *Handler) ApplyPreset(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID := r.PathValue("id")

	var req applyPresetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, "invalid JSON: "+httputil.SanitizeError(err))
		return
	}

	plan, err := h.planApply(ctx, kbID, strings.TrimSpace(req.Preset))
	if err != nil {
		if writeBadRequest(ctx, w, err) {
			return
		}
		logctx.From(ctx).Error("pipeline.preset.plan", "error", err, "kb_id", kbID)
		httputil.WriteInternalErrorCtx(ctx, w, err)
		return
	}

	if err := h.store.UpsertBatch(ctx, kbID, plan.kv); err != nil {
		logctx.From(ctx).Error("pipeline.preset.upsert", "error", err, "kb_id", kbID, "preset", plan.result.Preset)
		httputil.WriteInternalErrorCtx(ctx, w, err)
		return
	}

	logctx.From(ctx).Info("pipeline.preset.applied",
		"kb_id", kbID, "preset", plan.result.Preset,
		"keys", len(plan.kv), "overwrites", len(plan.result.Overwrites))

	httputil.WriteJSONCtx(ctx, w, http.StatusOK, plan.result)
}

// PreviewPreset handles GET /api/kb/{id}/workflow/preset?preset=<id>.
//
// It answers "what does applying this cost me?" without writing, so the UI can
// warn before it destroys anything. Three reasons this is a separate GET
// rather than a dryRun flag on the POST above:
//
//   - A GET cannot write. A dry-run flag is one forgotten field away from a
//     real apply, and the failure mode is silent data loss on someone's
//     configuration.
//   - The client cannot compute the count itself from data it already holds:
//     ProjectedNode.Origins documents that a KB override which happens to
//     equal the global value is reported as "global", so the canvas cannot
//     tell a redundant override from an inherited value — exactly the
//     distinction "how many of MY settings are overwritten" turns on.
//   - It shares planApply with the POST, so a preview can never advertise an
//     apply the server would reject: an apply that would conflict fails the
//     preview with the same 400 and the same message.
func (h *Handler) PreviewPreset(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	kbID := r.PathValue("id")

	plan, err := h.planApply(ctx, kbID, strings.TrimSpace(r.URL.Query().Get("preset")))
	if err != nil {
		if writeBadRequest(ctx, w, err) {
			return
		}
		logctx.From(ctx).Error("pipeline.preset.preview", "error", err, "kb_id", kbID)
		httputil.WriteInternalErrorCtx(ctx, w, err)
		return
	}

	httputil.WriteJSONCtx(ctx, w, http.StatusOK, plan.result)
}

// writeBadRequest answers 400 with err's own message when err is the caller's
// fault, and reports whether it did. A conflict must reach the admin verbatim
// — "do not force it through" is only useful if they are told what blocked it.
func writeBadRequest(ctx context.Context, w http.ResponseWriter, err error) bool {
	var br badRequest
	if !errors.As(err, &br) {
		return false
	}
	httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, httputil.SanitizeError(br.error))
	return true
}

// ListPresets handles GET /api/workflow/presets.
//
// Mounted authenticated-only, WITHOUT a KB in the path: presets are curated,
// deployment-wide starting points (internal/pipeline/presets.go), not a
// per-KB resource, so kbAdminChain's RequireKBRole gate does not apply here —
// there is no {id} to resolve a role against. Follows the same
// "authenticated, no KB" pattern as GET /api/kb/catalog and GET
// /api/kb-categories in routes.go (rc.authMw.Authenticate directly, no
// kbaccess middleware).
func (h *Handler) ListPresets(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	priced, err := PricePresets(ctx, h.global)
	if err != nil {
		logctx.From(ctx).Error("pipeline.workflow.list_presets", "error", err)
		httputil.WriteInternalErrorCtx(ctx, w, err)
		return
	}

	httputil.WriteJSONCtx(ctx, w, http.StatusOK, priced)
}
