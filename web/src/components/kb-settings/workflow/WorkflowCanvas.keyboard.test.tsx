import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ReactFlow, ReactFlowProvider } from '@xyflow/react';
import { WorkflowCanvas } from './WorkflowCanvas';
import WorkflowNode from './WorkflowNode';
import type { WorkflowGraph, WorkflowNodeData } from '../../../types';

// This file deliberately does NOT mock '@xyflow/react' (unlike
// WorkflowCanvas.test.tsx): it exists to empirically settle whether real
// React Flow fires onNodeClick (or any usable event) when a focused node is
// activated with Enter/Space, which only a real <ReactFlow> render can
// answer. React Flow's internal node-measuring hook needs ResizeObserver,
// which jsdom lacks — stubbed once globally in src/test/setup.ts.

// WorkflowCanvas reads useReducedMotion, which calls window.matchMedia —
// not available in jsdom by default (same treatment as Login.test.tsx).
vi.mock('../../../hooks/useReducedMotion', () => ({
  useReducedMotion: () => false,
  getMotionProps: () => ({}),
}));

// fieldFor is real (not a vi.fn()): NodeInspector, rendered inside this
// canvas, imports it from the same module and needs a genuine lookup, not a
// second mock to keep in sync with the fixture's `fields` map.
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>();
  return {
    ...actual,
    fetchWorkflow: vi.fn(),
    // PresetPicker (Task 5) fetches this on mount unconditionally; this
    // suite is about keyboard delegation, not the picker itself.
    fetchPresets: vi.fn().mockResolvedValue([]),
  };
});
import { fetchWorkflow } from './api';

const graph: WorkflowGraph = {
  lane: 'complex_reasoning',
  nodes: [
    { id: 'crag_grade', label: 'CRAG-Bewertung', group: 'Korrektur', help: 'Bewertet Textstellen.', keys: ['crag_enabled'], alwaysOn: false, llmCalls: 1, latencyMs: 600, activation: 'conditional', reason: 'orchestrator_bypass', condition: 'Läuft hier nicht.', values: { crag_enabled: 'true' }, origins: { crag_enabled: 'kb' }, editable: true },
  ],
  edges: [],
  orchestrators: [],
  estLlmCalls: 1,
  estLatencyMs: 600,
  fields: {},
  presetBase: '',
  presetBaseKnown: true,
  deviations: [],
};

function makeNode(): WorkflowNodeData {
  return {
    id: 'crag_grade', label: 'CRAG-Bewertung', group: 'Korrektur',
    help: 'Bewertet die gefundenen Textstellen.', keys: ['crag_enabled'],
    alwaysOn: false, llmCalls: 1, latencyMs: 600, activation: 'active',
    values: {}, origins: {}, editable: true,
  };
}

describe('React Flow keyboard behaviour (bare, no WorkflowCanvas)', () => {
  it('does NOT call onNodeClick when Enter is pressed on a focused node', () => {
    // Empirically settles the question the brief asks about: does React Flow
    // itself activate a focused node's onClick handler on Enter/Space? Read
    // node_modules/@xyflow/react/dist/esm/index.mjs's NodeWrapper first
    // (elementSelectionKeys = ['Enter', ' ', 'Escape'] from @xyflow/system;
    // the node wrapper's onKeyDown calls handleNodeClick, which only touches
    // the internal selection store) — this test proves it against the real,
    // unmocked library rather than trusting that reading.
    const onNodeClick = vi.fn();
    const n = makeNode();
    const { container } = render(
      <ReactFlowProvider>
        <div style={{ width: 400, height: 400 }}>
          <ReactFlow
            nodes={[{ id: n.id, type: 'workflow', position: { x: 0, y: 0 }, data: { data: n } }]}
            edges={[]}
            nodeTypes={{ workflow: WorkflowNode }}
            onNodeClick={onNodeClick}
          />
        </div>
      </ReactFlowProvider>
    );

    const rfNode = container.querySelector('.react-flow__node') as HTMLElement;
    rfNode.focus();
    fireEvent.keyDown(rfNode, { key: 'Enter' });

    expect(onNodeClick).not.toHaveBeenCalled();
  });
});

describe('WorkflowCanvas keyboard activation (real, unmocked React Flow)', () => {
  it('opens the inspector on Enter for a focused node via the canvas delegated handler', async () => {
    vi.mocked(fetchWorkflow).mockResolvedValue(graph);
    render(<WorkflowCanvas kbId="kb-1" />);
    const node = await screen.findByTestId('wf-node-crag_grade');
    const rfNode = node.closest('.react-flow__node') as HTMLElement;
    rfNode.focus();
    expect(document.activeElement).toBe(rfNode);

    fireEvent.keyDown(rfNode, { key: 'Enter' });

    expect(await screen.findByText('Läuft hier nicht.')).toBeInTheDocument();
  });

  it('opens the inspector on Space for a focused node too', async () => {
    vi.mocked(fetchWorkflow).mockResolvedValue(graph);
    render(<WorkflowCanvas kbId="kb-1" />);
    const node = await screen.findByTestId('wf-node-crag_grade');
    const rfNode = node.closest('.react-flow__node') as HTMLElement;
    rfNode.focus();

    fireEvent.keyDown(rfNode, { key: ' ' });

    expect(await screen.findByText('Läuft hier nicht.')).toBeInTheDocument();
  });
});

describe('WorkflowCanvas inspector focus journey (real, unmocked React Flow)', () => {
  // Opening the inspector used to be completely invisible to a keyboard or
  // screen-reader user: Enter opened the panel but focus stayed on the node,
  // there was no live region and no announcement, reaching the panel meant
  // tabbing through every remaining node plus React Flow's Controls, closing
  // dropped focus to <body>, and there was no Escape.
  const openViaKeyboard = async () => {
    vi.mocked(fetchWorkflow).mockResolvedValue(graph);
    render(<WorkflowCanvas kbId="kb-1" />);
    const node = await screen.findByTestId('wf-node-crag_grade');
    const rfNode = node.closest('.react-flow__node') as HTMLElement;
    rfNode.focus();
    fireEvent.keyDown(rfNode, { key: 'Enter' });
    const panel = await screen.findByLabelText('Details zu CRAG-Bewertung');
    return { rfNode, panel };
  };

  it('moves focus into the panel when it opens', async () => {
    const { panel } = await openViaKeyboard();
    await waitFor(() => expect(document.activeElement).toBe(panel));
  });

  it('closes on Escape and hands focus back to the node it was opened from', async () => {
    const { rfNode, panel } = await openViaKeyboard();
    await waitFor(() => expect(document.activeElement).toBe(panel));

    fireEvent.keyDown(panel, { key: 'Escape' });

    await waitFor(() =>
      expect(screen.queryByLabelText('Details zu CRAG-Bewertung')).not.toBeInTheDocument());
    expect(document.activeElement).toBe(rfNode);
  });

  it('hands focus back to the node when closed with the close button', async () => {
    const { rfNode } = await openViaKeyboard();
    await userEvent.click(screen.getByRole('button', { name: /schließen/i }));

    await waitFor(() =>
      expect(screen.queryByLabelText('Details zu CRAG-Bewertung')).not.toBeInTheDocument());
    expect(document.activeElement).toBe(rfNode);
  });

  it('keeps the panel out of the tab order — focus is moved to it, not tabbed to', () => {
    // tabIndex=-1: programmatically focusable, never a tab stop of its own.
    // A tabbable <aside> would add a stop to a canvas that already has one per
    // node plus the Controls buttons.
    return openViaKeyboard().then(({ panel }) => {
      expect(panel.getAttribute('tabindex')).toBe('-1');
    });
  });
});

describe('WorkflowCanvas click delegation (real, unmocked React Flow)', () => {
  // findWfNodeId's downward querySelector exists for the keyboard path, where
  // the event target is `.react-flow__node` (an ANCESTOR of `.wf-node`). But
  // the same function is shared with the click delegate, and a click on empty
  // canvas lands on `.react-flow__pane` — also an ancestor of every node — so
  // an unanchored downward walk from the pane matches the FIRST `.wf-node` in
  // DOM order and opens the inspector on the wrong stage. This test must be
  // run against the fix in WorkflowCanvas.tsx; it is written first specifically
  // to prove the bug exists before the fix lands.
  it('leaves the inspector closed when clicking empty canvas (the pane)', async () => {
    vi.mocked(fetchWorkflow).mockResolvedValue(graph);
    render(<WorkflowCanvas kbId="kb-1" />);
    await screen.findByTestId('wf-node-crag_grade');

    const pane = document.querySelector('.react-flow__pane') as HTMLElement;
    expect(pane).toBeTruthy();
    fireEvent.click(pane);

    expect(screen.queryByText('Läuft hier nicht.')).not.toBeInTheDocument();
  });

  it('leaves the inspector closed when clicking a React Flow Controls button', async () => {
    vi.mocked(fetchWorkflow).mockResolvedValue(graph);
    render(<WorkflowCanvas kbId="kb-1" />);
    await screen.findByTestId('wf-node-crag_grade');

    const controlsButton = document.querySelector('.react-flow__controls-button') as HTMLElement;
    expect(controlsButton).toBeTruthy();
    fireEvent.click(controlsButton);

    expect(screen.queryByText('Läuft hier nicht.')).not.toBeInTheDocument();
  });

  it('still opens the inspector for the correct node on a real click', async () => {
    vi.mocked(fetchWorkflow).mockResolvedValue(graph);
    render(<WorkflowCanvas kbId="kb-1" />);
    const node = await screen.findByTestId('wf-node-crag_grade');

    fireEvent.click(node);

    expect(await screen.findByText('Läuft hier nicht.')).toBeInTheDocument();
  });
});
