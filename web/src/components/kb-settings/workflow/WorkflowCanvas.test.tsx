import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type { ReactNode } from 'react';
import { WorkflowCanvas } from './WorkflowCanvas';
import { MIN_ZOOM } from './layout';
import type { WorkflowGraph } from '../../../types';

// fieldFor is real (not a vi.fn()): NodeInspector, rendered inside this
// canvas, imports it from the same module and needs a genuine lookup, not a
// second mock to keep in sync with fixture `fields` maps across every test.
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>();
  return {
    ...actual,
    fetchWorkflow: vi.fn(),
    saveKbSettings: vi.fn(),
    resetKbSetting: vi.fn(),
  };
});
import { fetchWorkflow, saveKbSettings, resetKbSetting } from './api';

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
  beforeEach(async () => {
    vi.mocked(fetchWorkflow).mockReset();
    vi.mocked(saveKbSettings).mockReset();
    vi.mocked(resetKbSetting).mockReset();
    lastRfProps = null;
  });

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

  // --- Task 5: save, reset, refetch ---

  // Two independently-editable nodes with distinct keys, both registered —
  // the minimum shape that can exercise the batching guarantee (an edit on
  // one node plus an edit on another must land in the SAME PUT).
  const editableGraph = (): WorkflowGraph => graph({
    nodes: [
      { id: 'retrieve', label: 'Retrieval', group: 'Suche', help: 'Hybride Suche.', keys: [], alwaysOn: true, llmCalls: 0, latencyMs: 400, activation: 'active', values: {}, origins: {}, editable: false },
      { id: 'crag_grade', label: 'CRAG-Bewertung', group: 'Korrektur', help: 'Bewertet Textstellen.', keys: ['crag_enabled'], alwaysOn: false, llmCalls: 1, latencyMs: 600, activation: 'conditional', reason: 'orchestrator_bypass', condition: 'Läuft hier nicht.', values: { crag_enabled: 'true' }, origins: { crag_enabled: 'kb' }, editable: true },
      { id: 'self_rag', label: 'Self-RAG', group: 'Verifikation', help: 'Selbstprüfung der Antwort.', keys: ['chat_self_rag_enabled'], alwaysOn: false, llmCalls: 1, latencyMs: 300, activation: 'active', values: { chat_self_rag_enabled: 'false' }, origins: { chat_self_rag_enabled: 'global' }, editable: true },
    ],
    edges: [{ from: 'retrieve', to: 'crag_grade', label: '', loop: false, maxIterations: 0 }],
    fields: {
      crag_enabled: { key: 'crag_enabled', type: 'bool', group: 'Korrektur', label: 'CRAG aktiviert', help: '' },
      chat_self_rag_enabled: { key: 'chat_self_rag_enabled', type: 'bool', group: 'Verifikation', label: 'Self-RAG aktiviert', help: '' },
    },
  });

  const stringFieldGraph = (): WorkflowGraph => graph({
    nodes: [
      { id: 'retrieve', label: 'Retrieval', group: 'Suche', help: '', keys: [], alwaysOn: true, llmCalls: 0, latencyMs: 400, activation: 'active', values: {}, origins: {}, editable: false },
      { id: 'tz_node', label: 'Zeitzone', group: 'Datum', help: '', keys: ['chat_date_timezone'], alwaysOn: false, llmCalls: 0, latencyMs: 0, activation: 'active', values: { chat_date_timezone: 'Europe/Berlin' }, origins: { chat_date_timezone: 'kb' }, editable: true },
    ],
    edges: [],
    fields: {
      chat_date_timezone: { key: 'chat_date_timezone', type: 'string', group: 'Datum', label: 'Zeitzone', help: '' },
    },
  });

  // Both an editable bool field AND an editable string field, on separate
  // nodes — needed for tests that must trigger the empty-string refusal hint
  // on one node while also having a normal, savable edit available on
  // another.
  const mixedGraph = (): WorkflowGraph => graph({
    nodes: [
      { id: 'retrieve', label: 'Retrieval', group: 'Suche', help: '', keys: [], alwaysOn: true, llmCalls: 0, latencyMs: 400, activation: 'active', values: {}, origins: {}, editable: false },
      { id: 'crag_grade', label: 'CRAG-Bewertung', group: 'Korrektur', help: '', keys: ['crag_enabled'], alwaysOn: false, llmCalls: 1, latencyMs: 600, activation: 'active', values: { crag_enabled: 'true' }, origins: { crag_enabled: 'kb' }, editable: true },
      { id: 'tz_node', label: 'Zeitzone', group: 'Datum', help: '', keys: ['chat_date_timezone'], alwaysOn: false, llmCalls: 0, latencyMs: 0, activation: 'active', values: { chat_date_timezone: 'Europe/Berlin' }, origins: { chat_date_timezone: 'kb' }, editable: true },
    ],
    edges: [],
    fields: {
      crag_enabled: { key: 'crag_enabled', type: 'bool', group: 'Korrektur', label: 'CRAG aktiviert', help: '' },
      chat_date_timezone: { key: 'chat_date_timezone', type: 'string', group: 'Datum', label: 'Zeitzone', help: '' },
    },
  });

  describe('save / reset / refetch', () => {
    it('shows the save bar with a count once a field is edited', async () => {
      vi.mocked(fetchWorkflow).mockResolvedValue(editableGraph());
      render(<WorkflowCanvas kbId="kb-1" />);
      await userEvent.click(await screen.findByTestId('wf-node-crag_grade'));
      await userEvent.selectOptions(screen.getByRole('combobox', { name: 'CRAG aktiviert' }), 'false');
      expect(await screen.findByText(/1 Einstellung geändert/)).toBeInTheDocument();
    });

    // THE batching guarantee: two edits on two DIFFERENT nodes must land in
    // one PUT with both keys, because ValidateConflicts judges the whole
    // batch — saving per-toggle would 400 the first half of a legitimate move
    // between chat_self_rag_enabled and chat_factuality_verifier_enabled and
    // trap the user one flag short of the state they want.
    it('saves every dirty key across the whole graph in a single call', async () => {
      vi.mocked(fetchWorkflow).mockResolvedValue(editableGraph());
      vi.mocked(saveKbSettings).mockResolvedValue(undefined);
      render(<WorkflowCanvas kbId="kb-1" />);

      await userEvent.click(await screen.findByTestId('wf-node-crag_grade'));
      await userEvent.selectOptions(screen.getByRole('combobox', { name: 'CRAG aktiviert' }), 'false');

      await userEvent.click(screen.getByTestId('wf-node-self_rag'));
      await userEvent.selectOptions(screen.getByRole('combobox', { name: 'Self-RAG aktiviert' }), 'true');

      expect(await screen.findByText(/2 Einstellungen geändert/)).toBeInTheDocument();

      await userEvent.click(screen.getByRole('button', { name: /speichern/i }));

      await waitFor(() => expect(saveKbSettings).toHaveBeenCalledTimes(1));
      expect(saveKbSettings).toHaveBeenCalledWith('kb-1', {
        crag_enabled: 'false',
        chat_self_rag_enabled: 'true',
      });
    });

    it('clears the draft and refetches the projection after a successful save', async () => {
      vi.mocked(fetchWorkflow).mockResolvedValue(editableGraph());
      vi.mocked(saveKbSettings).mockResolvedValue(undefined);
      render(<WorkflowCanvas kbId="kb-1" />);

      await userEvent.click(await screen.findByTestId('wf-node-crag_grade'));
      await userEvent.selectOptions(screen.getByRole('combobox', { name: 'CRAG aktiviert' }), 'false');
      await userEvent.click(screen.getByRole('button', { name: /speichern/i }));

      await waitFor(() => expect(fetchWorkflow).toHaveBeenCalledTimes(2));
      expect(fetchWorkflow).toHaveBeenNthCalledWith(2, 'kb-1', 'complex_reasoning');
      await waitFor(() => expect(screen.queryByText(/geändert/)).not.toBeInTheDocument());
    });

    // CRITICAL regression test: `selected` used to be a snapshot object taken
    // at click time. After a successful save cleared `draft`, the inspector's
    // `value = draft[k] ?? node.values[k]` fell through to that FROZEN
    // pre-save `node.values`, so a save that genuinely succeeded appeared to
    // revert in the UI the user was looking at. The inspector must now derive
    // from the freshly-refetched `graph`, not the click-time snapshot.
    it('shows the post-save value in the inspector, not the stale pre-save snapshot', async () => {
      const before = editableGraph();
      const after = editableGraph();
      after.nodes = after.nodes.map((n) => (
        n.id === 'crag_grade' ? { ...n, values: { crag_enabled: 'false' } } : n
      ));
      vi.mocked(fetchWorkflow).mockResolvedValueOnce(before).mockResolvedValueOnce(after);
      vi.mocked(saveKbSettings).mockResolvedValue(undefined);
      render(<WorkflowCanvas kbId="kb-1" />);

      await userEvent.click(await screen.findByTestId('wf-node-crag_grade'));
      await userEvent.selectOptions(screen.getByRole('combobox', { name: 'CRAG aktiviert' }), 'false');
      await userEvent.click(screen.getByRole('button', { name: /speichern/i }));

      await waitFor(() => expect(fetchWorkflow).toHaveBeenCalledTimes(2));
      // The inspector is still open on the same node (a successful save does
      // not close it) and must show the server's fresh value now that the
      // draft that used to mask the stale snapshot is gone.
      await waitFor(() => {
        const control = screen.getByRole('combobox', { name: 'CRAG aktiviert' }) as HTMLSelectElement;
        expect(control.value).toBe('false');
      });
    });

    // CRITICAL regression test, the Reset half: the origin badge and the
    // Reset button's own visibility both read `node.origins[k]` off the same
    // stale snapshot. A successful reset flips the server's origin from 'kb'
    // to 'global', but the frozen `selected` object kept reporting 'kb'
    // forever — the badge never returned to "global" and Reset never
    // disappeared, exactly the manual-verification step the brief calls out.
    it('shows the origin badge as "global" immediately after a successful reset, not the stale "kb" badge', async () => {
      const before = editableGraph();
      const after = editableGraph();
      after.nodes = after.nodes.map((n) => (
        n.id === 'crag_grade' ? { ...n, origins: { crag_enabled: 'global' } } : n
      ));
      vi.mocked(fetchWorkflow).mockResolvedValueOnce(before).mockResolvedValueOnce(after);
      vi.mocked(resetKbSetting).mockResolvedValue(undefined);
      render(<WorkflowCanvas kbId="kb-1" />);

      await userEvent.click(await screen.findByTestId('wf-node-crag_grade'));
      await userEvent.click(screen.getByRole('button', { name: /CRAG aktiviert zurücksetzen/i }));

      await waitFor(() => expect(fetchWorkflow).toHaveBeenCalledTimes(2));
      expect(await screen.findByText('global')).toBeInTheDocument();
      // Reset only offers itself for a 'kb'-origin key — it must disappear
      // once the origin is genuinely 'global'.
      expect(screen.queryByRole('button', { name: /CRAG aktiviert zurücksetzen/i })).not.toBeInTheDocument();
    });

    it('keeps the draft and surfaces the server message when save fails', async () => {
      vi.mocked(fetchWorkflow).mockResolvedValue(editableGraph());
      vi.mocked(saveKbSettings).mockRejectedValue(
        new Error('chat_self_rag_enabled and chat_factuality_verifier_enabled cannot both be enabled'),
      );
      render(<WorkflowCanvas kbId="kb-1" />);

      await userEvent.click(await screen.findByTestId('wf-node-crag_grade'));
      await userEvent.selectOptions(screen.getByRole('combobox', { name: 'CRAG aktiviert' }), 'false');
      await userEvent.click(screen.getByRole('button', { name: /speichern/i }));

      expect(await screen.findByText(/cannot both be enabled/)).toBeInTheDocument();
      // The draft is still there — the count row didn't disappear, and the
      // save button is still there to retry.
      expect(screen.getByText(/1 Einstellung geändert/)).toBeInTheDocument();
      // No refetch was attempted off the back of a failed save.
      expect(fetchWorkflow).toHaveBeenCalledTimes(1);
    });

    it('discard clears the draft without calling the API', async () => {
      vi.mocked(fetchWorkflow).mockResolvedValue(editableGraph());
      render(<WorkflowCanvas kbId="kb-1" />);

      await userEvent.click(await screen.findByTestId('wf-node-crag_grade'));
      await userEvent.selectOptions(screen.getByRole('combobox', { name: 'CRAG aktiviert' }), 'false');
      await screen.findByText(/geändert/);

      await userEvent.click(screen.getByRole('button', { name: /verwerfen/i }));

      expect(screen.queryByText(/geändert/)).not.toBeInTheDocument();
      expect(saveKbSettings).not.toHaveBeenCalled();
    });

    it('reset calls resetKbSetting and refetches the projection', async () => {
      vi.mocked(fetchWorkflow).mockResolvedValue(editableGraph());
      vi.mocked(resetKbSetting).mockResolvedValue(undefined);
      render(<WorkflowCanvas kbId="kb-1" />);

      await userEvent.click(await screen.findByTestId('wf-node-crag_grade'));
      await userEvent.click(screen.getByRole('button', { name: /CRAG aktiviert zurücksetzen/i }));

      await waitFor(() => expect(resetKbSetting).toHaveBeenCalledWith('kb-1', 'crag_enabled'));
      await waitFor(() => expect(fetchWorkflow).toHaveBeenCalledTimes(2));
    });

    // Phase 3's reset button shipped with no try/catch and silently swallowed
    // exactly this: the DELETE can legitimately 400 because clearing an
    // override may fall the key back to a conflicting global value.
    //
    // The rejection message deliberately names two specific keys, mirroring
    // what api.ts now actually surfaces from body.error (see api.test.ts),
    // rather than a generic "reset setting: 400" placeholder — a canvas that
    // only ever displayed a hardcoded fallback string would pass a test
    // asserting that same fallback even if it had stopped reading the real
    // message entirely.
    it('surfaces the server message when reset fails', async () => {
      vi.mocked(fetchWorkflow).mockResolvedValue(editableGraph());
      vi.mocked(resetKbSetting).mockRejectedValue(
        new Error('clearing crag_enabled would fall back to a global value that conflicts with chat_self_rag_enabled'),
      );
      render(<WorkflowCanvas kbId="kb-1" />);

      await userEvent.click(await screen.findByTestId('wf-node-crag_grade'));
      await userEvent.click(screen.getByRole('button', { name: /CRAG aktiviert zurücksetzen/i }));

      expect(await screen.findByText(/conflicts with chat_self_rag_enabled/)).toBeInTheDocument();
    });

    // The FieldString guard: siteconfig.Validate accepts "" for a
    // string-typed key, so an empty PUT would write a real kb-origin
    // override with an invisible value rather than clearing anything.
    it('never writes an empty value into the draft for a string-typed field, and points at Reset instead', async () => {
      vi.mocked(fetchWorkflow).mockResolvedValue(stringFieldGraph());
      render(<WorkflowCanvas kbId="kb-1" />);

      await userEvent.click(await screen.findByTestId('wf-node-tz_node'));
      const input = screen.getByRole('textbox', { name: 'Zeitzone' });
      await userEvent.clear(input);

      // Never became dirty — no save bar count for it.
      expect(screen.queryByText(/geändert/)).not.toBeInTheDocument();
      expect(await screen.findByText(/Leerer Wert/i)).toBeInTheDocument();
      expect(saveKbSettings).not.toHaveBeenCalled();
    });

    // Cheap fix: `value === ''` alone missed a whitespace-only value, which
    // renders just as invisibly once written but isn't caught by a bare
    // equality check.
    it('treats a whitespace-only value as empty too, refusing it the same way', async () => {
      vi.mocked(fetchWorkflow).mockResolvedValue(stringFieldGraph());
      render(<WorkflowCanvas kbId="kb-1" />);

      await userEvent.click(await screen.findByTestId('wf-node-tz_node'));
      const input = screen.getByRole('textbox', { name: 'Zeitzone' });
      fireEvent.change(input, { target: { value: '   ' } });

      expect(screen.queryByText(/geändert/)).not.toBeInTheDocument();
      expect(await screen.findByText(/Leerer Wert/i)).toBeInTheDocument();
      expect(saveKbSettings).not.toHaveBeenCalled();
    });

    it('marks the empty-value refusal hint as a polite live region', async () => {
      vi.mocked(fetchWorkflow).mockResolvedValue(stringFieldGraph());
      render(<WorkflowCanvas kbId="kb-1" />);

      await userEvent.click(await screen.findByTestId('wf-node-tz_node'));
      await userEvent.clear(screen.getByRole('textbox', { name: 'Zeitzone' }));

      const hint = await screen.findByText(/Leerer Wert/i);
      expect(hint).toHaveAttribute('aria-live', 'polite');
    });

    it('clears the empty-value refusal hint when the lane changes', async () => {
      vi.mocked(fetchWorkflow).mockResolvedValue(stringFieldGraph());
      render(<WorkflowCanvas kbId="kb-1" />);

      await userEvent.click(await screen.findByTestId('wf-node-tz_node'));
      await userEvent.clear(screen.getByRole('textbox', { name: 'Zeitzone' }));
      expect(await screen.findByText(/Leerer Wert/i)).toBeInTheDocument();

      vi.mocked(fetchWorkflow).mockResolvedValue(stringFieldGraph());
      await userEvent.click(screen.getByRole('button', { name: 'Nachschlagen' }));

      await waitFor(() => expect(screen.queryByText(/Leerer Wert/i)).not.toBeInTheDocument());
    });

    it('clears the empty-value refusal hint after a successful save', async () => {
      vi.mocked(fetchWorkflow).mockResolvedValue(mixedGraph());
      vi.mocked(saveKbSettings).mockResolvedValue(undefined);
      render(<WorkflowCanvas kbId="kb-1" />);

      await userEvent.click(await screen.findByTestId('wf-node-tz_node'));
      await userEvent.clear(screen.getByRole('textbox', { name: 'Zeitzone' }));
      expect(await screen.findByText(/Leerer Wert/i)).toBeInTheDocument();

      await userEvent.click(screen.getByTestId('wf-node-crag_grade'));
      await userEvent.selectOptions(screen.getByRole('combobox', { name: 'CRAG aktiviert' }), 'false');
      await userEvent.click(screen.getByRole('button', { name: /speichern/i }));

      await waitFor(() => expect(screen.queryByText(/Leerer Wert/i)).not.toBeInTheDocument());
    });

    // IMPORTANT 4 mitigation: KbSettingsPanel.tsx unmounts this component on
    // a Settings sub-tab switch, dropping the draft silently — out of this
    // file's scope to fix directly (see the beforeunload effect's comment).
    // This is the in-scope mitigation: a persistent warning line while the
    // draft is non-empty.
    it('warns that unsaved changes are lost on a tab switch, while the draft is dirty', async () => {
      vi.mocked(fetchWorkflow).mockResolvedValue(editableGraph());
      render(<WorkflowCanvas kbId="kb-1" />);

      await userEvent.click(await screen.findByTestId('wf-node-crag_grade'));
      expect(screen.queryByText(/Tab-Wechsel/)).not.toBeInTheDocument();

      await userEvent.selectOptions(screen.getByRole('combobox', { name: 'CRAG aktiviert' }), 'false');
      expect(await screen.findByText(/Tab-Wechsel/)).toBeInTheDocument();
    });

    // IMPORTANT 3 regression test: `refetchGraph` used to apply whatever
    // response arrived, unconditionally. A save's own refetch is issued for
    // the lane active AT SAVE TIME; if the user switches lanes before that
    // refetch resolves, the lane effect's own (already-guarded) fetch renders
    // the new lane correctly — but the save's slower, now-stale response
    // must not be allowed to land on top of it afterwards.
    it('does not let a slow save-refetch clobber a lane switched to while the save was in flight', async () => {
      const complex = editableGraph();
      const lookup = graph({
        lane: 'lookup',
        nodes: [
          { id: 'only', label: 'Nur-Lookup', group: 'X', help: '', keys: [], alwaysOn: true, llmCalls: 0, latencyMs: 0, activation: 'active', values: {}, origins: {}, editable: false },
        ],
        edges: [],
      });

      let resolveSaveRefetch: (g: WorkflowGraph) => void = () => {};
      const pendingSaveRefetch = new Promise<WorkflowGraph>((resolve) => { resolveSaveRefetch = resolve; });

      vi.mocked(fetchWorkflow)
        .mockResolvedValueOnce(complex) // initial load
        .mockReturnValueOnce(pendingSaveRefetch) // the save's own refetch — deliberately slow
        .mockResolvedValueOnce(lookup); // the lane switch's own fetch

      vi.mocked(saveKbSettings).mockResolvedValue(undefined);
      render(<WorkflowCanvas kbId="kb-1" />);

      await userEvent.click(await screen.findByTestId('wf-node-crag_grade'));
      await userEvent.selectOptions(screen.getByRole('combobox', { name: 'CRAG aktiviert' }), 'false');
      await userEvent.click(screen.getByRole('button', { name: /speichern/i }));

      // Switch lanes WHILE the save's refetch is still pending.
      await userEvent.click(screen.getByRole('button', { name: 'Nachschlagen' }));
      await screen.findByText('Nur-Lookup');

      // Now let the stale save-refetch resolve.
      resolveSaveRefetch(complex);
      await waitFor(() => expect(fetchWorkflow).toHaveBeenCalledTimes(3));

      // The lookup lane must still be showing — the stale response must not
      // have clobbered it with the previous lane's data.
      expect(screen.getByText('Nur-Lookup')).toBeInTheDocument();
      expect(screen.queryByText('CRAG-Bewertung')).not.toBeInTheDocument();
    });
  });
});
