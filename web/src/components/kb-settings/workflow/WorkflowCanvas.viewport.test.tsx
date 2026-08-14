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

// fieldFor stays real (via importOriginal), same treatment as the main
// WorkflowCanvas.test.tsx mock: NodeInspector needs a genuine lookup for the
// new save-path test below, which registers an editable field.
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>();
  return {
    ...actual,
    fetchWorkflow: vi.fn(),
    saveKbSettings: vi.fn(),
    resetKbSetting: vi.fn(),
    // PresetPicker (Task 5) fetches this on mount unconditionally; this
    // suite is about viewport/fitView behaviour, not the picker itself.
    fetchPresets: vi.fn(),
  };
});
import { fetchWorkflow, saveKbSettings, fetchPresets } from './api';

const graph = (lane: WorkflowGraph['lane'], over: Partial<WorkflowGraph> = {}): WorkflowGraph => ({
  lane,
  nodes: [
    { id: 'retrieve', label: 'Retrieval', group: 'Retrieval', help: '', keys: [], alwaysOn: true, llmCalls: 0, latencyMs: 400, activation: 'active', values: {}, origins: {}, editable: false },
    { id: 'crag_grade', label: 'CRAG-Bewertung', group: 'Korrektur', help: '', keys: [], alwaysOn: false, llmCalls: 1, latencyMs: 600, activation: 'active', values: {}, origins: {}, editable: true },
  ],
  edges: [{ from: 'retrieve', to: 'crag_grade', label: '', loop: false, maxIterations: 0 }],
  orchestrators: [],
  estLlmCalls: 1,
  estLatencyMs: 1000,
  fields: {},
  presetBase: '',
  presetBaseKnown: true,
  deviations: [],
  ...over,
});

describe('WorkflowCanvas viewport', () => {
  beforeEach(async () => {
    vi.mocked(fetchWorkflow).mockReset();
    vi.mocked(saveKbSettings).mockReset();
    vi.mocked(fetchPresets).mockReset().mockResolvedValue([]);
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

  // IMPORTANT 2 regression test: the `laid`-push effect used to arm the
  // refit latch on EVERY graph change, including a same-lane save refetch.
  // Node positions don't change on a save (same lane, same node set), so
  // that re-fit was pure viewport disruption — an admin who zoomed in to
  // read the node they were about to edit lost that zoom the instant the
  // save that node's own edit triggered came back.
  it('does NOT re-fit after a successful save — same lane, same node set', async () => {
    const withField = graph('complex_reasoning', {
      nodes: [
        { id: 'retrieve', label: 'Retrieval', group: 'Retrieval', help: '', keys: [], alwaysOn: true, llmCalls: 0, latencyMs: 400, activation: 'active', values: {}, origins: {}, editable: false },
        { id: 'crag_grade', label: 'CRAG-Bewertung', group: 'Korrektur', help: '', keys: ['crag_enabled'], alwaysOn: false, llmCalls: 1, latencyMs: 600, activation: 'active', values: { crag_enabled: 'true' }, origins: { crag_enabled: 'kb' }, editable: true },
      ],
      fields: {
        crag_enabled: { key: 'crag_enabled', type: 'bool', group: 'Korrektur', label: 'CRAG aktiviert', help: '' },
      },
    });
    vi.mocked(fetchWorkflow).mockResolvedValue(withField);
    vi.mocked(saveKbSettings).mockResolvedValue(undefined);
    render(<WorkflowCanvas kbId="kb-1" />);

    const node = await screen.findByTestId('wf-node-crag_grade');
    await waitFor(() => expect(fitView).toHaveBeenCalled());
    fitView.mockClear();

    fireEvent.click(node);
    await screen.findByLabelText('Details zu CRAG-Bewertung');
    await userEvent.selectOptions(screen.getByRole('combobox', { name: 'CRAG aktiviert' }), 'false');
    await userEvent.click(screen.getByRole('button', { name: /speichern/i }));

    await waitFor(() => expect(fetchWorkflow).toHaveBeenCalledTimes(2));
    expect(fitView).not.toHaveBeenCalled();
  });
});
