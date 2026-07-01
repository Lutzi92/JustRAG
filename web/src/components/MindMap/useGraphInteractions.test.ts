import { describe, it, expect } from 'vitest';
import { renderHook, act } from '@testing-library/react';
import type { Node, Edge } from '@xyflow/react';
import { useGraphInteractions, filterByType, litNodeSet, buildDimCss } from './useGraphInteractions';

const graph = {
  nodes: [
    { id: 1, name: 'A', type: 'org', degree: 2 },
    { id: 2, name: 'B', type: 'person', degree: 1 },
    { id: 3, name: 'C', type: 'org', degree: 1 },
  ],
  edges: [
    { source: 1, target: 2, rel: 'r1' },
    { source: 1, target: 3, rel: 'r2' },
  ],
};
const rfNodes: Node[] = graph.nodes.map(n => ({ id: String(n.id), position: { x: 0, y: 0 }, data: {} }));
const rfEdges: Edge[] = graph.edges.map((e, i) => ({ id: `e-${e.source}-${e.target}-${i}`, source: String(e.source), target: String(e.target) }));

const typeById = new Map(graph.nodes.map(n => [String(n.id), n.type] as const));
const adjacency = new Map<string, Set<string>>([
  ['1', new Set(['2', '3'])],
  ['2', new Set(['1'])],
  ['3', new Set(['1'])],
]);

describe('filterByType', () => {
  it('removes nodes of inactive types and any incident edges entirely', () => {
    const active = new Set(['org']); // person filtered out
    const { nodes, edges } = filterByType(rfNodes, rfEdges, active, typeById);
    expect(nodes.map(n => n.id).sort()).toEqual(['1', '3']); // person node 2 removed
    expect(nodes.find(n => n.id === '2')).toBeUndefined();
    // Edge 1->2 touches the removed node, so it is gone; edge 1->3 stays.
    expect(edges.map(e => e.id)).toEqual(['e-1-3-1']);
  });

  it('keeps everything when all types are active', () => {
    const active = new Set(['org', 'person']);
    const { nodes, edges } = filterByType(rfNodes, rfEdges, active, typeById);
    expect(nodes).toHaveLength(3);
    expect(edges).toHaveLength(2);
  });
});

describe('litNodeSet', () => {
  it('returns null (no dimming) when nothing is hovered', () => {
    expect(litNodeSet(null, adjacency)).toBeNull();
  });

  it('lights the hovered node and its direct neighbors', () => {
    const lit = litNodeSet('1', adjacency); // A: neighbors 2,3
    expect(lit).not.toBeNull();
    expect([...lit!].sort()).toEqual(['1', '2', '3']);
  });

  it('does not light a non-neighbor node', () => {
    const lit = litNodeSet('2', adjacency); // B: neighbor 1 only
    expect(lit!.has('3')).toBe(false);
  });
});

describe('buildDimCss', () => {
  it('is empty when nothing is lit', () => {
    expect(buildDimCss(null, rfEdges)).toBe('');
  });

  it('dims all nodes/edges and re-lights the lit node ids', () => {
    const lit = new Set(['1', '2', '3']);
    const css = buildDimCss(lit, rfEdges);
    expect(css).toContain('.react-flow__node[data-id="1"]');
    expect(css).toContain('.react-flow__node[data-id="2"]');
    expect(css).toMatch(/opacity:\s*0?\.15/); // base dim rule present
  });

  it('re-lights an edge only when both endpoints are lit', () => {
    const lit = new Set(['1', '2']); // node 3 not lit
    const css = buildDimCss(lit, rfEdges);
    expect(css).toContain('.react-flow__edge[data-id="e-1-2-0"]'); // both lit
    expect(css).not.toContain('e-1-3-1'); // endpoint 3 not lit
  });
});

describe('useGraphInteractions', () => {
  it('lists distinct entity types and starts with all active', () => {
    const { result } = renderHook(() => useGraphInteractions(graph));
    expect(result.current.allTypes.sort()).toEqual(['org', 'person']);
    expect([...result.current.activeTypes].sort()).toEqual(['org', 'person']);
  });

  it('applyTypeFilter drops nodes of a toggled-off type', () => {
    const { result } = renderHook(() => useGraphInteractions(graph));
    act(() => result.current.toggleType('person'));
    const { nodes } = result.current.applyTypeFilter(rfNodes, rfEdges);
    expect(nodes.find(n => n.id === '2')).toBeUndefined(); // person removed
    expect(nodes.find(n => n.id === '1')).toBeTruthy();     // org kept
  });

  it('exposes litNodes as the hovered node plus neighbors', () => {
    const { result } = renderHook(() => useGraphInteractions(graph));
    act(() => result.current.setHovered('1'));
    expect([...result.current.litNodes!].sort()).toEqual(['1', '2', '3']);
  });

  it('litNodes is null when nothing is hovered', () => {
    const { result } = renderHook(() => useGraphInteractions(graph));
    expect(result.current.litNodes).toBeNull();
  });
});
