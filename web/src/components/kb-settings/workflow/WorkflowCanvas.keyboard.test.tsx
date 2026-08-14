import { describe, it, expect, vi, beforeAll } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import { ReactFlow, ReactFlowProvider } from '@xyflow/react';
import { WorkflowCanvas } from './WorkflowCanvas';
import WorkflowNode from './WorkflowNode';
import type { WorkflowGraph, WorkflowNodeData } from '../../../types';

// This file deliberately does NOT mock '@xyflow/react' (unlike
// WorkflowCanvas.test.tsx): it exists to empirically settle whether real
// React Flow fires onNodeClick (or any usable event) when a focused node is
// activated with Enter/Space, which only a real <ReactFlow> render can
// answer. jsdom has no ResizeObserver, which React Flow's internal
// node-measuring hook requires (same setup as WorkflowNode.focus.test.tsx).
beforeAll(() => {
  class FakeResizeObserver {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
  globalThis.ResizeObserver = FakeResizeObserver as unknown as typeof ResizeObserver;
});

vi.mock('./api', () => ({ fetchWorkflow: vi.fn() }));
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
