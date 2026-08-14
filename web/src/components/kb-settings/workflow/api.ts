import type { WorkflowGraph, WorkflowLane } from '../../../types';
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
