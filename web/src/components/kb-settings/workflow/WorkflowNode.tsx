import { Handle, Position } from '@xyflow/react';
import { Sparkles } from 'lucide-react';
import type { WorkflowNodeData } from '../../../types';
import './WorkflowNode.css';

/**
 * reasonLabel maps a projection reason enum to a short German badge.
 *
 * Returns null for an unknown reason rather than rendering the raw enum: a new
 * backend reason should degrade to "no badge", never leak `superseded_by:foo`
 * into the UI.
 */
export function reasonLabel(reason?: string): string | null {
  switch (reason) {
    case 'flag_off':
      return 'Deaktiviert';
    case 'lane_skipped':
    case 'orchestrator_bypass':
      return 'Übersprungen';
    default:
      if (reason?.startsWith('superseded_by:')) return 'Ersetzt';
      return null;
  }
}

interface Props {
  id: string;
  data: { data: WorkflowNodeData };
  selected?: boolean;
}

export default function WorkflowNode({ data: { data: n } }: Props) {
  const badge = reasonLabel(n.reason);
  const showCost = n.activation !== 'inactive' && n.llmCalls > 0;

  return (
    <div className="wf-node" data-activation={n.activation} data-testid={`wf-node-${n.id}`}>
      <Handle type="target" position={Position.Top} />
      <span className="wf-node__group">{n.group}</span>
      <span className="wf-node__label">{n.label}</span>
      <div className="wf-node__meta">
        {badge && (
          <span
            className={
              n.activation === 'conditional'
                ? 'wf-node__badge wf-node__badge--conditional'
                : 'wf-node__badge'
            }
          >
            {badge}
          </span>
        )}
        {showCost && (
          <span className="wf-node__cost">
            <Sparkles size={11} aria-hidden="true" />
            {n.llmCalls} LLM
          </span>
        )}
      </div>
      <Handle type="source" position={Position.Bottom} />
    </div>
  );
}
