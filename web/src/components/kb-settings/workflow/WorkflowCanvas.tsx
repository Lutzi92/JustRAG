import { useCallback, useEffect, useMemo, useState } from 'react';
import {
  ReactFlow, ReactFlowProvider, Background, Controls,
  useNodesState, useEdgesState, useReactFlow,
} from '@xyflow/react';
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

function idFromTestId(el: Element | null): string | null {
  const testId = el?.getAttribute('data-testid');
  return testId ? testId.slice(WF_NODE_TESTID_PREFIX.length) : null;
}

/**
 * findWfNodeId locates the workflow node id backing a DOM event, regardless
 * of whether the event originated as a mouse click (target is `.wf-node`
 * itself, or a descendant of it — e.g. its label span) or a keyboard
 * activation on the focused React Flow node wrapper (target is
 * `.react-flow__node`, an ANCESTOR of `.wf-node`, so the lookup must also
 * walk downward). Works identically whether React Flow is mocked (plain
 * `.wf-node` markup, no wrapper) or real (wrapper present) — both render
 * paths always carry the `wf-node-<id>` testid on WorkflowNode's own root.
 *
 * The downward walk MUST be anchored to a `.react-flow__node` wrapper found
 * via `closest()` on the target itself — never run from the raw event target.
 * `.react-flow__pane` (empty canvas) and React Flow's own `<Controls>`
 * buttons are also ancestors of every node in the real, unmocked tree; an
 * unanchored `target.querySelector(...)` from either would silently match
 * the FIRST `.wf-node` in DOM order and open the inspector on the wrong
 * stage for a click that hit neither a node nor a keyboard-focused node.
 */
function findWfNodeId(target: EventTarget | null): string | null {
  if (!(target instanceof Element)) return null;

  // Click path: target is `.wf-node` itself, or a descendant of it (e.g. its
  // label span). Takes priority — this is the common case for both the
  // mocked test render (no `.react-flow__node` wrapper exists at all) and a
  // real click on a real node.
  const self = target.closest(`[data-testid^="${WF_NODE_TESTID_PREFIX}"]`);
  if (self) return idFromTestId(self);

  // Keyboard path only: target is the focused `.react-flow__node` wrapper
  // itself. Search *within that exact wrapper*, not from `target` broadly —
  // this is what keeps the pane and Controls buttons from ever matching.
  const wrapper = target.closest('.react-flow__node');
  if (!wrapper) return null;
  return idFromTestId(wrapper.querySelector(`[data-testid^="${WF_NODE_TESTID_PREFIX}"]`));
}

// useReactFlow() only resolves inside a <ReactFlowProvider> ancestor — and
// that ancestor must sit ABOVE the component calling the hook, not merely
// inside the JSX it returns (a component can't consume the context its own
// return value creates). So the exported component is a thin provider
// wrapper; all the real logic — including the fitView-on-lane-switch effect
// below, which needs the hook — lives in WorkflowCanvasInner.
export function WorkflowCanvas({ kbId }: { kbId: string }) {
  return (
    <ReactFlowProvider>
      <WorkflowCanvasInner kbId={kbId} />
    </ReactFlowProvider>
  );
}

function WorkflowCanvasInner({ kbId }: { kbId: string }) {
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
  const { fitView } = useReactFlow();

  // useNodesState/useEdgesState seed their internal state ONLY from the value
  // passed on first render. Without this effect, switching lanes refetches
  // `graph` and recomputes `laid`, but the canvas keeps showing the previous
  // lane's nodes — a repaint has to be pushed explicitly.
  useEffect(() => {
    setNodes(laid.nodes);
    setEdges(laid.edges);
  }, [laid, setNodes, setEdges]);

  // The `fitView` prop on <ReactFlow> below only re-fits once, on mount — RF
  // only re-applies a prop whose value actually changed, and `fitView={true}`
  // never changes. Without this, switching from a large lane to a small one
  // (or vice versa) leaves the new graph at the previous lane's zoom/pan,
  // potentially tucked in a corner or panned off-screen entirely — on a
  // feature whose whole point is showing the lane switch. Keyed on `nodes`
  // (the actual committed state, not `laid`) so it fires only once React Flow
  // has had a render cycle to sync its internal store from the new
  // controlled `nodes` prop.
  useEffect(() => {
    fitView();
  }, [nodes, fitView]);

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
