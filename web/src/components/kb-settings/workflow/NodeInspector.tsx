import { X } from 'lucide-react';
import type { ValueOrigin, WorkflowNodeData } from '../../../types';
import './NodeInspector.css';

const ORIGIN_LABEL: Record<ValueOrigin, string> = {
  kb: 'diese KB',
  global: 'global',
  default: 'Standard',
};

interface Props {
  node: WorkflowNodeData | null;
  onClose: () => void;
}

/**
 * NodeInspector shows one node's help text, the reason it is not running (if
 * any), and every config key it owns with the key's value AND where that value
 * came from. The origin column is the point: with global, per-KB and per-agent
 * layers, "why does this KB behave like this" is otherwise unanswerable.
 */
export function NodeInspector({ node, onClose }: Props) {
  if (!node) return null;

  return (
    <aside className="wf-inspector" aria-label={`Details zu ${node.label}`}>
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
      {node.condition && <p className="wf-inspector__condition">{node.condition}</p>}

      {node.keys.length > 0 && (
        <>
          <div className="wf-inspector__section">Einstellungen</div>
          <ul className="wf-inspector__keys">
            {node.keys.map((k) => (
              <li key={k} className="wf-inspector__key">
                <span className="wf-inspector__key-name">{k}</span>
                <span className="wf-inspector__key-meta">
                  <span className="wf-inspector__value">{node.values[k] ?? '—'}</span>
                  <span className="wf-inspector__origin">{ORIGIN_LABEL[node.origins[k]] ?? 'Standard'}</span>
                </span>
              </li>
            ))}
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
