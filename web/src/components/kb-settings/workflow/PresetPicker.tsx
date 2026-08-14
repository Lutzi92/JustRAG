import { useCallback, useEffect, useState } from 'react';
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
   * preset a librarian picks that lane for. Showing the active lane's own
   * number also makes the manual-verification promise checkable: this
   * badge and the canvas's own cost line, after applying, must read the
   * same number, because both come from the same projection. */
  lane: WorkflowLane;
  presetBase: string;
  presetBaseKnown: boolean;
  deviations: string[];
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

interface ConfirmState {
  id: string;
  label: string;
  description: string;
  /** null while the preview request is in flight. */
  overwrites: string[] | null;
}

function formatCost(estLlmCalls: number, estLatencyMs: number): string {
  return `${estLlmCalls} LLM-Aufrufe · ~${(estLatencyMs / 1000).toFixed(1)}s`;
}

/**
 * PresetPicker lists the curated workflow presets above the canvas and lets
 * an admin apply one. Applying is destructive — it overwrites all 21 bundle
 * keys, including ones set by hand — so every apply (and every reset-to-base)
 * goes through a confirmation dialog naming exactly how many of the KB's own
 * settings would be lost. That count comes from `previewPreset` (a GET that
 * runs the identical validation/conflict plan the apply itself would, without
 * writing) — never computed here, so the dialog can never promise an apply
 * the server would then reject.
 *
 * Reuses Phase 4's error/refetch machinery via props rather than owning a
 * second copy: `onError` is the canvas's existing opError setter, `onApplied`
 * is its existing refetchGraph. The one error path this component DOES own
 * (`listError`, below) is the direct analogue of the canvas's own `error`
 * state — "the primary content failed to load" — not a second operational
 * error surface.
 */
export function PresetPicker({
  kbId, lane, presetBase, presetBaseKnown, deviations, draftPending, onError, onApplied,
}: Props) {
  const [presets, setPresets] = useState<WorkflowPreset[] | null>(null);
  const [listError, setListError] = useState<string | null>(null);
  const [confirm, setConfirm] = useState<ConfirmState | null>(null);
  const [applying, setApplying] = useState(false);

  useEffect(() => {
    let cancelled = false;
    fetchPresets()
      .then((p) => { if (!cancelled) { setPresets(p); setListError(null); } })
      .catch((e: Error) => { if (!cancelled) setListError(e.message); });
    return () => { cancelled = true; };
  }, []);

  // A dialog already open, an apply in flight, or a pending hand-edited
  // draft all block a NEW preset action — never a second overlapping one.
  const busy = draftPending || confirm !== null || applying;

  const openConfirm = useCallback((id: string, label: string, description: string) => {
    if (busy) return;
    setConfirm({ id, label, description, overwrites: null });
    previewPreset(kbId, id)
      .then((res) => {
        // Only apply the response if this is still the preset being confirmed
        // — a rapid second click before the first preview resolved must not
        // let a stale response land on the wrong dialog.
        setConfirm((c) => (c && c.id === id ? { ...c, overwrites: res.overwrites } : c));
      })
      .catch((e: Error) => {
        onError(e.message);
        setConfirm(null);
      });
  }, [busy, kbId, onError]);

  const cancelConfirm = useCallback(() => {
    if (applying) return;
    setConfirm(null);
  }, [applying]);

  const confirmApply = useCallback(async () => {
    if (!confirm || confirm.overwrites === null || applying) return;
    setApplying(true);
    try {
      await applyPreset(kbId, confirm.id);
    } catch (e) {
      onError(e instanceof Error ? e.message : String(e));
      setApplying(false);
      setConfirm(null);
      return;
    }
    setApplying(false);
    setConfirm(null);
    await onApplied();
  }, [confirm, applying, kbId, onApplied, onError]);

  const baseLabel = (presetBase && presets?.find((p) => p.id === presetBase)?.label) || presetBase;

  return (
    <div className="wf-presets">
      {listError ? (
        <p className="wf-presets__error" role="alert">
          Vorlagen konnten nicht geladen werden ({listError}).
        </p>
      ) : (
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
      )}

      {draftPending && (
        <p className="wf-presets__hint">
          Speichere oder verwirf deine Änderungen, bevor du eine Vorlage anwendest.
        </p>
      )}

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

      {confirm && (
        <div
          className="modal-overlay"
          role="presentation"
          onClick={(e) => { if (e.target === e.currentTarget) cancelConfirm(); }}
        >
          <div
            className="modal-content wf-presets__confirm"
            role="dialog"
            aria-modal="true"
            aria-labelledby="wf-presets-confirm-title"
          >
            <h3 id="wf-presets-confirm-title">{`„${confirm.label}“ anwenden?`}</h3>
            {confirm.description && <p className="wf-presets__confirm-desc">{confirm.description}</p>}
            {confirm.overwrites === null ? (
              <p className="wf-presets__confirm-loading">Prüft, was sich ändert…</p>
            ) : (
              <>
                <p className="wf-presets__confirm-count">
                  {confirm.overwrites.length === 0
                    ? 'Keine deiner eigenen Einstellungen wird überschrieben.'
                    : `${confirm.overwrites.length} ${confirm.overwrites.length === 1 ? 'deiner Einstellungen wird' : 'deiner Einstellungen werden'} überschrieben.`}
                </p>
                {confirm.overwrites.length > 0 && (
                  <ul className="wf-presets__confirm-keys">
                    {confirm.overwrites.map((k) => <li key={k}><code>{k}</code></li>)}
                  </ul>
                )}
              </>
            )}
            <div className="wf-presets__confirm-actions">
              <button type="button" onClick={cancelConfirm} disabled={applying}>Abbrechen</button>
              <button
                type="button"
                onClick={confirmApply}
                disabled={applying || confirm.overwrites === null}
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
