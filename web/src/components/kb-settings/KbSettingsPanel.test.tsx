import { render, screen, waitFor, fireEvent } from '@testing-library/react';
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { KbSettingsPanel } from './KbSettingsPanel';
import { API_BASE_URL } from '../../api';

const sample = {
  registry: [
    { key: 'rerank_blend_alpha', type: 'float', group: 'Retrieval', label: 'Reranker blend α', help: 'h', min: 0, max: 1 },
    { key: 'crag_enabled', type: 'bool', group: 'Corrective', label: 'Corrective RAG', help: 'h' },
    { key: 'chat_graph_routing_path_mode', type: 'enum', group: 'Knowledge graph', label: 'Graph mode', help: 'h', enum: ['neighbors', 'ppr', 'paths'] },
  ],
  values: {
    rerank_blend_alpha: { override: '0.3', global: '0.8', effective: '0.3' },
    crag_enabled: { override: null, global: 'true', effective: 'true' },
    chat_graph_routing_path_mode: { override: null, global: 'neighbors', effective: 'neighbors' },
  },
};

beforeEach(() => {
  // authFetch (used by the api client) reads localStorage for the bearer token;
  // jsdom's localStorage isn't reliably present in this env, so stub it.
  vi.stubGlobal('localStorage', {
    getItem: () => null,
    setItem: () => {},
    removeItem: () => {},
    clear: () => {},
  });
  // useReducedMotion calls window.matchMedia, which jsdom doesn't implement.
  vi.stubGlobal('matchMedia', (query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener: () => {},
    removeEventListener: () => {},
    addListener: () => {},
    removeListener: () => {},
    dispatchEvent: () => false,
  }));
  global.fetch = vi.fn(async (_url: string, opts?: RequestInit) => {
    if (opts?.method === 'PUT') return { ok: true, json: async () => ({ success: true }) } as Response;
    return { ok: true, json: async () => sample } as Response;
  }) as unknown as typeof fetch;
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('KbSettingsPanel', () => {
  it('renders effective values and marks overridden fields', async () => {
    render(<KbSettingsPanel kbId="kb1" />);
    await waitFor(() => screen.getByText('Reranker blend α'));
    // Overridden field shows the override indicator.
    expect(screen.getByTestId('override-badge-rerank_blend_alpha')).toBeTruthy();
    // Inherited field does not.
    expect(screen.queryByTestId('override-badge-crag_enabled')).toBeNull();
  });

  it('saves changed values via PUT', async () => {
    render(<KbSettingsPanel kbId="kb1" />);
    await waitFor(() => screen.getByText('Corrective RAG'));
    const toggle = screen.getByTestId('field-crag_enabled') as HTMLInputElement;
    fireEvent.click(toggle);
    fireEvent.click(screen.getByText('Save'));
    await waitFor(() => {
      expect(global.fetch).toHaveBeenCalledWith(
        `${API_BASE_URL}/api/kb/kb1/settings`,
        expect.objectContaining({ method: 'PUT' }),
      );
    });
  });
});
