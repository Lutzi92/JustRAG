import { useCallback, useEffect, useRef, useState } from 'react';
import type { WorkflowLane, WorkflowPreset } from '../../../types';
import { fetchPresets, previewPreset, applyPreset } from './api';
import './PresetPicker.css';

interface Props {
  kbId: string;
  /** The canvas's CURRENTLY SELECTED lane. Costs are keyed by lane (see
   * WorkflowPreset), and the picker deliberately shows the figure for THIS
   * lane rather than a single collapsed number: on the complex lane
   * "research" and "standard" project the same total (conditional nodes
   * aren't summed), so a lane-agnostic badge would understate exactly the
   * preset a librarian picks that lane for.
   *
   * What the badge is NOT: this KB's cost. `PricePresets` projects each
   * bundle over the DEPLOYMENT GLOBALS (presets_cost.go), while the canvas's
   * own cost line projects THIS KB. The two diverge whenever the KB overrides
   * a cost-bearing key outside the 21-key preset vocabulary — step_back_enabled
   * and chat_graph_routing_enabled both qualify (nodes.go) — so the card can
   * legitimately read "3 LLM-Aufrufe" while the bar below reads "geschätzt 4".
   * That is why the list carries a footnote saying which configuration the
   * badge describes; an earlier version of this comment claimed the two
   * numbers "must read the same", which was simply false. */
  lane: WorkflowLane;
  presetBase: string;
  presetBaseKnown: boolean;
  deviations: string[];
  /** True while the canvas has not yet loaded a projection (first fetch in
   * flight). presetBase is then "" for want of data, not because the KB has
   * no base — and „Noch keine Vorlage angewendet." is a positive claim that
   * would be a lie for the duration of the load on a KB that does have one.
   * The backend spent a three-state return (presetBaseKnown) keeping
   * "unknown" apart from "none"; this prop keeps the same distinction on the
   * one state the backend cannot express, namely "not answered yet". */
  presetBaseLoading: boolean;
  /** True while the canvas holds unsaved field edits. Applying (or
   * resetting to) a preset overwrites the very keys a draft might be
   * mid-edit on; rather than silently dropping the draft or silently
   * letting a stale draft value get saved back over the preset the admin
   * just chose, preset actions are simply unavailable until the draft is
   * saved or discarded — the draft itself is untouched either way. */
  draftPending: boolean;
  /** Routes every operational failure (preview + apply) through the
   * canvas's EXISTING opError/savebar channel — the same one save/reset
   * already use. PresetPicker never renders its own operation-error text. */
  onError: (message: string) => void;
  /** The canvas's own refetch, called after a successful apply so the
   * diagram repaints from the server — never guessed client-side. */
  onApplied: () => Promise<void> | void;
}

/** The preview's numbers, normalised. Every field is defaulted at the
 * boundary: a 200 with a missing field yields `undefined`, which passes a
 * `=== null` "still loading" guard and then throws on `.length` in render. */
interface ConfirmPlan {
  overwrites: string[];
  effective: string[];
  pinned: number;
}

interface ConfirmState {
  id: string;
  label: string;
  description: string;
  /** null while the preview request is in flight. */
  plan: ConfirmPlan | null;
}

function formatCost(estLlmCalls: number, estLatencyMs: number): string {
  return `${estLlmCalls} LLM-Aufrufe · ~${(estLatencyMs / 1000).toFixed(1)}s`;
}

/** Focusable descendants of the dialog, in tab order. Used by the focus trap
 * below; the dialog's own container is excluded (tabIndex -1). */
function focusablesIn(root: HTMLElement | null): HTMLElement[] {
  if (!root) return [];
  return Array.from(
    root.querySelectorAll<HTMLElement>(
      'button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])',
    ),
  );
}

/**
 * PresetPicker lists the curated workflow presets above the canvas and lets
 * an admin apply one. Applying is destructive — it overwrites all 21 bundle
 * keys, including ones set by hand — so every apply (and every reset-to-base)
 * goes through a confirmation dialog naming exactly what the write costs.
 * Those numbers come from `previewPreset` (a GET that runs the identical
 * validation/conflict plan the apply itself would, without writing) — never
 * computed here, so the dialog can never promise an apply the server would
 * then reject.
 *
 * The dialog states TWO counts, not one. `overwrites` (what the admin
 * personally set and loses) is zero for a KB that never overrode anything —
 * the common case, and the case where the apply still pins all 21 keys and
 * changes behaviour wherever the deployment global disagrees with the bundle.
 * `effective` is that second number. Reporting only the first produced
 * „Keine deiner eigenen Einstellungen wird überschrieben." while, on this
 * deployment, „Standard" silently turned the globally-enabled supervisor off.
 *
 * Reuses Phase 4's error/refetch machinery via props rather than owning a
 * second copy: `onError` is the canvas's existing opError setter, `onApplied`
 * is its existing refetchGraph. The one error path this component DOES own
 * (`listError`, below) is the direct analogue of the canvas's own `error`
 * state — "the primary content failed to load" — not a second operational
 * error surface.
 */
export function PresetPicker({
  kbId, lane, presetBase, presetBaseKnown, deviations, presetBaseLoading,
  draftPending, onError, onApplied,
}: Props) {
  const [presets, setPresets] = useState<WorkflowPreset[] | null>(null);
  const [listError, setListError] = useState<string | null>(null);
  const [confirm, setConfirm] = useState<ConfirmState | null>(null);
  const [applying, setApplying] = useState(false);
  const dialogRef = useRef<HTMLDivElement | null>(null);
  // The element to hand focus back to when the dialog closes — captured in
  // openConfirm, BEFORE `busy` disables the trigger: the browser blurs a
  // disabled element, so by the time an effect could read document.activeElement
  // focus has already fallen to <body>.
  const restoreFocusRef = useRef<HTMLElement | null>(null);
  const rootRef = useRef<HTMLDivElement | null>(null);
  // Monotonic token identifying the preview request belonging to the dialog
  // currently open. Incremented on every open AND on every close, so a
  // response that arrives for a dismissed (or replaced) dialog is dropped —
  // in the .catch as much as in the .then. Without it on the .catch, cancelling
  // a dialog whose preview then rejects raised an error banner for a dialog
  // that is no longer on screen.
  const previewTokenRef = useRef(0);

  useEffect(() => {
    let cancelled = false;
    fetchPresets()
      .then((p) => { if (!cancelled) { setPresets(p); setListError(null); } })
      .catch((e: Error) => { if (!cancelled) setListError(e.message); });
    return () => { cancelled = true; };
  }, []);

  const dialogOpen = confirm !== null;

  // Focus goes INTO the dialog on open and back to the trigger on close.
  // Neither happened before: opening disabled the trigger, the browser blurred
  // it, and focus landed on <body> — from where a keyboard user could tab into
  // the canvas BEHIND the dialog, create a draft in NodeInspector, and tab on
  // to „Anwenden". The cleanup runs after React has committed the render in
  // which the trigger is enabled again, so the restore cannot land on a still-
  // disabled button.
  useEffect(() => {
    if (!dialogOpen) return;
    dialogRef.current?.focus();
    return () => {
      const back = restoreFocusRef.current;
      restoreFocusRef.current = null;
      // The trigger can legitimately be GONE by the time we restore: a
      // successful reset unmounts „Auf Vorlage zurücksetzen" (deviations drop
      // to 0), and a draft that appeared meanwhile disables every card. Falling
      // through to <body> there would strand a keyboard user at the top of the
      // document, so the picker root — focusable only for this purpose — is the
      // fallback: it keeps the tab position where the dialog was opened.
      const target = back && document.contains(back) && !(back as HTMLButtonElement).disabled
        ? back
        : rootRef.current;
      target?.focus();
    };
  }, [dialogOpen]);

  // A dialog already open, an apply in flight, or a pending hand-edited
  // draft all block a NEW preset action — never a second overlapping one.
  const busy = draftPending || confirm !== null || applying;

  const openConfirm = useCallback((id: string, label: string, description: string) => {
    if (busy) return;
    restoreFocusRef.current = document.activeElement as HTMLElement | null;
    const token = ++previewTokenRef.current;
    setConfirm({ id, label, description, plan: null });
    previewPreset(kbId, id)
      .then((res) => {
        // Only apply the response if this is still the preset being confirmed
        // — a rapid second click, or a cancel-and-reopen before the first
        // preview resolved, must not let a stale response land on the wrong
        // dialog.
        if (previewTokenRef.current !== token) return;
        const overwrites = res.overwrites ?? [];
        const effective = res.effective ?? [];
        setConfirm((c) => (c && c.id === id ? {
          ...c,
          plan: {
            overwrites,
            // Server-side `effective` is a superset of `overwrites` by
            // construction (a key the KB overrides to a value the bundle
            // changes necessarily resolves to that value). An empty
            // `effective` alongside a non-empty `overwrites` therefore means
            // the field did not arrive — a server that predates it — and
            // falling back is the difference between the dialog understating
            // and the dialog claiming „Keine Einstellung ändert dadurch ihr
            // Verhalten." over a list of keys it is about to overwrite.
            effective: effective.length > 0 ? effective : overwrites,
            pinned: res.pinned ?? 0,
          },
        } : c));
      })
      .catch((e: Error) => {
        if (previewTokenRef.current !== token) return;
        onError(e.message);
        setConfirm(null);
      });
  }, [busy, kbId, onError]);

  const cancelConfirm = useCallback(() => {
    if (applying) return;
    previewTokenRef.current++;
    setConfirm(null);
  }, [applying]);

  // `draftPending` is re-checked HERE, not only in openConfirm: a draft can
  // appear while the dialog is open (the canvas behind it is still live), and
  // the whole point of the draft gate is that a preset apply and a pending
  // hand-edit must never race for the same keys.
  const confirmApply = useCallback(async () => {
    if (!confirm || confirm.plan === null || applying || draftPending) return;
    setApplying(true);
    try {
      await applyPreset(kbId, confirm.id);
    } catch (e) {
      onError(e instanceof Error ? e.message : String(e));
      setApplying(false);
      previewTokenRef.current++;
      setConfirm(null);
      return;
    }
    setApplying(false);
    previewTokenRef.current++;
    setConfirm(null);
    await onApplied();
  }, [confirm, applying, draftPending, kbId, onApplied, onError]);

  // Escape closes; Tab cycles inside. The trap is what actually closes the
  // draft-gate bypass: without it a keyboard user reaches the canvas behind
  // the dialog, and „Anwenden" is a destructive, irreversible bulk write.
  const onDialogKeyDown = useCallback((event: React.KeyboardEvent<HTMLDivElement>) => {
    if (event.key === 'Escape') {
      event.stopPropagation();
      cancelConfirm();
      return;
    }
    if (event.key !== 'Tab') return;
    const items = focusablesIn(dialogRef.current);
    if (items.length === 0) {
      // Both buttons disabled (apply in flight): keep focus on the dialog
      // rather than letting it escape to the page behind.
      event.preventDefault();
      dialogRef.current?.focus();
      return;
    }
    const first = items[0];
    const last = items[items.length - 1];
    const active = document.activeElement;
    if (event.shiftKey && (active === first || active === dialogRef.current)) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && active === last) {
      event.preventDefault();
      first.focus();
    }
  }, [cancelConfirm]);

  const baseLabel = (presetBase && presets?.find((p) => p.id === presetBase)?.label) || presetBase;
  const plan = confirm?.plan ?? null;

  return (
    <div className="wf-presets" ref={rootRef} tabIndex={-1}>
      {listError ? (
        <p className="wf-presets__error" role="alert">
          Vorlagen konnten nicht geladen werden ({listError}).
        </p>
      ) : (
        <>
          <ul className="wf-presets__list" aria-label="Vorlagen">
            {(presets ?? []).map((p) => {
              const cost = p.costs[lane];
              return (
                <li key={p.id}>
                  <button
                    type="button"
                    className="wf-presets__card"
                    onClick={() => openConfirm(p.id, p.label, p.description)}
                    disabled={busy}
                    aria-current={presetBase === p.id}
                  >
                    <span className="wf-presets__card-label">{p.label}</span>
                    <span className="wf-presets__card-desc">{p.description}</span>
                    {cost && (
                      <span className="wf-presets__card-cost">{formatCost(cost.estLlmCalls, cost.estLatencyMs)}</span>
                    )}
                  </button>
                </li>
              );
            })}
          </ul>
          {presets && presets.length > 0 && (
            // See the `lane` prop's comment: the badge prices the bundle over
            // the DEPLOYMENT defaults, the bar below prices THIS KB. They
            // legitimately differ, and an unexplained "3" above a "geschätzt 4"
            // is exactly the kind of small lie this surface exists to avoid.
            <p className="wf-presets__note">
              Die Kostenangaben gelten für eine Wissensbank ohne weitere eigene Einstellungen.
              Eigene Einstellungen außerhalb der Vorlage können den Wert verschieben, den die
              Leiste unten für diese Wissensbank anzeigt.
            </p>
          )}
        </>
      )}

      {draftPending && (
        <p className="wf-presets__hint">
          Speichere oder verwirf deine Änderungen, bevor du eine Vorlage anwendest.
        </p>
      )}

      {/* Nothing is claimed about the base until the projection has answered:
          `presetBase` is "" while the first fetch is in flight, and rendering
          that as „Noch keine Vorlage angewendet." tells a KB that IS based on
          „Hohe Präzision" the opposite for the duration of the load. */}
      {presetBaseLoading ? (
        <p className="wf-presets__base wf-presets__base--loading">Basis wird geladen…</p>
      ) : (
        <p className="wf-presets__base">
          {!presetBaseKnown ? (
            `Basis: „${presetBase}“ — diese Vorlage gibt es nicht mehr, Abweichungen lassen sich nicht bestimmen.`
          ) : !presetBase ? (
            'Noch keine Vorlage angewendet.'
          ) : (
            <>
              {`Basis: ${baseLabel} · ${deviations.length} ${deviations.length === 1 ? 'Abweichung' : 'Abweichungen'}`}
              {deviations.length > 0 && (
                <button
                  type="button"
                  className="wf-presets__reset"
                  onClick={() => openConfirm(presetBase, baseLabel, '')}
                  disabled={busy}
                >
                  Auf Vorlage zurücksetzen
                </button>
              )}
            </>
          )}
        </p>
      )}

      {confirm && (
        <div
          className="modal-overlay"
          role="presentation"
          onClick={(e) => { if (e.target === e.currentTarget) cancelConfirm(); }}
        >
          {/* eslint-disable-next-line jsx-a11y/no-noninteractive-element-interactions -- a modal dialog is exactly the element that must own Escape and a Tab trap: the handler is what keeps focus from reaching the canvas behind an open destructive confirmation. The container is deliberately focusable (tabIndex -1, focused on open) but is NOT a control and must not get an interactive role. */}
          <div
            className="modal-content wf-presets__confirm"
            role="dialog"
            aria-modal="true"
            aria-labelledby="wf-presets-confirm-title"
            ref={dialogRef}
            tabIndex={-1}
            onKeyDown={onDialogKeyDown}
          >
            <h3 id="wf-presets-confirm-title">{`„${confirm.label}“ anwenden?`}</h3>
            {confirm.description && <p className="wf-presets__confirm-desc">{confirm.description}</p>}
            {plan === null ? (
              <p className="wf-presets__confirm-loading">Prüft, was sich ändert…</p>
            ) : (
              <>
                <p className="wf-presets__confirm-count">
                  {plan.effective.length === 0
                    ? 'Keine Einstellung ändert dadurch ihr Verhalten.'
                    : `${plan.effective.length} ${plan.effective.length === 1 ? 'Einstellung ändert' : 'Einstellungen ändern'} dadurch ihr Verhalten.`}
                </p>
                {plan.effective.length > 0 && (
                  <>
                    <p className="wf-presets__confirm-own">
                      {plan.overwrites.length === 0
                        ? 'Keine davon hast du selbst gesetzt.'
                        : `Davon hast du ${plan.overwrites.length} selbst gesetzt — unten markiert.`}
                    </p>
                    <ul className="wf-presets__confirm-keys">
                      {plan.effective.map((k) => (
                        <li key={k}>
                          <code>{k}</code>
                          {plan.overwrites.includes(k) && (
                            <span className="wf-presets__confirm-own-tag"> — von dir gesetzt</span>
                          )}
                        </li>
                      ))}
                    </ul>
                  </>
                )}
                {/* The second fact an apply owes the admin, and the one the
                    old dialog never mentioned: it writes the WHOLE vocabulary
                    as per-KB rows, so those keys stop following the deployment
                    defaults — permanently, and regardless of how many of them
                    change anything today. */}
                {plan.pinned > 0 && (
                  <p className="wf-presets__confirm-pinned">
                    {`${plan.pinned} ${plan.pinned === 1 ? 'Einstellung wird' : 'Einstellungen werden'} dabei für diese Wissensbank festgeschrieben und ${plan.pinned === 1 ? 'folgt' : 'folgen'} danach nicht mehr den globalen Vorgaben.`}
                  </p>
                )}
              </>
            )}
            <div className="wf-presets__confirm-actions">
              <button type="button" onClick={cancelConfirm} disabled={applying}>Abbrechen</button>
              <button
                type="button"
                onClick={confirmApply}
                disabled={applying || plan === null || draftPending}
              >
                {applying ? 'Wird angewendet…' : 'Anwenden'}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
