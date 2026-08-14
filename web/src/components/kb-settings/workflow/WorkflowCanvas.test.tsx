import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type { ReactNode } from 'react';
import { WorkflowCanvas } from './WorkflowCanvas';
import type { WorkflowGraph } from '../../../types';

vi.mock('./api', () => ({ fetchWorkflow: vi.fn() }));
import { fetchWorkflow } from './api';

// React Flow needs layout APIs jsdom lacks; render nodes as plain markup so the
// test asserts behaviour, not the library.
vi.mock('@xyflow/react', () => ({
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  ReactFlow: ({ nodes, nodeTypes }: any) => (
    <div data-testid="rf">
      {/* eslint-disable-next-line @typescript-eslint/no-explicit-any */}
      {nodes.map((n: any) => {
        const C = nodeTypes.workflow;
        return <C key={n.id} id={n.id} data={n.data} />;
      })}
    </div>
  ),
  Background: () => null,
  Controls: () => null,
  Handle: () => null,
  Position: { Top: 'top', Bottom: 'bottom' },
  // ReactFlowProvider/useReactFlow are needed only so WorkflowCanvas.tsx's
  // fitView-on-lane-switch effect has something to call; this suite doesn't
  // assert on viewport behaviour (that's covered by the real, unmocked
  // WorkflowCanvas.keyboard.test.tsx), so a no-op passthrough is enough here.
  ReactFlowProvider: ({ children }: { children: ReactNode }) => children,
  useReactFlow: () => ({ fitView: vi.fn() }),
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  useNodesState: (init: any) => [init, vi.fn(), vi.fn()],
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  useEdgesState: (init: any) => [init, vi.fn(), vi.fn()],
}));

const graph = (over: Partial<WorkflowGraph> = {}): WorkflowGraph => ({
  lane: 'complex_reasoning',
  nodes: [
    { id: 'retrieve', label: 'Retrieval', group: 'Suche', help: 'Hybride Suche.', keys: [], alwaysOn: true, llmCalls: 0, latencyMs: 400, activation: 'active', values: {}, origins: {}, editable: false },
    { id: 'crag_grade', label: 'CRAG-Bewertung', group: 'Korrektur', help: 'Bewertet Textstellen.', keys: ['crag_enabled'], alwaysOn: false, llmCalls: 1, latencyMs: 600, activation: 'conditional', reason: 'orchestrator_bypass', condition: 'Läuft hier nicht.', values: { crag_enabled: 'true' }, origins: { crag_enabled: 'kb' }, editable: true },
  ],
  edges: [{ from: 'retrieve', to: 'crag_grade', label: '', loop: false, maxIterations: 0 }],
  orchestrators: [{ orchestrator: 'supervisor', activation: 'active' }],
  estLlmCalls: 4,
  estLatencyMs: 5200,
  ...over,
});

describe('WorkflowCanvas', () => {
  // beforeEach is deliberately async (even though mockReset() itself is
  // synchronous): a synchronous beforeEach that mutates a mock right before a
  // test whose FIRST act creates a rejected promise makes Vitest's unhandled-
  // rejection tracker misfire — attributing a rejection this test's own
  // .catch() already handles as an uncaught failure. Confirmed with a minimal
  // repro with no React/testing-library involved at all (plain vi.fn() +
  // beforeEach + a rejected .then().catch() chain in the following test);
  // wrapping the hook in `async` (even with nothing to await) resolves it by
  // inserting a task boundary between the hook and the test body.
  beforeEach(async () => { vi.mocked(fetchWorkflow).mockReset(); });

  it('loads the complex lane by default', async () => {
    vi.mocked(fetchWorkflow).mockResolvedValue(graph());
    render(<WorkflowCanvas kbId="kb-1" />);
    await waitFor(() => expect(fetchWorkflow).toHaveBeenCalledWith('kb-1', 'complex_reasoning'));
  });

  it('renders a node per projection node', async () => {
    vi.mocked(fetchWorkflow).mockResolvedValue(graph());
    render(<WorkflowCanvas kbId="kb-1" />);
    expect(await screen.findByText('Retrieval')).toBeInTheDocument();
    expect(screen.getByText('CRAG-Bewertung')).toBeInTheDocument();
  });

  it('refetches when the lane changes', async () => {
    vi.mocked(fetchWorkflow).mockResolvedValue(graph());
    render(<WorkflowCanvas kbId="kb-1" />);
    await screen.findByText('Retrieval');
    await userEvent.click(screen.getByRole('button', { name: 'Nachschlagen' }));
    await waitFor(() => expect(fetchWorkflow).toHaveBeenCalledWith('kb-1', 'lookup'));
  });

  it('shows the cost estimate, labelled as an estimate', async () => {
    vi.mocked(fetchWorkflow).mockResolvedValue(graph());
    render(<WorkflowCanvas kbId="kb-1" />);
    expect(await screen.findByText(/gesch(ä|ae)tzt/i)).toBeInTheDocument();
    expect(screen.getByText(/4/)).toBeInTheDocument();
  });

  it('opens the inspector when a node is clicked', async () => {
    vi.mocked(fetchWorkflow).mockResolvedValue(graph());
    render(<WorkflowCanvas kbId="kb-1" />);
    await userEvent.click(await screen.findByTestId('wf-node-crag_grade'));
    expect(await screen.findByText('Läuft hier nicht.')).toBeInTheDocument();
  });

  it('surfaces a load failure instead of rendering an empty canvas', async () => {
    vi.mocked(fetchWorkflow).mockRejectedValue(new Error('fetch workflow: 403'));
    render(<WorkflowCanvas kbId="kb-1" />);
    expect(await screen.findByRole('alert')).toBeInTheDocument();
  });
});
