import { useCallback, useEffect, useMemo, useState } from 'react';
import { ReactFlow, Background, Controls, useNodesState, useEdgesState } from '@xyflow/react';
import '@xyflow/react/dist/style.css';
import type { Edge } from '@xyflow/react';
import type { WorkflowGraph, WorkflowLane, WorkflowNodeData } from '../../../types';
import { fetchWorkflow } from './api';
import { layoutWorkflow, type WorkflowRFNode } from './layout';
import WorkflowNode from './WorkflowNode';
import { NodeInspector } from './NodeInspector';
import './WorkflowCanvas.css';

const LANES: { id: WorkflowLane; label: string }[] = [
  { id: 'lookup', label: 'Nachschlagen' },
  { id: 'enumeration', label: 'Aufzählung' },
  { id: 'complex_reasoning', label: 'Komplexe Frage' },
];

const nodeTypes = { workflow: WorkflowNode };

const WF_NODE_TESTID_PREFIX = 'wf-node-';

/**
 * findWfNodeId locates the workflow node id backing a DOM event, regardless
 * of whether the event originated as a mouse click (target is `.wf-node`
 * itself, or a descendant of it — e.g. its label span) or a keyboard
 * activation on the focused React Flow node wrapper (target is
 * `.react-flow__node`, an ANCESTOR of `.wf-node`, so the lookup must also
 * walk downward). Works identically whether React Flow is mocked (plain
 * `.wf-node` markup, no wrapper) or real (wrapper present) — both render
 * paths always carry the `wf-node-<id>` testid on WorkflowNode's own root.
 */
function findWfNodeId(target: EventTarget | null): string | null {
  if (!(target instanceof Element)) return null;
  const self = target.closest(`[data-testid^="${WF_NODE_TESTID_PREFIX}"]`);
  const el = self ?? target.querySelector(`[data-testid^="${WF_NODE_TESTID_PREFIX}"]`);
  const testId = el?.getAttribute('data-testid');
  return testId ? testId.slice(WF_NODE_TESTID_PREFIX.length) : null;
}

export function WorkflowCanvas({ kbId }: { kbId: string }) {
  const [lane, setLane] = useState<WorkflowLane>('complex_reasoning');
  const [graph, setGraph] = useState<WorkflowGraph | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [selected, setSelected] = useState<WorkflowNodeData | null>(null);

  useEffect(() => {
    let cancelled = false;
    setError(null);
    fetchWorkflow(kbId, lane)
      .then((g) => { if (!cancelled) setGraph(g); })
      .catch((e: Error) => { if (!cancelled) setError(e.message); });
    // Stale-response guard: a lane switched away from while a slower request
    // is still in flight must not clobber a faster response for the lane the
    // user is now looking at.
    return () => { cancelled = true; };
  }, [kbId, lane]);

  const laid = useMemo(
    () => (graph ? layoutWorkflow(graph) : { nodes: [] as WorkflowRFNode[], edges: [] as Edge[] }),
    [graph],
  );

  const [nodes, setNodes, onNodesChange] = useNodesState(laid.nodes);
  const [edges, setEdges, onEdgesChange] = useEdgesState(laid.edges);

  // useNodesState/useEdgesState seed their internal state ONLY from the value
  // passed on first render. Without this effect, switching lanes refetches
  // `graph` and recomputes `laid`, but the canvas keeps showing the previous
  // lane's nodes — a repaint has to be pushed explicitly.
  useEffect(() => {
    setNodes(laid.nodes);
    setEdges(laid.edges);
  }, [laid, setNodes, setEdges]);

  const findGraphNode = useCallback(
    (target: EventTarget | null): WorkflowNodeData | null => {
      if (!graph) return null;
      const id = findWfNodeId(target);
      if (!id) return null;
      return graph.nodes.find((n) => n.id === id) ?? null;
    },
    [graph],
  );

  // Click delegation: WorkflowNode renders no onClick of its own (it is a
  // frozen, presentation-only component), so the canvas owns click-to-inspect
  // via one delegated listener instead of per-node handlers.
  const onSurfaceClick = useCallback(
    (event: React.MouseEvent) => {
      const found = findGraphNode(event.target);
      if (found) setSelected(found);
    },
    [findGraphNode],
  );

  // Keyboard activation: React Flow puts the tab stop and role="group" on its
  // own `.react-flow__node` wrapper and handles Enter/Space internally, but
  // only to toggle ITS OWN selection state — it does not invoke onNodeClick
  // (confirmed by reading node_modules/@xyflow/react's NodeWrapper: the
  // wrapper's onKeyDown calls handleNodeClick, which only touches the
  // selection store, while onClick — the prop this component would otherwise
  // pass as onNodeClick — is wired solely to the DOM onClick handler). So a
  // keyboard user pressing Enter/Space on a focused node would see nothing
  // happen unless this component opens the inspector itself. This handler is
  // attached to the same wrapper as the click delegate above (no second tab
  // stop is introduced — nothing here sets tabIndex or role on `.wf-node`).
  //
  // preventDefault() only fires once a workflow node is actually resolved:
  // this same delegate also sees Enter/Space presses on React Flow's own
  // <Controls> buttons (zoom, fit view — real <button> elements further down
  // the same wrapper), and unconditionally preventing default there would
  // break their native Space-key activation.
  const onSurfaceKeyDown = useCallback(
    (event: React.KeyboardEvent) => {
      if (event.key !== 'Enter' && event.key !== ' ') return;
      const found = findGraphNode(event.target);
      if (!found) return;
      event.preventDefault();
      setSelected(found);
    },
    [findGraphNode],
  );

  if (error) {
    return (
      <div className="wf-canvas">
        <p className="wf-canvas__error" role="alert">
          Der Workflow konnte nicht geladen werden ({error}).
        </p>
      </div>
    );
  }

  return (
    <div className="wf-canvas">
      <div className="wf-canvas__bar">
        <div className="wf-canvas__lanes">
          {LANES.map((l) => (
            <button key={l.id} type="button" className="wf-canvas__lane"
                    aria-pressed={lane === l.id} onClick={() => { setLane(l.id); setSelected(null); }}>
              {l.label}
            </button>
          ))}
        </div>
        <div className="wf-canvas__legend">
          <span><span className="wf-canvas__swatch" style={{ borderColor: 'var(--accent-primary)' }} />läuft</span>
          <span><span className="wf-canvas__swatch" style={{ borderColor: '#b45309' }} />bedingt</span>
          <span><span className="wf-canvas__swatch" style={{ opacity: 0.5 }} />inaktiv</span>
        </div>
        {graph && graph.orchestrators.length > 0 && (
          <ul className="wf-canvas__orchestrators">
            {graph.orchestrators.map((o) => (
              <li key={o.orchestrator} className="wf-canvas__orchestrator" data-activation={o.activation}>
                {o.orchestrator}
              </li>
            ))}
          </ul>
        )}
        {graph && (
          <span className="wf-canvas__meta">
            geschätzt {graph.estLlmCalls} LLM-Aufrufe · ~{(graph.estLatencyMs / 1000).toFixed(1)}s
          </span>
        )}
      </div>

      {/* eslint-disable-next-line jsx-a11y/no-static-element-interactions -- event delegation only: each interactive descendant (React Flow's own node wrapper — tabIndex=0, role="group" — and the inspector's close button) already owns real keyboard/focus semantics; giving this wrapper its own role/tabIndex would add a second, redundant tab stop over the whole canvas */}
      <div className="wf-canvas__surface" onClick={onSurfaceClick} onKeyDown={onSurfaceKeyDown}>
        <ReactFlow
          nodes={nodes} edges={edges}
          onNodesChange={onNodesChange} onEdgesChange={onEdgesChange}
          nodeTypes={nodeTypes}
          nodesDraggable={false} nodesConnectable={false} elementsSelectable
          fitView proOptions={{ hideAttribution: true }}
        >
          <Background />
          <Controls showInteractive={false} />
        </ReactFlow>
        <NodeInspector node={selected} onClose={() => setSelected(null)} />
      </div>
    </div>
  );
}
