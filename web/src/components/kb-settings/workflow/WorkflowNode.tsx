import { Handle, Position } from '@xyflow/react';
import { Sparkles } from 'lucide-react';
import type { WorkflowNodeData } from '../../../types';
import { reasonLabel } from './reasonLabel';
import './WorkflowNode.css';

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
