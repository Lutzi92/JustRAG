import { useEffect, useRef } from 'react';
import { RotateCcw, X } from 'lucide-react';
import type { WorkflowConfigField, WorkflowNodeData } from '../../../types';
import { reasonLabel } from './reasonLabel';
import { fieldFor } from './api';
import { NodeFieldInput } from './NodeFieldInput';
import { ORIGIN_LABEL, UNKNOWN_ORIGIN_LABEL, DEFAULT_VALUE_LABEL } from './constants';
import './NodeInspector.css';

interface Props {
  node: WorkflowNodeData | null;
  onClose: () => void;
  /** Registry field metadata for every key any node references; a miss means
   * the key is not per-KB configurable (see fieldFor's doc comment). */
  fields: Record<string, WorkflowConfigField>;
  /** Unsaved edits, keyed by config key. Presence of a key here — not its
   * value — is what makes a row "dirty": typing the resolved value right
   * back in still counts, because the user explicitly touched it. */
  draft: Record<string, string>;
  onChange: (key: string, value: string) => void;
  onReset: (key: string) => void;
  /** When set, every control renders read-only and Reset is withheld,
   * regardless of the node's own editability — e.g. a save in flight. The
   * reason is shown to the user, not just enforced silently. */
  readOnlyReason?: string;
}

/**
 * NodeInspector shows one node's help text, the reason it is not running (if
 * any), and every config key it owns with the key's value AND where that value
 * came from. The origin column is the point: with global, per-KB and per-agent
 * layers, "why does this KB behave like this" is otherwise unanswerable.
 *
 * The panel takes focus when it opens. Without that, a keyboard user pressing
 * Enter on a node got no signal at all that anything had happened — focus
 * stayed on the node, and reaching the panel meant tabbing forward through
 * every remaining node plus React Flow's own Controls. Escape-to-close and
 * focus restoration to the originating node are the canvas's job — it already
 * owns one delegated keydown listener over the whole surface, and it is the
 * side that knows which node id to send focus back to. See WorkflowCanvas.
 *
 * Editing (Task 4): `node.editable` says nothing about any key OTHER than
 * `keys[0]` — it is derived from that key alone (project.go). An editable
 * node routinely has keys with no registry entry (`fieldFor` returns
 * undefined for the majority of them across the vocabulary), so each key is
 * resolved independently: a control renders only where the key has a
 * registry field AND the node is editable AND the panel isn't read-only;
 * everything else keeps the original read-only row — now with the field's
 * label instead of a bare key name whenever a field exists at all, which is
 * strictly better than before even for a locked node.
 */
export function NodeInspector({ node, onClose, fields, draft, onChange, onReset, readOnlyReason }: Props) {
  const panelRef = useRef<HTMLElement | null>(null);
  const nodeId = node?.id;

  // Keyed on the node id, not the object: re-selecting the same node must not
  // yank focus back out of the panel a user has already tabbed into, but
  // switching to a different node must move focus to the new content.
  useEffect(() => {
    if (nodeId) panelRef.current?.focus();
  }, [nodeId]);

  if (!node) return null;

  const badge = reasonLabel(node.activation, node.reason);

  return (
    <aside
      ref={panelRef}
      className="wf-inspector"
      // -1: programmatically focusable, but never a tab stop of its own.
      tabIndex={-1}
      aria-label={`Details zu ${node.label}`}
    >
      <div className="wf-inspector__head">
        <div>
          <span className="wf-inspector__group">{node.group}</span>
          <h3 className="wf-inspector__title">{node.label}</h3>
        </div>
        <button type="button" onClick={onClose} aria-label="Schließen"
                style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text-secondary)' }}>
          <X size={16} aria-hidden="true" />
        </button>
      </div>

      {node.help && <p className="wf-inspector__help">{node.help}</p>}
      {badge && <span className="wf-inspector__reason">{badge}</span>}
      {node.condition && <p className="wf-inspector__condition">{node.condition}</p>}

      {node.keys.length > 0 && (
        <>
          <div className="wf-inspector__section">Einstellungen</div>
          {readOnlyReason && <p className="wf-inspector__note">{readOnlyReason}</p>}
          <ul className="wf-inspector__keys">
            {node.keys.map((k) => {
              const field = fieldFor({ fields }, k);
              const origin = node.origins[k];
              const dirty = Object.prototype.hasOwnProperty.call(draft, k);
              const value = draft[k] ?? node.values[k];
              const canEdit = node.editable && field !== undefined && !readOnlyReason;
              const canReset = origin === 'kb' && node.editable && !readOnlyReason;

              return (
                <li key={k} className="wf-inspector__key" data-dirty={dirty}>
                  {field ? (
                    <NodeFieldInput field={field} value={value} origin={origin} editable={canEdit} onChange={onChange} />
                  ) : (
                    <>
                      <span className="wf-inspector__key-name">{k}</span>
                      <span className="wf-inspector__key-meta">
                        <span className={origin === 'default' ? 'wf-inspector__value wf-inspector__value--default' : 'wf-inspector__value'}>
                          {origin === 'default' ? DEFAULT_VALUE_LABEL : (value ?? '—')}
                        </span>
                        <span className="wf-inspector__origin">{ORIGIN_LABEL[origin] ?? UNKNOWN_ORIGIN_LABEL}</span>
                      </span>
                    </>
                  )}
                  {canReset && (
                    <button
                      type="button"
                      className="wf-inspector__reset"
                      onClick={() => onReset(k)}
                      aria-label={`${field?.label ?? k} zurücksetzen`}
                      title="Auf globalen Wert zurücksetzen"
                    >
                      <RotateCcw size={12} aria-hidden="true" /> Zurücksetzen
                    </button>
                  )}
                </li>
              );
            })}
          </ul>
        </>
      )}

      {!node.editable && node.keys.length > 0 && (
        <p className="wf-inspector__note">
          Diese Stufe ist derzeit nicht pro Knowledge Base einstellbar — sie folgt der globalen Konfiguration.
        </p>
      )}
    </aside>
  );
}
