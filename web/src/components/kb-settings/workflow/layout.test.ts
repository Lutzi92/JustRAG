import { describe, it, expect } from 'vitest';
import { layoutWorkflow, NODE_W, NODE_H } from './layout';
import type { WorkflowGraph, WorkflowNodeData } from '../../../types';

function node(id: string, over: Partial<WorkflowNodeData> = {}): WorkflowNodeData {
  return {
    id, label: id, group: 'Test', help: '', keys: [], alwaysOn: false,
    llmCalls: 0, latencyMs: 0, activation: 'active', values: {}, origins: {},
    editable: false, ...over,
  };
}

const graph: WorkflowGraph = {
  lane: 'complex_reasoning',
  nodes: [node('a'), node('b'), node('c', { activation: 'inactive', reason: 'flag_off' })],
  edges: [
    { from: 'a', to: 'b', label: '', loop: false, maxIterations: 0 },
    { from: 'b', to: 'c', label: 'ok', loop: false, maxIterations: 0 },
    { from: 'c', to: 'a', label: 'erneut', loop: true, maxIterations: 2 },
  ],
  orchestrators: [],
  estLlmCalls: 0,
  estLatencyMs: 0,
};

describe('layoutWorkflow', () => {
  it('returns one RF node per projection node, carrying the projection data', () => {
    const { nodes } = layoutWorkflow(graph);
    expect(nodes).toHaveLength(3);
    expect(nodes.map((n) => n.id).sort()).toEqual(['a', 'b', 'c']);
    expect(nodes.find((n) => n.id === 'c')!.data.data.activation).toBe('inactive');
  });

  it('assigns finite, non-overlapping positions', () => {
    const { nodes } = layoutWorkflow(graph);
    for (const n of nodes) {
      expect(Number.isFinite(n.position.x)).toBe(true);
      expect(Number.isFinite(n.position.y)).toBe(true);
    }
    const ys = nodes.map((n) => n.position.y);
    expect(new Set(ys).size).toBeGreaterThan(1);
  });

  it('lays the pipeline out top-to-bottom: a above b above c', () => {
    const { nodes } = layoutWorkflow(graph);
    const y = (id: string) => nodes.find((n) => n.id === id)!.position.y;
    expect(y('a')).toBeLessThan(y('b'));
    expect(y('b')).toBeLessThan(y('c'));
  });

  it('keeps loop edges in the output but out of the ranking', () => {
    const { edges } = layoutWorkflow(graph);
    expect(edges).toHaveLength(3);
    const loop = edges.find((e) => e.source === 'c' && e.target === 'a');
    expect(loop).toBeDefined();
    // If the loop edge had been fed to dagre, it would have forced 'a' below 'c'.
    const { nodes } = layoutWorkflow(graph);
    const y = (id: string) => nodes.find((n) => n.id === id)!.position.y;
    expect(y('a')).toBeLessThan(y('c'));
  });

  it('labels a loop edge with its iteration bound', () => {
    const { edges } = layoutWorkflow(graph);
    const loop = edges.find((e) => e.source === 'c')!;
    expect(String(loop.label)).toContain('2');
  });

  it('uses stable node dimensions', () => {
    expect(NODE_W).toBeGreaterThan(0);
    expect(NODE_H).toBeGreaterThan(0);
  });
});
