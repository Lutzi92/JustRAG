import dagre from 'dagre';
import type { Node, Edge } from '@xyflow/react';
import type { WorkflowGraph, WorkflowNodeData } from '../../../types';

export const NODE_W = 236;
export const NODE_H = 84;

export type WorkflowRFNode = Node<{ data: WorkflowNodeData }, 'workflow'>;

/**
 * layoutWorkflow turns a projection into positioned React Flow nodes/edges.
 *
 * Positions are DERIVED on every render, never persisted — a topology change
 * must not be able to leave a KB with a stale saved layout.
 *
 * Loop edges (CRAG re-search, the refine loop, multi-hop orchestrators) are
 * deliberately excluded from the dagre graph: as ranking constraints they would
 * turn the pipeline into a cycle and wreck the top-to-bottom reading order.
 * They are still returned so the canvas can draw them as back-edges.
 */
export function layoutWorkflow(graph: WorkflowGraph): { nodes: WorkflowRFNode[]; edges: Edge[] } {
  const g = new dagre.graphlib.Graph();
  g.setGraph({ rankdir: 'TB', nodesep: 28, ranksep: 44 });
  g.setDefaultEdgeLabel(() => ({}));

  for (const n of graph.nodes) g.setNode(n.id, { width: NODE_W, height: NODE_H });
  for (const e of graph.edges) {
    if (e.loop) continue;
    g.setEdge(e.from, e.to);
  }

  dagre.layout(g);

  const nodes: WorkflowRFNode[] = graph.nodes.map((n) => {
    const pos = g.node(n.id);
    return {
      id: n.id,
      type: 'workflow',
      position: { x: (pos?.x ?? 0) - NODE_W / 2, y: (pos?.y ?? 0) - NODE_H / 2 },
      data: { data: n },
    };
  });

  const edges: Edge[] = graph.edges.map((e) => ({
    id: `${e.from}->${e.to}${e.loop ? ':loop' : ''}`,
    source: e.from,
    target: e.to,
    label: e.loop ? `${e.label} (max. ${e.maxIterations}×)` : e.label,
    animated: false,
    data: { loop: e.loop },
  }));

  return { nodes, edges };
}
