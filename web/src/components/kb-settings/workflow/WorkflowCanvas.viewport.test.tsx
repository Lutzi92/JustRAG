import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { WorkflowCanvas } from './WorkflowCanvas';
import type { WorkflowGraph } from '../../../types';

// A STABLE spy, shared by every render. The old suite's
// `useReactFlow: () => ({ fitView: vi.fn() })` handed back a fresh no-op on
// every render, so nobody could observe a call — which is exactly what hid the
// re-fit-on-every-click bug for seven task reviews.
const fitView = vi.fn();

// Partial mock: React Flow stays REAL (this file must exercise the actual
// node/selection wiring, since the bug lives in RF's onSelectNodeHandler ->
// onNodesChange -> applyChanges path). Only useReactFlow is wrapped, so the
// component still gets a working store and we still get a spy on fitView.
vi.mock('@xyflow/react', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@xyflow/react')>();
  return {
    ...actual,
    useReactFlow: () => ({ ...actual.useReactFlow(), fitView }),
  };
});

// WorkflowCanvas reads useReducedMotion, which calls window.matchMedia —
// not available in jsdom by default (same treatment as Login.test.tsx).
vi.mock('../../../hooks/useReducedMotion', () => ({
  useReducedMotion: () => false,
  getMotionProps: () => ({}),
}));

vi.mock('./api', () => ({ fetchWorkflow: vi.fn() }));
import { fetchWorkflow } from './api';

const graph = (lane: WorkflowGraph['lane']): WorkflowGraph => ({
  lane,
  nodes: [
    { id: 'retrieve', label: 'Retrieval', group: 'Retrieval', help: '', keys: [], alwaysOn: true, llmCalls: 0, latencyMs: 400, activation: 'active', values: {}, origins: {}, editable: false },
    { id: 'crag_grade', label: 'CRAG-Bewertung', group: 'Korrektur', help: '', keys: [], alwaysOn: false, llmCalls: 1, latencyMs: 600, activation: 'active', values: {}, origins: {}, editable: true },
  ],
  edges: [{ from: 'retrieve', to: 'crag_grade', label: '', loop: false, maxIterations: 0 }],
  orchestrators: [],
  estLlmCalls: 1,
  estLatencyMs: 1000,
});

describe('WorkflowCanvas viewport', () => {
  beforeEach(async () => {
    vi.mocked(fetchWorkflow).mockReset();
    fitView.mockReset();
  });

  it('fits the view once when the graph first arrives', async () => {
    vi.mocked(fetchWorkflow).mockResolvedValue(graph('complex_reasoning'));
    render(<WorkflowCanvas kbId="kb-1" />);
    await screen.findByTestId('wf-node-crag_grade');
    await waitFor(() => expect(fitView).toHaveBeenCalled());
  });

  it('does NOT re-fit when a node is clicked — the user keeps their zoom', async () => {
    // `elementsSelectable` is on, so RF wires onSelectNodeHandler on every
    // node. A click runs handleNodeClick -> triggerNodeChanges ->
    // onNodesChange -> applyChanges, which ALWAYS returns a new array. An
    // effect keyed on `nodes` therefore re-fit the viewport on the one
    // interaction this surface exists to support: clicking a node to read it.
    vi.mocked(fetchWorkflow).mockResolvedValue(graph('complex_reasoning'));
    render(<WorkflowCanvas kbId="kb-1" />);
    const node = await screen.findByTestId('wf-node-crag_grade');
    await waitFor(() => expect(fitView).toHaveBeenCalled());
    fitView.mockClear();

    fireEvent.click(node);
    await screen.findByLabelText('Details zu CRAG-Bewertung');

    expect(fitView).not.toHaveBeenCalled();
  });

  it('does NOT re-fit on Enter activation either', async () => {
    vi.mocked(fetchWorkflow).mockResolvedValue(graph('complex_reasoning'));
    render(<WorkflowCanvas kbId="kb-1" />);
    const node = await screen.findByTestId('wf-node-crag_grade');
    await waitFor(() => expect(fitView).toHaveBeenCalled());
    fitView.mockClear();

    const rfNode = node.closest('.react-flow__node') as HTMLElement;
    rfNode.focus();
    fireEvent.keyDown(rfNode, { key: 'Enter' });
    await screen.findByLabelText('Details zu CRAG-Bewertung');

    expect(fitView).not.toHaveBeenCalled();
  });

  it('DOES re-fit when the lane changes', async () => {
    vi.mocked(fetchWorkflow).mockResolvedValue(graph('complex_reasoning'));
    render(<WorkflowCanvas kbId="kb-1" />);
    await screen.findByTestId('wf-node-crag_grade');
    await waitFor(() => expect(fitView).toHaveBeenCalled());
    fitView.mockClear();

    vi.mocked(fetchWorkflow).mockResolvedValue(graph('lookup'));
    await userEvent.click(screen.getByRole('button', { name: 'Nachschlagen' }));

    await waitFor(() => expect(fitView).toHaveBeenCalled());
  });
});
