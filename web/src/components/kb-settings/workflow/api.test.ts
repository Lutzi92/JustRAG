import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { fetchWorkflow, saveKbSettings, resetKbSetting, fieldFor } from './api';
import type { WorkflowGraph } from '../../../types';
import * as settingsApi from '../api';

vi.mock('../../../api', () => ({
  API_BASE_URL: 'http://test.local',
  authFetch: vi.fn(),
}));

import { authFetch } from '../../../api';

describe('fetchWorkflow', () => {
  beforeEach(() => vi.mocked(authFetch).mockReset());
  afterEach(() => vi.restoreAllMocks());

  it('requests the workflow endpoint with the lane as a query parameter', async () => {
    vi.mocked(authFetch).mockResolvedValue({
      ok: true,
      json: async () => ({ lane: 'lookup', nodes: [], edges: [], orchestrators: [], estLlmCalls: 0, estLatencyMs: 0 }),
    } as unknown as Response);

    const g = await fetchWorkflow('kb-1', 'lookup');

    expect(authFetch).toHaveBeenCalledWith('http://test.local/api/kb/kb-1/workflow?lane=lookup');
    expect(g.lane).toBe('lookup');
  });

  it('url-encodes the kb id', async () => {
    vi.mocked(authFetch).mockResolvedValue({
      ok: true,
      json: async () => ({ lane: 'lookup', nodes: [], edges: [], orchestrators: [], estLlmCalls: 0, estLatencyMs: 0 }),
    } as unknown as Response);

    await fetchWorkflow('kb/1', 'lookup');

    expect(authFetch).toHaveBeenCalledWith('http://test.local/api/kb/kb%2F1/workflow?lane=lookup');
  });

  it('throws with the status on a non-ok response', async () => {
    vi.mocked(authFetch).mockResolvedValue({ ok: false, status: 403 } as unknown as Response);

    await expect(fetchWorkflow('kb-1', 'complex_reasoning')).rejects.toThrow('403');
  });
});

describe('re-exports', () => {
  it('re-exports saveKbSettings and resetKbSetting as the same function objects', () => {
    // Re-exported rather than reimplemented so error handling cannot drift
    // between the workflow canvas and the flat settings panel (both surfaces
    // use the same endpoints and read body.error on 4xx).
    expect(saveKbSettings).toBe(settingsApi.saveKbSettings);
    expect(resetKbSetting).toBe(settingsApi.resetKbSetting);
  });
});

// resetKbSetting is a genuine re-export (not a mock) here, so this exercises
// the real implementation in ../api.ts. DELETE /settings/{key} can 400 on a
// conflict (clearing an override may fall the key back to a global value
// that conflicts with another enabled key), and handler.go writes the
// specific reason to body.error — the same shape saveKbSettings already read.
// Before this fix, resetKbSetting only ever threw a bare "reset setting:
// 400", discarding that reason.
describe('resetKbSetting error handling', () => {
  beforeEach(() => vi.mocked(authFetch).mockReset());
  afterEach(() => vi.restoreAllMocks());

  it('surfaces the server-provided reason on a 400, not just the status code', async () => {
    vi.mocked(authFetch).mockResolvedValue({
      ok: false,
      status: 400,
      json: async () => ({
        error: 'clearing crag_enabled would fall back to a global value that conflicts with chat_self_rag_enabled',
      }),
    } as unknown as Response);

    await expect(resetKbSetting('kb-1', 'crag_enabled')).rejects.toThrow(
      /chat_self_rag_enabled/,
    );
  });

  it('falls back to the bare status when the body carries no error field', async () => {
    vi.mocked(authFetch).mockResolvedValue({
      ok: false,
      status: 500,
      json: async () => { throw new Error('not json'); },
    } as unknown as Response);

    await expect(resetKbSetting('kb-1', 'crag_enabled')).rejects.toThrow('500');
  });
});

describe('fieldFor', () => {
  const graph: WorkflowGraph = {
    lane: 'lookup',
    nodes: [],
    edges: [],
    orchestrators: [],
    estLlmCalls: 0,
    estLatencyMs: 0,
    fields: {
      query_cache_enabled: {
        key: 'query_cache_enabled',
        type: 'bool',
        group: 'Retrieval',
        label: 'Anfragen-Cache',
        help: 'Cache beschreibung',
      },
    },
    presetBase: '',
    presetBaseKnown: true,
    deviations: [],
    agentBinding: { kind: '', id: '', name: '', disabled: false, emptyTeam: false, options: [] },
  };

  it('returns the field entry for a registered key', () => {
    const field = fieldFor(graph, 'query_cache_enabled');

    expect(field).toBeDefined();
    expect(field?.key).toBe('query_cache_enabled');
    expect(field?.type).toBe('bool');
  });

  it('returns undefined for an unregistered key', () => {
    const field = fieldFor(graph, 'nonexistent_key');

    expect(field).toBeUndefined();
  });
});
