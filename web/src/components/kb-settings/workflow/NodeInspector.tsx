import { useEffect, useRef } from 'react';
import { X } from 'lucide-react';
import type { ValueOrigin, WorkflowNodeData } from '../../../types';
import { reasonLabel } from './reasonLabel';
import './NodeInspector.css';

const ORIGIN_LABEL: Record<ValueOrigin, string> = {
  kb: 'diese KB',
  global: 'global',
  default: 'Standard',
};

// An origin outside kb|global|default should never assert "Standard" — that
// tells the user "deployment default" when the truth might be an override
// from a layer this panel can't see. Fail visibly instead.
const UNKNOWN_ORIGIN_LABEL = 'unbekannt';

// What to print in the value slot for a key nobody has set anywhere.
//
// project.go:65-70 is explicit: Values holds ONLY explicitly-set keys, an unset
// key is absent from the map and shows up in Origins as "default", and the UI
// "must therefore not assume a missing key means an empty value". An em dash
// did exactly that — on a deployment that has configured nothing, every row
// read "key — Standard", i.e. "nothing is set". Worst on factcheck_in_chat,
// which defaults to TRUE (project.go:179): the canvas drew "Faktencheck" as
// active while its inspector row looked empty.
const DEFAULT_VALUE_LABEL = 'Standardwert';

interface Props {
  node: WorkflowNodeData | null;
  onClose: () => void;
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
 */
export function NodeInspector({ node, onClose }: Props) {
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
          <ul className="wf-inspector__keys">
            {node.keys.map((k) => {
              const unset = node.origins[k] === 'default';
              return (
                <li key={k} className="wf-inspector__key">
                  <span className="wf-inspector__key-name">{k}</span>
                  <span className="wf-inspector__key-meta">
                    <span className={unset ? 'wf-inspector__value wf-inspector__value--default' : 'wf-inspector__value'}>
                      {unset ? DEFAULT_VALUE_LABEL : (node.values[k] ?? '—')}
                    </span>
                    <span className="wf-inspector__origin">{ORIGIN_LABEL[node.origins[k]] ?? UNKNOWN_ORIGIN_LABEL}</span>
                  </span>
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
