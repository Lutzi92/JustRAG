import { describe, it, expect } from 'vitest';
import { layoutWorkflow, NODE_W, NODE_H, RANK_SEP, MIN_ZOOM } from './layout';
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

  it('excludes loop edges from ranking: siblings stay on the same rank', () => {
    // a fans out to b and c, so b and c share a rank.
    // The c -> b LOOP edge, if fed to dagre, would force c above b and split
    // the rank. Excluding it keeps them level — which is the property under test.
    const fan: WorkflowGraph = {
      lane: 'complex_reasoning',
      nodes: [node('a'), node('b'), node('c')],
      edges: [
        { from: 'a', to: 'b', label: '', loop: false, maxIterations: 0 },
        { from: 'a', to: 'c', label: '', loop: false, maxIterations: 0 },
        { from: 'c', to: 'b', label: 'zurück', loop: true, maxIterations: 1 },
      ],
      orchestrators: [],
      estLlmCalls: 0,
      estLatencyMs: 0,
    };

    const { nodes, edges } = layoutWorkflow(fan);
    const y = (id: string) => nodes.find((n) => n.id === id)!.position.y;

    expect(y('b')).toBe(y('c'));       // same rank — fails if the loop edge ranked
    expect(y('a')).toBeLessThan(y('b'));
    expect(edges).toHaveLength(3);      // and the loop edge is still drawn
  });
});

// --- Edge state (spec §6.1, graph.go:5-7 and :16-18) ---

/** Two active nodes joined by one loop edge — the "everything runs" baseline. */
const liveLoop: WorkflowGraph = {
  lane: 'complex_reasoning',
  nodes: [node('a'), node('b')],
  edges: [
    { from: 'a', to: 'b', label: '', loop: false, maxIterations: 0 },
    { from: 'b', to: 'a', label: 'erneut suchen', loop: true, maxIterations: 1 },
  ],
  orchestrators: [],
  estLlmCalls: 0,
  estLatencyMs: 0,
};

describe('layoutWorkflow edge state', () => {
  it('animates a loop edge — the bound is the whole point of drawing the graph', () => {
    const { edges } = layoutWorkflow(liveLoop);
    expect(edges.find((e) => e.source === 'b')!.animated).toBe(true);
    expect(edges.find((e) => e.source === 'a')!.animated).toBe(false);
  });

  it('stops animating loop edges under prefers-reduced-motion', () => {
    const { edges } = layoutWorkflow(liveLoop, { reducedMotion: true });
    expect(edges.find((e) => e.source === 'b')!.animated).toBe(false);
  });

  it('dims an edge whose source or target is inactive, and only those', () => {
    // The shared `graph` fixture has 'c' inactive: b -> c and the c -> a loop
    // both touch it, a -> b does not. This is the CRAG-off case — the canvas
    // used to draw a full-strength "erneut suchen (max. 1x)" loop through two
    // dimmed boxes.
    const { edges } = layoutWorkflow(graph);
    const byId = (source: string, target: string) =>
      edges.find((e) => e.source === source && e.target === target)!;

    expect(byId('a', 'b').className).not.toContain('wf-edge--dimmed');
    expect(byId('b', 'c').className).toContain('wf-edge--dimmed');
    expect(byId('c', 'a').className).toContain('wf-edge--dimmed');
  });

  it('does not animate a dimmed loop edge — a dead loop must not draw the eye', () => {
    const { edges } = layoutWorkflow(graph);
    expect(edges.find((e) => e.source === 'c' && e.target === 'a')!.animated).toBe(false);
  });
});

// --- Viewport fit against the REAL graph size, not a 3-node fixture ---

describe('layoutWorkflow at production scale', () => {
  // The live projection is 23 nodes (go-backend/internal/pipeline/nodes.go:21-43)
  // in a mostly linear pipeline. A 23-node chain is the worst case and within a
  // few ranks of the real topology (~20 ranks, since answer fans out to
  // factuality + citation_validate, which then rejoin).
  const CHAIN = 23;
  const chain: WorkflowGraph = {
    lane: 'complex_reasoning',
    nodes: Array.from({ length: CHAIN }, (_, i) => node(`n${i}`)),
    edges: Array.from({ length: CHAIN - 1 }, (_, i) => ({
      from: `n${i}`, to: `n${i + 1}`, label: '', loop: false, maxIterations: 0,
    })),
    orchestrators: [],
    estLlmCalls: 0,
    estLatencyMs: 0,
  };

  const layoutHeight = () => {
    const { nodes } = layoutWorkflow(chain);
    const ys = nodes.map((n) => n.position.y);
    return Math.max(...ys) + NODE_H - Math.min(...ys);
  };

  it('is about 2900px tall — far taller than any canvas surface', () => {
    // CHAIN ranks of NODE_H, separated by CHAIN-1 gaps of RANK_SEP.
    expect(layoutHeight()).toBe(CHAIN * NODE_H + (CHAIN - 1) * RANK_SEP);
    expect(layoutHeight()).toBeGreaterThan(2000);
  });

  it('needs a fit zoom below React Flow\'s default minZoom of 0.5, and above ours', () => {
    // A generous surface (a tall desktop viewport, toolbar subtracted). If the
    // fit zoom for THIS is under 0.5, React Flow's default clamp engages and
    // the canvas opens mid-pipeline at half scale, which is the bug.
    const SURFACE_H = 760;
    const fitZoom = SURFACE_H / layoutHeight();

    expect(fitZoom).toBeLessThan(0.5);          // the default would clamp
    expect(fitZoom).toBeGreaterThan(MIN_ZOOM);  // ours does not
  });
});
