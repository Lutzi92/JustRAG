import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { fetchWorkflow } from './api';

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
