import dagre from 'dagre';
import type { Node, Edge } from '@xyflow/react';
import type { WorkflowGraph, WorkflowNodeData } from '../../../types';

export const NODE_W = 236;
export const NODE_H = 84;

/**
 * MIN_ZOOM is the floor handed to <ReactFlow minZoom>, and it is a correctness
 * knob, not a taste one.
 *
 * The real topology is 23 nodes in ~20 dagre ranks, i.e. roughly
 * 20 * NODE_H + 19 * RANK_SEP ≈ 2500px tall. React Flow's DEFAULT minZoom is
 * 0.5, and `getViewportForBounds` clamps the fit zoom to it and then centres —
 * so on a ~570px-tall surface the initial `fitView()` could not go below 0.5,
 * and the canvas opened showing the middle third of the pipeline at half scale:
 * "Klassifizierung" off the top, the whole "Prüfung" group off the bottom, and
 * nothing on screen saying the graph continued.
 *
 * 0.1 is below the fit zoom the real graph needs on any plausible surface
 * height, so the clamp never engages. layout.test.ts pins this against a
 * realistic 23-node chain, so a future NODE_H bump can't silently reintroduce
 * the clipping.
 */
export const MIN_ZOOM = 0.1;

/** Dagre rank separation — exported so the layout-height test isn't a magic number. */
export const RANK_SEP = 44;

export type WorkflowRFNode = Node<{ data: WorkflowNodeData }, 'workflow'>;

export interface LayoutOptions {
  /**
   * When true, loop edges are drawn statically instead of animated. The dashed
   * marching-ants animation is the only motion on this canvas and it never
   * stops, which is precisely what `prefers-reduced-motion` exists to suppress.
   */
  reducedMotion?: boolean;
}

/**
 * layoutWorkflow turns a projection into positioned React Flow nodes/edges.
 *
 * Positions are DERIVED on every render, never persisted — a topology change
 * must not be able to leave a KB with a stale saved layout.
 *
 * Loop edges (CRAG re-search, the refine loop, multi-hop orchestrators) are
 * deliberately excluded from the dagre graph: as ranking constraints they would
 * turn the pipeline into a cycle and wreck the top-to-bottom reading order.
 * They are still returned so the canvas can draw them as back-edges — animated
 * and labelled with their bound, which graph.go:5-7 calls "the whole point of
 * drawing the graph".
 *
 * Edge state comes from the endpoints. graph.go:16-18 says the projection
 * "neither prunes nor annotates" edges and that "deciding how to render an edge
 * whose endpoints are inactive is the client's job, using the endpoints'
 * Activation from the same payload". Without that, a KB with CRAG switched off
 * still got a full-strength `crag_grade → crag_rewrite → retrieve` loop drawn
 * at full opacity, labelled "erneut suchen (max. 1×)", through two dimmed boxes.
 */
export function layoutWorkflow(
  graph: WorkflowGraph,
  { reducedMotion = false }: LayoutOptions = {},
): { nodes: WorkflowRFNode[]; edges: Edge[] } {
  const g = new dagre.graphlib.Graph();
  g.setGraph({ rankdir: 'TB', nodesep: 28, ranksep: RANK_SEP });
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

  const activationById = new Map(graph.nodes.map((n) => [n.id, n.activation]));
  // An edge is only as live as its weaker endpoint. An unknown endpoint (an
  // edge naming a node the projection didn't return) is treated as inactive
  // rather than active — an over-dimmed edge understates, a full-strength one
  // asserts a flow that isn't there.
  const isInactive = (id: string) => (activationById.get(id) ?? 'inactive') === 'inactive';

  const edges: Edge[] = graph.edges.map((e) => {
    const dimmed = isInactive(e.from) || isInactive(e.to);
    return {
      id: `${e.from}->${e.to}${e.loop ? ':loop' : ''}`,
      source: e.from,
      target: e.to,
      label: e.loop ? `${e.label} (max. ${e.maxIterations}×)` : e.label,
      animated: e.loop && !reducedMotion && !dimmed,
      // The class lands on React Flow's `.react-flow__edge` group, so the label
      // dims with the path instead of floating at full strength above it.
      className: dimmed ? 'wf-edge wf-edge--dimmed' : 'wf-edge',
      data: { loop: e.loop, dimmed },
    };
  });

  return { nodes, edges };
}
