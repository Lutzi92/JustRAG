import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type { ReactNode } from 'react';
import { WorkflowCanvas } from './WorkflowCanvas';
import { MIN_ZOOM } from './layout';
import type { WorkflowGraph } from '../../../types';

vi.mock('./api', () => ({ fetchWorkflow: vi.fn() }));
import { fetchWorkflow } from './api';

// WorkflowCanvas reads useReducedMotion, which calls window.matchMedia —
// not available in jsdom by default (same treatment as Login.test.tsx).
vi.mock('../../../hooks/useReducedMotion', () => ({
  useReducedMotion: () => false,
  getMotionProps: () => ({}),
}));

// Captures the props the canvas hands <ReactFlow>, so viewport configuration
// (minZoom) is assertable without a real render, where jsdom's zero-sized
// boxes make the actual zoom meaningless.
// eslint-disable-next-line @typescript-eslint/no-explicit-any
let lastRfProps: any = null;

// React Flow needs layout APIs jsdom lacks; render nodes as plain markup so the
// test asserts behaviour, not the library.
vi.mock('@xyflow/react', () => ({
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  ReactFlow: (props: any) => {
    lastRfProps = props;
    return (
      <div data-testid="rf">
        {/* eslint-disable-next-line @typescript-eslint/no-explicit-any */}
        {props.nodes.map((n: any) => {
          const C = props.nodeTypes.workflow;
          return <C key={n.id} id={n.id} data={n.data} />;
        })}
      </div>
    );
  },
  Background: () => null,
  Controls: () => null,
  Handle: () => null,
  Position: { Top: 'top', Bottom: 'bottom' },
  // The viewport behaviour this stub can't express (does clicking a node
  // re-fit?) is covered against real, unmocked React Flow with a stable spy in
  // WorkflowCanvas.viewport.test.tsx. A per-render `vi.fn()` here would be
  // unobservable — which is how the re-fit-on-click bug stayed hidden.
  ReactFlowProvider: ({ children }: { children: ReactNode }) => children,
  useReactFlow: () => ({ fitView: () => {} }),
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
  fields: {},
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
  beforeEach(async () => { vi.mocked(fetchWorkflow).mockReset(); lastRfProps = null; });

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

  it('hands React Flow a minZoom below its 0.5 default, so the fit is not clamped', async () => {
    vi.mocked(fetchWorkflow).mockResolvedValue(graph());
    render(<WorkflowCanvas kbId="kb-1" />);
    await screen.findByText('Retrieval');
    expect(lastRfProps.minZoom).toBe(MIN_ZOOM);
    expect(lastRfProps.minZoom).toBeLessThan(0.5);
  });

  describe('orchestrator chips', () => {
    const withOrchestrators = () => graph({
      orchestrators: [
        { orchestrator: 'corpus_table', activation: 'conditional', condition: 'wenn die Frage einen Vergleich über mehrere Dokumente verlangt' },
        { orchestrator: 'plan_execute', activation: 'active' },
      ],
    });

    it('shows German names, never the raw backend identifier', async () => {
      // project.go:232-241 owns these names but deliberately keeps them off the
      // wire, so the mapping is the frontend's job. Until now the chips printed
      // `plan_execute` and `corpus_table` verbatim to a German-speaking user.
      vi.mocked(fetchWorkflow).mockResolvedValue(withOrchestrators());
      render(<WorkflowCanvas kbId="kb-1" />);
      expect(await screen.findByText('Korpus-Vergleichstabelle')).toBeInTheDocument();
      expect(screen.getByText('Plan-and-Execute')).toBeInTheDocument();
      expect(screen.queryByText('corpus_table')).not.toBeInTheDocument();
      expect(screen.queryByText('plan_execute')).not.toBeInTheDocument();
    });

    it('labels the group so three bare words are not left unexplained', async () => {
      vi.mocked(fetchWorkflow).mockResolvedValue(withOrchestrators());
      render(<WorkflowCanvas kbId="kb-1" />);
      expect(await screen.findByText('Orchestrator:')).toBeInTheDocument();
    });

    it('carries the activation state as text, not hue alone', async () => {
      vi.mocked(fetchWorkflow).mockResolvedValue(withOrchestrators());
      render(<WorkflowCanvas kbId="kb-1" />);
      const chip = (await screen.findByText('Korpus-Vergleichstabelle')).closest('li')!;
      expect(chip).toHaveTextContent('bedingt');
      const active = screen.getByText('Plan-and-Execute').closest('li')!;
      expect(active).toHaveTextContent('läuft');
    });

    it('surfaces the condition explaining when a conditional orchestrator wins', async () => {
      vi.mocked(fetchWorkflow).mockResolvedValue(withOrchestrators());
      render(<WorkflowCanvas kbId="kb-1" />);
      expect(await screen.findByText(/Vergleich über mehrere Dokumente/)).toBeInTheDocument();
    });

    it('falls back to the raw id for an orchestrator it does not know', async () => {
      vi.mocked(fetchWorkflow).mockResolvedValue(graph({
        orchestrators: [{ orchestrator: 'brand_new', activation: 'active' }],
      }));
      render(<WorkflowCanvas kbId="kb-1" />);
      expect(await screen.findByText('brand_new')).toBeInTheDocument();
    });
  });

  it('uses the same word for the conditional state in the legend as on a node badge', async () => {
    // The legend said "bedingt" while the node badge said "Übersprungen" —
    // one state reading as two different things on the same screen.
    vi.mocked(fetchWorkflow).mockResolvedValue(graph());
    render(<WorkflowCanvas kbId="kb-1" />);
    expect(await screen.findByText('bedingt')).toBeInTheDocument();
    expect(screen.getByText('Bedingt')).toBeInTheDocument(); // the crag_grade badge
  });
});
