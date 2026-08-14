import { render, screen, waitFor, fireEvent } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, it, expect, vi, beforeEach, beforeAll, afterEach } from 'vitest';
import { KbSettingsPanel } from './KbSettingsPanel';
import { API_BASE_URL } from '../../api';

// The Workflow tab mounts the real (unmocked) React Flow canvas, whose
// internal node-measuring hook requires ResizeObserver — absent in jsdom
// (same gap, same fix as WorkflowCanvas.keyboard.test.tsx and
// WorkflowNode.focus.test.tsx).
beforeAll(() => {
  class FakeResizeObserver {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
  globalThis.ResizeObserver = FakeResizeObserver as unknown as typeof ResizeObserver;
});

const showConfirm = vi.fn(async () => true);
const showAlert = vi.fn(async () => {});
vi.mock('../../contexts/ModalContext', () => ({
  useModalContext: () => ({ showConfirm, showAlert }),
}));

vi.mock('./workflow/api', () => ({
  fetchWorkflow: vi.fn().mockResolvedValue({
    lane: 'complex_reasoning', nodes: [], edges: [], orchestrators: [],
    estLlmCalls: 0, estLatencyMs: 0,
  }),
}));

const sample = {
  registry: [
    { key: 'rerank_blend_alpha', type: 'float', group: 'Retrieval', label: 'Reranker blend α', help: 'h', min: 0, max: 1 },
    { key: 'crag_enabled', type: 'bool', group: 'Corrective', label: 'Corrective RAG', help: 'h' },
    { key: 'chat_graph_routing_path_mode', type: 'enum', group: 'Knowledge graph', label: 'Graph mode', help: 'h', enum: ['neighbors', 'ppr', 'paths'] },
    { key: 'raptor_enabled', type: 'bool', group: 'Ingestion', label: 'RAPTOR indexing', help: 'h', requiresReingest: true },
  ],
  values: {
    rerank_blend_alpha: { override: '0.3', global: '0.8', effective: '0.3' },
    crag_enabled: { override: null, global: 'true', effective: 'true' },
    chat_graph_routing_path_mode: { override: null, global: 'neighbors', effective: 'neighbors' },
    raptor_enabled: { override: null, global: 'false', effective: 'false' },
  },
};

beforeEach(() => {
  showConfirm.mockClear();
  showAlert.mockClear();
  vi.stubGlobal('localStorage', {
    getItem: () => null,
    setItem: () => {},
    removeItem: () => {},
    clear: () => {},
  });
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
  global.fetch = vi.fn(async (url: string, opts?: RequestInit) => {
    if (opts?.method === 'POST' && String(url).endsWith('/reembed')) {
      return { ok: true, json: async () => ({ queued: 3 }) } as Response;
    }
    if (opts?.method === 'PUT') return { ok: true, json: async () => ({ success: true }) } as Response;
    return { ok: true, json: async () => sample } as Response;
  }) as unknown as typeof fetch;
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('KbSettingsPanel', () => {
  // The OpenAI-compatible endpoint only accepts "kb-{uuid}" in `model`
  // (extractKBID in internal/openaicompat/handler.go), so the panel shows the
  // prefixed form rather than the raw KB uuid.
  it('shows the kb- prefixed API model name and copies it to the clipboard', async () => {
    const writeText = vi.fn(async () => {});
    vi.stubGlobal('navigator', { clipboard: { writeText } });
    render(<KbSettingsPanel kbId="kb1" />);
    expect(screen.getByTestId('kb-model-value').textContent).toBe('kb-kb1');
    fireEvent.click(screen.getByLabelText('Copy API model name'));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith('kb-kb1'));
    await waitFor(() => screen.getByText('Copied'));
  });

  it('renders effective values and marks overridden fields', async () => {
    render(<KbSettingsPanel kbId="kb1" />);
    await waitFor(() => screen.getByText('Reranker blend α'));
    expect(screen.getByTestId('override-badge-rerank_blend_alpha')).toBeTruthy();
    expect(screen.queryByTestId('override-badge-crag_enabled')).toBeNull();
  });

  it('saves changed values via PUT without prompting re-ingest for non-ingest keys', async () => {
    render(<KbSettingsPanel kbId="kb1" />);
    await waitFor(() => screen.getByText('Corrective RAG'));
    fireEvent.click(screen.getByTestId('field-crag_enabled') as HTMLInputElement);
    fireEvent.click(screen.getByText('Save'));
    await waitFor(() => {
      expect(global.fetch).toHaveBeenCalledWith(
        `${API_BASE_URL}/api/kb/kb1/settings`,
        expect.objectContaining({ method: 'PUT' }),
      );
    });
    expect(showConfirm).not.toHaveBeenCalled();
  });

  it('does not call /reembed or showAlert when user cancels the re-ingest prompt', async () => {
    showConfirm.mockResolvedValueOnce(false);
    render(<KbSettingsPanel kbId="kb1" />);
    await waitFor(() => screen.getByText('RAPTOR indexing'));
    fireEvent.click(screen.getByTestId('field-raptor_enabled') as HTMLInputElement);
    fireEvent.click(screen.getByText('Save'));
    await waitFor(() => expect(showConfirm).toHaveBeenCalled());
    // Settings PUT must have been called.
    expect(global.fetch).toHaveBeenCalledWith(
      `${API_BASE_URL}/api/kb/kb1/settings`,
      expect.objectContaining({ method: 'PUT' }),
    );
    // No /reembed POST when the user declines.
    const fetchMock = global.fetch as ReturnType<typeof vi.fn>;
    const reembedCall = fetchMock.mock.calls.find(
      (call) => String(call[0]).endsWith('/reembed'),
    );
    expect(reembedCall).toBeUndefined();
    expect(showAlert).not.toHaveBeenCalled();
  });

  it('prompts and schedules re-ingest when an ingest key changes', async () => {
    render(<KbSettingsPanel kbId="kb1" />);
    await waitFor(() => screen.getByText('RAPTOR indexing'));
    fireEvent.click(screen.getByTestId('field-raptor_enabled') as HTMLInputElement);
    fireEvent.click(screen.getByText('Save'));
    await waitFor(() => expect(showConfirm).toHaveBeenCalled());
    await waitFor(() => {
      expect(global.fetch).toHaveBeenCalledWith(
        `${API_BASE_URL}/api/kb/kb1/reembed`,
        expect.objectContaining({ method: 'POST' }),
      );
    });
    await waitFor(() => expect(showAlert).toHaveBeenCalled());
  });

  it('shows the workflow tab and renders the canvas when selected', async () => {
    render(<KbSettingsPanel kbId="kb-1" />);

    const tab = await screen.findByRole('button', { name: /workflow/i });
    await userEvent.click(tab);

    expect(await screen.findByRole('button', { name: 'Komplexe Frage' })).toBeInTheDocument();
  });
});
