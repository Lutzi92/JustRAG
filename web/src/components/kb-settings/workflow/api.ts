import type {
  BindingTarget,
  WorkflowConfigField, WorkflowGraph, WorkflowLane, WorkflowPreset, WorkflowPresetApplyResult,
} from '../../../types';
import { API_BASE_URL, authFetch } from '../../../api';
import { attachAgentToKb, attachTeamToKb } from '../../agents/api';

// authFetch injects the Bearer token from localStorage and fires the logout
// event on 401 — a raw fetch() would send no Authorization header and 401.
// API_BASE_URL is required because there is no dev proxy for /api.
export async function fetchWorkflow(kbId: string, lane: WorkflowLane): Promise<WorkflowGraph> {
  const res = await authFetch(
    `${API_BASE_URL}/api/kb/${encodeURIComponent(kbId)}/workflow?lane=${lane}`,
  );
  if (!res.ok) throw new Error(`fetch workflow: ${res.status}`);
  return res.json();
}

// GET /api/workflow/presets — global, not KB-scoped: costs are computed from
// the SAME projection the canvas renders (see PricePresets in
// presets_cost.go), so the picker's badges cannot disagree with the graph.
export async function fetchPresets(): Promise<WorkflowPreset[]> {
  const res = await authFetch(`${API_BASE_URL}/api/workflow/presets`);
  if (!res.ok) throw new Error(`fetch presets: ${res.status}`);
  return res.json();
}

// GET /api/kb/{id}/workflow/preset?preset=<id> — a PREVIEW: runs the exact
// same validation/conflict plan the POST below would, but writes nothing.
// This is what the confirmation dialog reads its overwrite count from —
// never computed client-side, so the dialog can never advertise an apply the
// server would then reject (see planApply in preset_apply.go).
export async function previewPreset(kbId: string, presetId: string): Promise<WorkflowPresetApplyResult> {
  const res = await authFetch(
    `${API_BASE_URL}/api/kb/${encodeURIComponent(kbId)}/workflow/preset?preset=${encodeURIComponent(presetId)}`,
  );
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new Error(body.error || `preview preset: ${res.status}`);
  }
  return res.json();
}

// POST /api/kb/{id}/workflow/preset — applies the preset: overwrites every
// bundle key, including ones the admin set by hand. Destructive; the caller
// (PresetPicker) must confirm first, using previewPreset's own count.
export async function applyPreset(kbId: string, presetId: string): Promise<WorkflowPresetApplyResult> {
  const res = await authFetch(
    `${API_BASE_URL}/api/kb/${encodeURIComponent(kbId)}/workflow/preset`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ preset: presetId }),
    },
  );
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new Error(body.error || `apply preset: ${res.status}`);
  }
  return res.json();
}

// Editing goes through the settings endpoints the flat panel already uses:
// PUT validates types, ranges AND mutual-exclusion conflicts, DELETE clears one
// override. Re-exported rather than reimplemented so error handling cannot drift
// between the two surfaces (both read body.error on failure, including the new
// 400 that names mutually-exclusive keys).
export { saveKbSettings, resetKbSetting } from '../api';

/**
 * setKbDefaultBinding writes the KB's default agent/team through the EXISTING
 * attach endpoint (PUT /api/kb/{id}/agents/{agentId} or …/teams/{teamId},
 * body `{isDefault}`) — deliberately not through the settings savebar the rest
 * of this canvas uses. The endpoints and payloads differ, and folding a second
 * call into one Save button creates a partial-failure state (settings written,
 * binding not) with no good recovery.
 *
 * `isDefault: false` is how the binding is CLEARED, addressed at the currently
 * bound link. That really clears rather than no-opping: both attach statements
 * are `INSERT … ON CONFLICT (…, kb_id) DO UPDATE SET is_default =
 * EXCLUDED.is_default` (internal/agentteams/store_pg.go), so an existing row is
 * updated to false. Only `isDefault: true` runs the two clearing UPDATEs first,
 * which is also why a switch from one agent to another needs no explicit clear.
 *
 * The attach endpoint is gated by the ROUTE (kbAdminChain / RequireKBRole
 * admin) and by nothing else. It used to re-check that the caller owned the
 * agent, which 404'd a KB admin operating on their own KB — including when
 * clearing a default someone else had bound, i.e. the exact write this control
 * exists to make possible. That check is gone; what remains is an existence
 * check, so a 404 here now means the agent or team really is not there. Any
 * failure surfaces through the canvas's opError channel like any other write.
 */
export function setKbDefaultBinding(
  kbId: string, target: BindingTarget, id: string, isDefault: boolean,
): Promise<{ success: boolean }> {
  return target === 'team'
    ? attachTeamToKb(kbId, id, isDefault)
    : attachAgentToKb(kbId, id, isDefault);
}

/**
 * fieldFor resolves a config key's registry metadata, or undefined when the key
 * has no registry row.
 *
 * Only 45 of the 100 keys the node vocabulary references are per-KB configurable.
 * A miss is the common case, not an edge case. This helper exists because
 * `graph.fields[key]` type-checks cleanly under this project's tsconfig
 * (noUncheckedIndexedAccess is off) and would throw at runtime on accessing
 * `.type` — callers must handle the undefined explicitly.
 *
 * Tasks 3–5 will use this to safely look up field metadata for each node's keys.
 */
export function fieldFor(
  graph: Pick<WorkflowGraph, 'fields'>,
  key: string,
): WorkflowConfigField | undefined {
  return graph.fields[key];
}
