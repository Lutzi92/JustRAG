import { describe, it, expect } from 'vitest';
import { render } from '@testing-library/react';
import { ReactFlow, ReactFlowProvider } from '@xyflow/react';
import WorkflowNode from './WorkflowNode';
import type { WorkflowNodeData } from '../../../types';

// This file deliberately does NOT mock '@xyflow/react' (unlike WorkflowNode.test.tsx):
// it exists to pin which DOM element owns the keyboard tab stop for a workflow
// node, which only a real <ReactFlow> render can answer. React Flow's internal
// node-measuring hook needs ResizeObserver, which jsdom lacks — stubbed once
// globally in src/test/setup.ts.

function makeNode(): WorkflowNodeData {
  return {
    id: 'crag_grade', label: 'CRAG-Bewertung', group: 'Korrektur',
    help: 'Bewertet die gefundenen Textstellen.', keys: ['crag_enabled'],
    alwaysOn: false, llmCalls: 1, latencyMs: 600, activation: 'active',
    values: {}, origins: {}, editable: true,
  };
}

describe('WorkflowNode keyboard focus ownership', () => {
  it('lets React Flow put the tab stop on its own node wrapper, not on .wf-node', () => {
    const n = makeNode();
    const { container } = render(
      <ReactFlowProvider>
        <div style={{ width: 400, height: 400 }}>
          <ReactFlow
            nodes={[{ id: n.id, type: 'workflow', position: { x: 0, y: 0 }, data: { data: n } }]}
            edges={[]}
            nodeTypes={{ workflow: WorkflowNode }}
          />
        </div>
      </ReactFlowProvider>
    );

    const rfNode = container.querySelector('.react-flow__node');
    const wfNode = container.querySelector('.wf-node');

    // React Flow's wrapper is the real, single tab stop for the node
    // (nodesFocusable defaults to true): tabIndex=0 + role="group".
    expect(rfNode).toBeTruthy();
    expect(rfNode?.getAttribute('tabindex')).toBe('0');
    expect(rfNode?.getAttribute('role')).toBe('group');

    // Our inner element must NOT declare its own tabIndex/role — doing so
    // would give keyboard users a second tab stop per node.
    expect(wfNode?.hasAttribute('tabindex')).toBe(false);
    expect(wfNode?.hasAttribute('role')).toBe(false);

    // And focus actually lands on the wrapper, confirming it (not .wf-node)
    // is what :focus-visible needs to be scoped to in the CSS.
    (rfNode as HTMLElement).focus();
    expect(document.activeElement).toBe(rfNode);
  });
});
