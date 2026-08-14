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
import { fetchWorkflow, saveKbSettings, resetKbSetting } from './api';
import { layoutWorkflow, MIN_ZOOM, type WorkflowRFNode } from './layout';
import WorkflowNode from './WorkflowNode';
import { NodeInspector } from './NodeInspector';
import { PresetPicker } from './PresetPicker';
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
  // The SELECTION is an id, not a snapshot node object. A snapshot taken at
  // click time would go stale the moment `graph` is replaced by a refetch
  // (save success, reset success) — NodeInspector would keep rendering the
  // pre-save values/origin off the frozen object while the canvas nodes
  // behind it correctly repainted from the server, making a successful save
  // look like it reverted. Deriving `selected` from the CURRENT `graph` below
  // means every graph swap self-heals the inspector for free.
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const surfaceRef = useRef<HTMLDivElement | null>(null);
  const reducedMotion = useReducedMotion();
  // Set true only when the NEXT `laid` push should also trigger a viewport
  // fit — i.e. on the initial load and on an actual lane switch (new
  // topology). Declared here, ahead of the lane-fetch effect below, which is
  // now the one place that arms it. See the fitView effects further down.
  const refitPendingRef = useRef(true);
  // Mirrors `lane` for async code that closes over a specific lane at call
  // time (refetchGraph) and needs to check, after an await, whether that
  // lane is still the one on screen. A plain effect dependency can't do this
  // because the callback's own closure is frozen at call time.
  const laneRef = useRef(lane);
  useEffect(() => { laneRef.current = lane; }, [lane]);

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
  // Set only when a control refuses to emit an empty string-field edit (see
  // NodeFieldInput's onRefuse and `onFieldRefused` below); cleared on the next
  // accepted edit, a selection change, a lane switch, a successful save, or
  // Discard.
  //
  // An OBJECT, never the bare string: refusing the same field twice in a row
  // produces an identical message, and `setState` with an `Object.is`-equal
  // value lets React bail out of the re-render. A fresh object per refusal can
  // never be Object.is-equal, so the render that snaps the controlled input
  // back to the value the draft actually holds always happens.
  //
  // (Measured caveat, so nobody "simplifies" this back and trusts a green
  // suite: react-dom ALSO restores a controlled input's DOM value after every
  // change event it processes, independently of rendering — so the emptied box
  // snaps back either way and no test in this suite can tell the two apart.
  // The object stays because the guarantee should come from our own state, not
  // from an implementation detail of react-dom's event plugin, and because
  // `key` makes the note self-describing.)
  const [emptyStringNote, setEmptyStringNote] = useState<{ key: string; msg: string } | null>(null);

  useEffect(() => {
    let cancelled = false;
    setError(null);
    fetchWorkflow(kbId, lane)
      .then((g) => {
        if (cancelled) return;
        // A genuinely new topology (first load or a real lane switch) is the
        // only case that should re-fit the viewport — see refitPendingRef's
        // declaration above and the fitView effects below.
        refitPendingRef.current = true;
        setGraph(g);
      })
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
  //
  // Deliberately does NOT arm `refitPendingRef` — same lane, same node set,
  // so re-fitting here would only throw away a zoom/pan the admin set to read
  // the very node they just edited (the harm the fitView effects below exist
  // to prevent, on a second, easy-to-miss writer).
  //
  // Deliberately DOES guard against a stale lane: this call notes the lane it
  // was issued for (`requestedLane`), and if the user has since switched
  // lanes, the lane effect above already fetched and rendered the new lane's
  // graph — applying THIS response on top would silently show the old lane's
  // data under the new lane's active pill.
  //
  // `requestedLane` is read from `laneRef` AT CALL TIME, not closed over from
  // the `lane` render value. A save that is in flight while the user switches
  // lanes finishes holding the callback instance created for the OLD lane; a
  // captured `lane` would issue the post-save refetch for that old lane, the
  // guard below would (correctly) discard the response, and nothing would ever
  // refetch the new one. Since the node vocabulary is lane-invariant, the key
  // that was just saved is on screen in the new lane too — showing its
  // pre-save value, on a diagram that self-corrects only on another lane
  // switch. Reading the ref makes the refetch follow the user; the guard still
  // protects a slow response landing after a LATER switch.
  const refetchGraph = useCallback(async () => {
    const requestedLane = laneRef.current;
    const fresh = await fetchWorkflow(kbId, requestedLane);
    if (laneRef.current !== requestedLane) return;
    setGraph(fresh);
    // A successful fetch means the surface is loadable again: clearing a
    // previous lane-fetch failure is what lets the canvas come back without a
    // lane switch (the error branch below replaces the graph surface, so
    // nothing else would ever retire that banner).
    setError(null);
  }, [kbId]);

  // PresetPicker's own refetch handoff — mirrors onSaveDraft/onResetKey's
  // treatment of a post-write refetch failure: the WRITE already landed (the
  // picker's own confirmation dialog is closed by the time this runs), so a
  // refetch failure here must read as "repaint failed", never as "the apply
  // failed" — that would send an admin to retry an apply that already
  // succeeded, potentially re-confirming an overwrite that already happened.
  const onPresetApplied = useCallback(async () => {
    setOpError(null);
    setEmptyStringNote(null);
    try {
      await refetchGraph();
    } catch {
      setOpError('Vorlage angewendet — die Ansicht konnte nicht aktualisiert werden. Bitte Ansicht neu laden.');
    }
  }, [refetchGraph]);

  const draftCount = Object.keys(draft).length;

  // Only ever called with a value the emitting control already vetted — the
  // empty-string refusal for string-typed keys lives in NodeFieldInput, with
  // the component that emits the value, so it travels with any reuse of that
  // control instead of only protecting this one caller. See its `onRefuse`
  // doc comment for why the server cannot be relied on there.
  const onFieldChange = useCallback((key: string, value: string) => {
    setEmptyStringNote(null);
    setDraft((d) => ({ ...d, [key]: value }));
  }, []);

  // The refusal side of the same channel. Always a FRESH object — see
  // emptyStringNote's declaration for what an Object.is-equal repeat would do
  // to the controlled input.
  const onFieldRefused = useCallback((key: string, msg: string) => {
    setEmptyStringNote({ key, msg });
  }, []);

  const onDiscardDraft = useCallback(() => {
    setDraft({});
    setOpError(null);
    setEmptyStringNote(null);
  }, []);

  // The WRITE and the REFETCH are two separate failures and must never be
  // reported as one. They used to share a try: a save that landed, followed by
  // a refetch that 500'd, cleared the draft (edits visibly gone) and then
  // rendered the refetch's message — "fetch workflow: 500" — in the red
  // role="alert". An admin reads that as "it failed" and does it again, or
  // worse, undoes it. The write succeeded; only the picture is stale, and that
  // is what the message has to say.
  const onSaveDraft = useCallback(async () => {
    if (Object.keys(draft).length === 0) return;
    setSaving(true);
    setOpError(null);
    try {
      // ONE call with every dirty key across the whole graph — the batching
      // guarantee. Saving per-toggle would let a legitimate move between two
      // mutually-exclusive keys 400 on the first half.
      await saveKbSettings(kbId, draft);
    } catch (e) {
      // Keep the draft on failure — the conflict 400 names both keys, and
      // that message is the only thing telling the user why; losing their
      // edits on top of that would mean re-doing work to even read the error.
      setOpError(e instanceof Error ? e.message : String(e));
      setSaving(false);
      return;
    }
    // Past this line the values ARE persisted. The draft has served its
    // purpose and is dropped before anything else can fail.
    setDraft({});
    setEmptyStringNote(null);
    try {
      await refetchGraph();
    } catch {
      // Deliberately NOT the fetch error's own text: the operation the admin
      // triggered succeeded, and the only honest thing to report is that the
      // diagram they are looking at may no longer match it.
      setOpError('Gespeichert — die Ansicht konnte nicht aktualisiert werden. Bitte Ansicht neu laden.');
    } finally {
      setSaving(false);
    }
  }, [kbId, draft, refetchGraph]);

  const onResetKey = useCallback(async (key: string) => {
    setOpError(null);
    try {
      // Phase 3's reset button shipped with no try/catch at all and silently
      // swallowed exactly the failure this catch surfaces: the DELETE can
      // legitimately 400 when clearing an override falls back to a conflicting
      // global value.
      await resetKbSetting(kbId, key);
    } catch (e) {
      setOpError(e instanceof Error ? e.message : String(e));
      return;
    }
    // Reset is "give up on this key and go back to the server's resolved
    // value" — a pending local edit for the SAME key no longer applies. Done
    // immediately after the DELETE lands, NOT after the refetch: a successful
    // reset whose refetch then failed used to leave the now-obsolete pending
    // edit sitting in the draft, ready to be saved back over the value the
    // admin just cleared.
    setDraft((d) => {
      if (!(key in d)) return d;
      const next = { ...d };
      delete next[key];
      return next;
    });
    try {
      await refetchGraph();
    } catch {
      setOpError('Zurückgesetzt — die Ansicht konnte nicht aktualisiert werden. Bitte Ansicht neu laden.');
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

  // useNodesState/useEdgesState seed their internal state ONLY from the value
  // passed on first render. Without this effect, switching lanes (or a
  // save/reset refetch) recomputes `laid`, but the canvas keeps showing the
  // previous nodes — a repaint has to be pushed explicitly EVERY time `laid`
  // changes. This is deliberately separate from arming `refitPendingRef`
  // (declared above, near the lane-fetch effect that owns it): pushing new
  // node/edge data must happen on every graph swap so activation dimming and
  // edges stay in sync, but only a genuinely new topology (first load, real
  // lane switch) should also reset the viewport — see the lane-fetch effect
  // and refetchGraph above for why a save/reset refetch must NOT re-arm it.
  useEffect(() => {
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
  // this whole surface exists to support. So `refitPendingRef` (armed only by
  // the lane-fetch effect above, on a genuinely new topology — NOT by every
  // push of `laid`, which also fires for a same-lane save/reset refetch)
  // decides; `nodes` merely times it.
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

  // Derived, not stored: re-resolves against the CURRENT `graph` on every
  // render, so a refetch (save/reset success) that changes this node's
  // values/origins/activation is reflected immediately. If the id no longer
  // exists in a fresh graph (topology changed under it), this simply
  // resolves to null and NodeInspector renders nothing — closing quietly
  // rather than showing stale content tied to a node that's gone.
  const selected = useMemo(
    () => (selectedId ? graph?.nodes.find((n) => n.id === selectedId) ?? null : null),
    [selectedId, graph],
  );

  // Closing the inspector unmounts the element that holds focus, which drops
  // focus to <body> — a keyboard user loses their place on the canvas entirely
  // and has to tab in from the top. Send it back to the node they opened.
  const closeInspector = useCallback(() => {
    const id = selectedId;
    setSelectedId(null);
    if (!id) return;
    // Matched by attribute read rather than an interpolated selector: node ids
    // come from the backend, and building a selector string out of them would
    // need escaping to stay correct.
    const wrapper = Array.from(
      surfaceRef.current?.querySelectorAll<HTMLElement>('.react-flow__node') ?? [],
    ).find((el) => el.getAttribute('data-id') === id);
    wrapper?.focus();
  }, [selectedId]);

  // The single entry point for opening a node, from either input path.
  // Both notes it clears name a FIELD ("Zeitzone: Leerer Wert …", "crag_enabled
  // and … cannot both be enabled") and neither says which node that field was
  // on. Left standing while the admin walks to a different stage, they read as
  // a complaint about the stage now in front of them. The draft itself is
  // untouched — it spans the whole graph on purpose.
  const selectNode = useCallback((id: string) => {
    setSelectedId(id);
    setEmptyStringNote(null);
    setOpError(null);
  }, []);

  // Click delegation: WorkflowNode renders no onClick of its own (it is a
  // frozen, presentation-only component), so the canvas owns click-to-inspect
  // via one delegated listener instead of per-node handlers.
  const onSurfaceClick = useCallback(
    (event: React.MouseEvent) => {
      const found = findGraphNode(event.target);
      if (found) selectNode(found.id);
    },
    [findGraphNode, selectNode],
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
  // This delegate is also why spec §6.1's "put the toggle on the node itself"
  // is deliberately NOT implemented, and must not be reintroduced without
  // reworking both halves below: (1) any focusable control rendered inside
  // `.wf-node` resolves through findWfNodeId's `closest()` here, so Space on
  // an on-node <select> would be preventDefault()ed and open the inspector
  // instead of the dropdown — the control would be unusable by keyboard;
  // (2) `readOnlyReason` covers the INSPECTOR only, so an on-node control
  // would be the one input surface still live during a save, which is exactly
  // the edit that can be lost between the PUT and its refetch.
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
      selectNode(found.id);
    },
    [findGraphNode, selected, closeInspector, selectNode],
  );

  // A load failure replaces THE GRAPH SURFACE — never the whole component.
  // Returning early here used to take the lane pills and the save bar with it:
  // a failed lane fetch after two edits left the draft alive in state with no
  // Save button to commit it, no Discard to drop it, and no lane pill to get
  // back to a lane that loads. The only exit was a reload, which fired the
  // beforeunload warning for a draft the admin could no longer even see.
  return (
    <div className="wf-canvas">
      {/* Suppressed on a load failure, same rule as the orchestrator/cost
          readouts below: `graph` would still hold the PREVIOUS successful
          fetch (or be null on the very first load), and a presetBase/
          deviations line built from stale data would misattribute it to
          whatever the admin is looking at now. */}
      {!error && (
        <PresetPicker
          kbId={kbId}
          lane={lane}
          presetBase={graph?.presetBase ?? ''}
          presetBaseKnown={graph?.presetBaseKnown ?? true}
          deviations={graph?.deviations ?? []}
          draftPending={draftCount > 0}
          onError={setOpError}
          onApplied={onPresetApplied}
        />
      )}
      <div className="wf-canvas__bar">
        <div className="wf-canvas__lanes">
          {LANES.map((l) => (
            <button key={l.id} type="button" className="wf-canvas__lane"
                    aria-pressed={lane === l.id}
                    onClick={() => {
                      setLane(l.id);
                      setSelectedId(null);
                      // A stale refusal hint or a stale op error from the
                      // previous lane has no bearing on the lane just opened.
                      setEmptyStringNote(null);
                      setOpError(null);
                    }}>
              {l.label}
            </button>
          ))}
        </div>
        <div className="wf-canvas__legend">
          <span><span className="wf-canvas__swatch" style={{ borderColor: 'var(--accent-primary)' }} />{ACTIVATION_LABEL.active}</span>
          <span><span className="wf-canvas__swatch wf-canvas__swatch--conditional" />{ACTIVATION_LABEL.conditional}</span>
          <span><span className="wf-canvas__swatch" style={{ opacity: 0.5 }} />{ACTIVATION_LABEL.inactive}</span>
        </div>
        {/* Lane-derived readouts, suppressed while a lane fetch is failing:
            `graph` then still holds the PREVIOUS lane's projection, and
            printing its orchestrators or its cost estimate under the newly
            pressed lane pill would attribute one lane's numbers to another.
            The lane pills and the legend are lane-independent and stay. */}
        {!error && graph && graph.orchestrators.length > 0 && (
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
        {!error && graph && (
          <span className="wf-canvas__meta">
            geschätzt {graph.estLlmCalls} LLM-Aufrufe · ~{(graph.estLatencyMs / 1000).toFixed(1)}s
          </span>
        )}
      </div>

      {error ? (
        <p className="wf-canvas__error" role="alert">
          Der Workflow konnte nicht geladen werden ({error}).
        </p>
      ) : (
        <>
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
              onRefuse={onFieldRefused}
              onReset={onResetKey}
              readOnlyReason={saving ? 'Wird gerade gespeichert.' : undefined}
            />
          </div>
        </>
      )}

      {(draftCount > 0 || opError || emptyStringNote) && (
        <div className="wf-canvas__savebar">
          {emptyStringNote && (
            // aria-live: this line replaces a keystroke the user just made
            // with a refusal — without an explicit live region, a screen
            // reader user gets a silently rejected input and a value that
            // snaps back, with no announcement of why.
            <p className="wf-canvas__savebar-hint" aria-live="polite" data-field={emptyStringNote.key}>
              {emptyStringNote.msg}
            </p>
          )}
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
          {draftCount > 0 && (
            // KbSettingsPanel.tsx unmounts this component on a Settings
            // sub-tab switch (out of this file's scope — see the beforeunload
            // effect's comment above), which drops the draft silently. A
            // confirm dialog on every tab click would be worse than that
            // loss; this line is the mitigation that fits within scope.
            <p className="wf-canvas__savebar-warn">
              Nicht gespeicherte Änderungen gehen beim Tab-Wechsel verloren.
            </p>
          )}
        </div>
      )}
    </div>
  );
}
