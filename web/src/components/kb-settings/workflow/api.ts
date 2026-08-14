import type { WorkflowConfigField, WorkflowGraph, WorkflowLane } from '../../../types';
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
