import { describe, it, expect } from 'vitest';
import { renderHook, act } from '@testing-library/react';
import type { Node, Edge } from '@xyflow/react';
import { useGraphInteractions } from './useGraphInteractions';

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
const rfEdges: Edge[] = graph.edges.map((e, i) => ({ id: `e${i}`, source: String(e.source), target: String(e.target) }));

describe('useGraphInteractions', () => {
  it('lists distinct entity types and starts with all active', () => {
    const { result } = renderHook(() => useGraphInteractions(graph));
    expect(result.current.allTypes.sort()).toEqual(['org', 'person']);
    expect([...result.current.activeTypes].sort()).toEqual(['org', 'person']);
  });

  it('hides nodes whose type is filtered out, and their incident edges', () => {
    const { result } = renderHook(() => useGraphInteractions(graph));
    act(() => result.current.toggleType('person'));
    const { nodes, edges } = result.current.decorate(rfNodes, rfEdges);
    expect(nodes.find(n => n.id === '2')?.hidden).toBe(true);   // person hidden
    expect(nodes.find(n => n.id === '1')?.hidden).toBeFalsy();  // org visible
    expect(edges.find(e => e.target === '2')?.hidden).toBe(true); // edge to hidden node
  });

  it('dims non-neighbors on hover', () => {
    const { result } = renderHook(() => useGraphInteractions(graph));
    act(() => result.current.setHovered('1')); // A: neighbors 2,3
    const { nodes } = result.current.decorate(rfNodes, rfEdges);
    expect(nodes.find(n => n.id === '1')?.style?.opacity).toBe(1);
    expect(nodes.find(n => n.id === '2')?.style?.opacity).toBe(1);
  });

  it('edge between two neighbors stays bright; edge to non-neighbor dims', () => {
    // Triangle: 1-2, 1-3, 2-3 plus isolated edge 2-4.
    // Hover 1: nodes 1,2,3 are lit; node 4 is not.
    // Edge 2-3 (both endpoints lit) → opacity 1.
    // Edge 2-4 (4 is not lit)       → opacity 0.15.
    const gTri = {
      nodes: [
        { id: 1, name: 'A', type: 'org', degree: 3 },
        { id: 2, name: 'B', type: 'org', degree: 2 },
        { id: 3, name: 'C', type: 'org', degree: 2 },
        { id: 4, name: 'D', type: 'org', degree: 1 },
      ],
      edges: [
        { source: 1, target: 2, rel: 'r1' },
        { source: 1, target: 3, rel: 'r2' },
        { source: 2, target: 3, rel: 'r3' },
        { source: 2, target: 4, rel: 'r4' },
      ],
    };
    const triNodes: Node[] = gTri.nodes.map(n => ({ id: String(n.id), position: { x: 0, y: 0 }, data: {} }));
    const triEdges: Edge[] = [
      { id: 'e1', source: '1', target: '2' },
      { id: 'e2', source: '1', target: '3' },
      { id: 'e3', source: '2', target: '3' },
      { id: 'e4', source: '2', target: '4' },
    ];
    const { result } = renderHook(() => useGraphInteractions(gTri));
    act(() => result.current.setHovered('1'));
    const { edges } = result.current.decorate(triNodes, triEdges);
    // Edge between two lit neighbors must be bright
    expect(edges.find(e => e.id === 'e3')?.style?.opacity).toBe(1);
    // Edge to non-neighbor (node 4 is not lit) must dim
    expect(edges.find(e => e.id === 'e4')?.style?.opacity).toBe(0.15);
  });

  it('dims a node that is not the hovered node nor a neighbor', () => {
    const g2 = {
      nodes: [...graph.nodes, { id: 4, name: 'D', type: 'org', degree: 0 }],
      edges: graph.edges,
    };
    const nodes4: Node[] = g2.nodes.map(n => ({ id: String(n.id), position: { x: 0, y: 0 }, data: {} }));
    const { result } = renderHook(() => useGraphInteractions(g2));
    act(() => result.current.setHovered('1'));
    const { nodes } = result.current.decorate(nodes4, rfEdges);
    expect(nodes.find(n => n.id === '4')?.style?.opacity).toBe(0.15);
  });
});
