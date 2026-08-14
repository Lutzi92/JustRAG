import type {
  WorkflowConfigField, WorkflowGraph, WorkflowLane, WorkflowPreset, WorkflowPresetApplyResult,
} from '../../../types';
import { API_BASE_URL, authFetch } from '../../../api';

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
