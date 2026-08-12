import type { ReactNode } from 'react';

export interface AgentEntityRowProps {
  icon: ReactNode;
  name: string;
  secondary?: ReactNode;
  actions: ReactNode;
}

/**
 * One agent/team row: icon, name, muted secondary line, caller-supplied actions.
 *
 * `actions` is a slot rather than fixed buttons because the call sites differ —
 * AgentsView wants edit + delete, KbAgentsSection wants a default radio plus an
 * attach/detach toggle. The row owns layout, truncation, and card chrome only.
 */
export function AgentEntityRow({ icon, name, secondary, actions }: AgentEntityRowProps) {
  return (
    <div
      className="result-card"
      style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', padding: '0.75rem', marginTop: '0.5rem' }}
    >
      {icon}
      {/* min-width:0 is load-bearing: without it the flex child refuses to
          shrink and long descriptions push the actions out of the card. */}
      <div style={{ flex: 1, minWidth: 0 }}>
        <strong style={{ display: 'block', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
          {name}
        </strong>
        {secondary && (
          <div
            className="form-hint"
            style={{ margin: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}
          >
            {secondary}
          </div>
        )}
      </div>
      <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', flexShrink: 0 }}>
        {actions}
      </div>
    </div>
  );
}

export default AgentEntityRow;
