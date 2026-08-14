import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  ReactFlow, ReactFlowProvider, Background, Controls,
  useNodesState, useEdgesState, useReactFlow,
} from '@xyflow/react';
import '@xyflow/react/dist/style.css';
import type { Edge } from '@xyflow/react';
import { Save, Undo2 } from 'lucide-react';
import type { NodeActivation, WorkflowGraph, WorkflowLane, WorkflowNodeData } from '../../../types';
import { useReducedMotion } from '../../../hooks/useReducedMotion';
import { fetchWorkflow, fieldFor, saveKbSettings, resetKbSetting } from './api';
import { layoutWorkflow, MIN_ZOOM, type WorkflowRFNode } from './layout';
import WorkflowNode from './WorkflowNode';
import { NodeInspector } from './NodeInspector';
import './WorkflowCanvas.css';

const LANES: { id: WorkflowLane; label: string }[] = [
  { id: 'lookup', label: 'Nachschlagen' },
  { id: 'enumeration', label: 'Aufzählung' },
  { id: 'complex_reasoning', label: 'Komplexe Frage' },
];

/**
 * ACTIVATION_LABEL is the single German wording for the three activation
 * states, used by the legend AND by the orchestrator chips. It matches the node
 * badge for the conditional state ("Bedingt" / "bedingt"), which is the state
 * operators actually stumble over — the legend used to say "bedingt" while the
 * node badge said "Übersprungen", so one state read as two different things.
 */
const ACTIVATION_LABEL: Record<NodeActivation, string> = {
  active: 'läuft',
  conditional: 'bedingt',
  inactive: 'inaktiv',
};

/**
 * ORCHESTRATOR_LABEL mirrors `orchestratorLabels` in
 * go-backend/internal/pipeline/project.go:232-241. Those German names are
 * deliberately kept OFF the wire (the contract stays the bare
 * chat.Orchestrator string), so owning the mapping is the frontend's job — and
 * until now the frontend never did it, printing `plan_execute` and
 * `corpus_table` verbatim to a German-speaking librarian.
 *
 * An unmapped id falls back to the raw string rather than being hidden: a new
 * backend orchestrator should look untranslated, not disappear.
 */
const ORCHESTRATOR_LABEL: Record<string, string> = {
  comparison: 'Dokumentenvergleich',
  team: 'Agenten-Team',
  corpus_table: 'Korpus-Vergleichstabelle',
  drift: 'DRIFT',
  supervisor: 'Supervisor',
  plan_execute: 'Plan-and-Execute',
  agentic: 'Agentische Suche',
  standard: 'Zwei-Schritt-Recherche',
};

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
  const surfaceRef = useRef<HTMLDivElement | null>(null);
  const reducedMotion = useReducedMotion();

  // Unsaved edits, keyed by config key, spanning the WHOLE graph — not one
  // map per node. `ValidateConflicts` on the server judges a save as a batch,
  // and a mutually-exclusive pair (chat_self_rag_enabled vs
  // chat_factuality_verifier_enabled) is a live example: a user moving from
  // one to the other must flip both keys in the SAME PUT, or the first toggle
  // alone 400s and traps them one flag short of the state they want. Per-node
  // draft state would make that impossible to express.
  const [draft, setDraft] = useState<Record<string, string>>({});
  const [saving, setSaving] = useState(false);
  // Surfaces both a rejected save and a rejected reset. Deliberately a
  // separate state from `error` above: `error` is reserved for "the initial
  // load failed" (renders instead of the canvas); a save/reset failure must
  // leave the graph exactly as it was so the user can retry or adjust.
  const [opError, setOpError] = useState<string | null>(null);
  // Set only when onFieldChange refuses to write an empty string-field edit
  // into `draft` (see onFieldChange below); cleared on the next accepted
  // edit, a successful save, or Discard.
  const [emptyStringNote, setEmptyStringNote] = useState<string | null>(null);

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

  // Re-fetches the projection for the CURRENT lane. Activation, edges, the
  // cost estimate and the orchestrator candidates are all server-derived, so
  // a save/reset repaints the graph from a fresh fetch rather than guessing
  // the new state client-side.
  const refetchGraph = useCallback(async () => {
    const fresh = await fetchWorkflow(kbId, lane);
    setGraph(fresh);
  }, [kbId, lane]);

  const draftCount = Object.keys(draft).length;

  const onFieldChange = useCallback((key: string, value: string) => {
    const field = graph ? fieldFor(graph, key) : undefined;
    // siteconfig.Validate's FieldString case accepts "" as a legitimate
    // value — only numeric fields reject an empty string via parse failure.
    // Sending "" for a string key would therefore write a real kb-origin
    // override row with an invisible empty value instead of clearing
    // anything, and the settings UI would show `origin: kb` for a value the
    // admin never knowingly set. Refuse the write outright — an empty string
    // can never enter `draft` for a string-typed field — and point the user
    // at Reset, the only real way to clear one.
    if (field?.type === 'string' && value === '') {
      setEmptyStringNote(`${field.label}: Leerer Wert wird nicht übernommen — zum Löschen "Zurücksetzen" verwenden.`);
      return;
    }
    setEmptyStringNote(null);
    setDraft((d) => ({ ...d, [key]: value }));
  }, [graph]);

  const onDiscardDraft = useCallback(() => {
    setDraft({});
    setOpError(null);
    setEmptyStringNote(null);
  }, []);

  const onSaveDraft = useCallback(async () => {
    if (Object.keys(draft).length === 0) return;
    setSaving(true);
    setOpError(null);
    try {
      // ONE call with every dirty key across the whole graph — the batching
      // guarantee. Saving per-toggle would let a legitimate move between two
      // mutually-exclusive keys 400 on the first half.
      await saveKbSettings(kbId, draft);
      setDraft({});
      await refetchGraph();
    } catch (e) {
      // Keep the draft on failure — the conflict 400 names both keys, and
      // that message is the only thing telling the user why; losing their
      // edits on top of that would mean re-doing work to even read the error.
      setOpError(e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(false);
    }
  }, [kbId, draft, refetchGraph]);

  const onResetKey = useCallback(async (key: string) => {
    setOpError(null);
    try {
      await resetKbSetting(kbId, key);
      await refetchGraph();
      // Reset is "give up on this key and go back to the server's resolved
      // value" — a pending local edit for the SAME key no longer applies.
      // Phase 3's reset button shipped with no try/catch at all and silently
      // swallowed exactly the failure this catch below now surfaces: the
      // DELETE can legitimately 400 when clearing an override falls back to
      // a conflicting global value.
      setDraft((d) => {
        if (!(key in d)) return d;
        const next = { ...d };
        delete next[key];
        return next;
      });
    } catch (e) {
      setOpError(e instanceof Error ? e.message : String(e));
    }
  }, [kbId, refetchGraph]);

  // Warns only on an actual browser navigation away (reload, close, back to
  // another origin) — that loss is unrecoverable from inside the app. It does
  // NOT warn on switching the Settings sub-tab away from Workflow: that tab
  // switch is same-page, one click to return, and a confirm dialog firing on
  // every tab click would be worse than the (easily-redone) loss it prevents.
  // KbSettingsPanel.tsx — which owns that tab switch — is also out of scope
  // for this task.
  useEffect(() => {
    if (draftCount === 0) return;
    const handler = (e: BeforeUnloadEvent) => {
      e.preventDefault();
      e.returnValue = '';
    };
    window.addEventListener('beforeunload', handler);
    return () => window.removeEventListener('beforeunload', handler);
  }, [draftCount]);

  const laid = useMemo(
    () => (graph
      ? layoutWorkflow(graph, { reducedMotion })
      : { nodes: [] as WorkflowRFNode[], edges: [] as Edge[] }),
    [graph, reducedMotion],
  );

  const [nodes, setNodes, onNodesChange] = useNodesState(laid.nodes);
  const [edges, setEdges, onEdgesChange] = useEdgesState(laid.edges);
  const { fitView } = useReactFlow();

  // Set when a NEW graph has just been pushed into React Flow's controlled
  // props, cleared by the fit that consumes it. See the two effects below.
  const refitPendingRef = useRef(true);

  // useNodesState/useEdgesState seed their internal state ONLY from the value
  // passed on first render. Without this effect, switching lanes refetches
  // `graph` and recomputes `laid`, but the canvas keeps showing the previous
  // lane's nodes — a repaint has to be pushed explicitly.
  useEffect(() => {
    refitPendingRef.current = true;
    setNodes(laid.nodes);
    setEdges(laid.edges);
  }, [laid, setNodes, setEdges]);

  // The `fitView` prop on <ReactFlow> below only re-fits once, on mount — RF
  // only re-applies a prop whose value actually changed, and `fitView={true}`
  // never changes. Without this, switching from a large lane to a small one
  // (or vice versa) leaves the new graph at the previous lane's zoom/pan,
  // potentially tucked in a corner or panned off-screen entirely.
  //
  // The dependency has to be `nodes` — the COMMITTED state, not `laid` — so
  // the fit runs only once React Flow has had a render cycle to sync its
  // internal store from the new controlled prop. But `nodes` changes for
  // reasons that are not a new lane: `elementsSelectable` is on, so clicking
  // (or Enter/Space-ing) a node runs RF's own onSelectNodeHandler ->
  // triggerNodeChanges -> onNodesChange -> applyChanges, which ALWAYS returns
  // a new array. Firing fitView on that threw away the zoom the user had just
  // set in order to read the node they were clicking — on the one interaction
  // this whole surface exists to support. So the latch above, set only where a
  // new `laid` is pushed, decides; `nodes` merely times it.
  useEffect(() => {
    if (!refitPendingRef.current) return;
    refitPendingRef.current = false;
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

  // Closing the inspector unmounts the element that holds focus, which drops
  // focus to <body> — a keyboard user loses their place on the canvas entirely
  // and has to tab in from the top. Send it back to the node they opened.
  const closeInspector = useCallback(() => {
    const id = selected?.id;
    setSelected(null);
    if (!id) return;
    // Matched by attribute read rather than an interpolated selector: node ids
    // come from the backend, and building a selector string out of them would
    // need escaping to stay correct.
    const wrapper = Array.from(
      surfaceRef.current?.querySelectorAll<HTMLElement>('.react-flow__node') ?? [],
    ).find((el) => el.getAttribute('data-id') === id);
    wrapper?.focus();
  }, [selected]);

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
  //
  // Escape is handled here too, rather than on the panel itself: the panel is
  // rendered inside this surface, so an Escape pressed with focus in the panel
  // bubbles here — and this side is the one that knows which node to hand focus
  // back to. stopPropagation prevents the Escape from bubbling further up the
  // DOM tree to parent elements.
  const onSurfaceKeyDown = useCallback(
    (event: React.KeyboardEvent) => {
      if (event.key === 'Escape') {
        if (!selected) return;
        event.stopPropagation();
        closeInspector();
        return;
      }
      if (event.key !== 'Enter' && event.key !== ' ') return;
      const found = findGraphNode(event.target);
      if (!found) return;
      event.preventDefault();
      setSelected(found);
    },
    [findGraphNode, selected, closeInspector],
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
          <span><span className="wf-canvas__swatch" style={{ borderColor: 'var(--accent-primary)' }} />{ACTIVATION_LABEL.active}</span>
          <span><span className="wf-canvas__swatch wf-canvas__swatch--conditional" />{ACTIVATION_LABEL.conditional}</span>
          <span><span className="wf-canvas__swatch" style={{ opacity: 0.5 }} />{ACTIVATION_LABEL.inactive}</span>
        </div>
        {graph && graph.orchestrators.length > 0 && (
          <div className="wf-canvas__orchestrators">
            <span className="wf-canvas__orchestrators-label" id="wf-orchestrators-label">Orchestrator:</span>
            <ul className="wf-canvas__orchestrator-list" aria-labelledby="wf-orchestrators-label">
              {graph.orchestrators.map((o) => (
                <li key={o.orchestrator} className="wf-canvas__orchestrator" data-activation={o.activation}>
                  <span className="wf-canvas__orchestrator-name">
                    {ORCHESTRATOR_LABEL[o.orchestrator] ?? o.orchestrator}
                  </span>
                  {/* The state is carried as TEXT, not only as hue+opacity:
                      three chips differing only in colour tell a screen reader
                      nothing and fail for anyone who can't separate the hues. */}
                  <span className="wf-canvas__orchestrator-state">{ACTIVATION_LABEL[o.activation]}</span>
                  {o.condition && (
                    <span className="wf-canvas__orchestrator-condition">{o.condition}</span>
                  )}
                </li>
              ))}
            </ul>
          </div>
        )}
        {graph && (
          <span className="wf-canvas__meta">
            geschätzt {graph.estLlmCalls} LLM-Aufrufe · ~{(graph.estLatencyMs / 1000).toFixed(1)}s
          </span>
        )}
      </div>

      {/* eslint-disable-next-line jsx-a11y/no-static-element-interactions -- event delegation only: each interactive descendant (React Flow's own node wrapper — tabIndex=0, role="group" — and the inspector's close button) already owns real keyboard/focus semantics; giving this wrapper its own role/tabIndex would add a second, redundant tab stop over the whole canvas */}
      <div className="wf-canvas__surface" ref={surfaceRef} onClick={onSurfaceClick} onKeyDown={onSurfaceKeyDown}>
        <ReactFlow
          nodes={nodes} edges={edges}
          onNodesChange={onNodesChange} onEdgesChange={onEdgesChange}
          nodeTypes={nodeTypes}
          nodesDraggable={false} nodesConnectable={false} elementsSelectable
          minZoom={MIN_ZOOM}
          fitView proOptions={{ hideAttribution: true }}
        >
          <Background />
          <Controls showInteractive={false} />
        </ReactFlow>
        <NodeInspector
          node={selected}
          onClose={closeInspector}
          fields={graph?.fields ?? {}}
          draft={draft}
          onChange={onFieldChange}
          onReset={onResetKey}
          readOnlyReason={saving ? 'Wird gerade gespeichert.' : undefined}
        />
      </div>

      {(draftCount > 0 || opError || emptyStringNote) && (
        <div className="wf-canvas__savebar">
          {emptyStringNote && <p className="wf-canvas__savebar-hint">{emptyStringNote}</p>}
          {opError && <p className="wf-canvas__savebar-error" role="alert">{opError}</p>}
          {draftCount > 0 && (
            <div className="wf-canvas__savebar-row">
              <span className="wf-canvas__savebar-count">
                {`${draftCount} ${draftCount === 1 ? 'Einstellung' : 'Einstellungen'} geändert`}
              </span>
              <div className="wf-canvas__savebar-actions">
                <button
                  type="button"
                  className="wf-canvas__savebar-discard"
                  onClick={onDiscardDraft}
                  disabled={saving}
                >
                  <Undo2 size={14} aria-hidden="true" /> Verwerfen
                </button>
                <button
                  type="button"
                  className="wf-canvas__savebar-save"
                  onClick={onSaveDraft}
                  disabled={saving}
                >
                  <Save size={14} aria-hidden="true" /> {saving ? 'Speichert…' : 'Speichern'}
                </button>
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  );
}
