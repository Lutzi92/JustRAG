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

// bindingSource lists the agents and teams attached to one KB, flagging the one
// that is the KB default (agent_kb_links.is_default / team_kb_links.is_default)
// and any whose agent/team is switched off.
//
// The disabled ones must be included, which is why this is NOT the chat
// picker's read: that one filters is_enabled (correctly — a disabled agent is
// not selectable in chat), and reusing it left a KB whose default points at a
// disabled agent projecting „keine Vorgabe" with no way to clear the row.
//
// It is stated in this package's OWN types on purpose. The single production
// implementation wraps internal/agentteams' Store.ListBindingCandidatesForKB,
// but that method returns agentteams' own DTOs, and naming a foreign struct in
// this interface would drag the import into a package that must stay a leaf
// (see the package doc in nodes.go). internal/app owns the small adapter, the
// same way it owns communitySink for internal/community.
type bindingSource interface {
	ListBindingOptions(ctx context.Context, kbID string) ([]BindingOption, error)
}

// Handler serves the read-only workflow projection.
type Handler struct {
	store    store
	global   globalReader
	bindings bindingSource
}

// NewHandler constructs the workflow Handler.
func NewHandler(s store, g globalReader, b bindingSource) *Handler {
	return &Handler{store: s, global: g, bindings: b}
}

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

	binding := h.resolveBinding(ctx, kbID)

	g, err := Project(ctx, overlay, h.global, lane, binding.binding())
	if err != nil {
		logctx.From(ctx).Error("pipeline.workflow.project", "error", err, "kb_id", kbID)
		httputil.WriteInternalErrorCtx(ctx, w, err)
		return
	}
	// Project fills Kind and Name from what it was handed; the id and the
	// attachable set are the handler's to add (see AgentBindingInfo).
	g.AgentBinding = binding

	if err := h.annotatePreset(ctx, g, overlay, overrides); err != nil {
		logctx.From(ctx).Error("pipeline.workflow.preset_base", "error", err, "kb_id", kbID)
		httputil.WriteInternalErrorCtx(ctx, w, err)
		return
	}

	httputil.WriteJSONCtx(ctx, w, http.StatusOK, g)
}

// resolveBinding reads the KB's default agent/team plus the attachable set.
//
// A failed read does NOT fail the request. The graph is ~30 nodes of resolved
// configuration and one of them is the binding; refusing to draw the whole
// pipeline because the agent listing is down would be a worse answer than
// drawing it with one node marked unreadable. The route is a KB admin's only
// view of what their KB actually does.
//
// What it must not do is degrade to the ZERO AgentBinding. That value means
// "nothing bound", which is a claim about the KB — and the wrong one for every
// KB that does have a default. BindingUnknown exists precisely so a failed read
// can say "I do not know" on the node (applyAgentBinding) instead of quietly
// asserting the more common case. Options stays empty, so Task 4's inspector
// has nothing to offer and cannot write a change from a state it cannot see.
func (h *Handler) resolveBinding(ctx context.Context, kbID string) AgentBindingInfo {
	opts, err := h.bindings.ListBindingOptions(ctx, kbID)
	if err != nil {
		logctx.From(ctx).Error("pipeline.workflow.bindings", "error", err, "kb_id", kbID)
		return AgentBindingInfo{Kind: BindingUnknown, Options: []BindingOption{}}
	}
	return bindingInfoFrom(opts)
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
// warn before it destroys anything.
//
// A client COULD compute the overwrite count itself: GET /api/kb/{id}/settings
// (same kbAdminChain) returns each key's raw `override` explicitly, and GET
// /api/workflow/presets returns each preset's full Bundle, which is everything
// Overwrites needs. Note this is NOT derivable from the workflow projection
// alone — ProjectedNode.Origins collapses a KB override that happens to equal
// the global value to "global", so the canvas by itself cannot tell a
// redundant override from an inherited one. The endpoint exists for three
// other reasons:
//
//   - It shares planApply with the POST, so the preview can never advertise an
//     apply the server would then reject: an apply that would conflict fails
//     the preview with the same 400 and the same message. A client-side count
//     would answer "3 werden überschrieben" and then eat a 400 on confirm.
//   - One definition of "overwritten", on the server, next to the apply that
//     acts on it. A reimplementation in the frontend would have to re-derive
//     the loose-boolean comparison and the NULL-row rule (see Overwrites), and
//     would drift from them silently.
//   - One round-trip, against two (settings + presets) plus a client-side
//     join, on a dialog that opens on a click.
//
// Why a separate GET rather than a dryRun flag on the POST: a GET cannot
// write. A dry-run flag is one forgotten field away from a real apply, and the
// failure mode is silent destruction of an admin's configuration.
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
