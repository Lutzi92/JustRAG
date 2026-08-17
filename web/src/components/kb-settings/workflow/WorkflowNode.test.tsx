import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import WorkflowNode from './WorkflowNode';
import type { WorkflowNodeData } from '../../../types';

vi.mock('@xyflow/react', () => ({
  Handle: () => null,
  Position: { Top: 'top', Bottom: 'bottom' },
}));

function data(over: Partial<WorkflowNodeData> = {}): WorkflowNodeData {
  return {
    id: 'crag_grade', label: 'CRAG-Bewertung', group: 'Korrektur',
    help: 'Bewertet die gefundenen Textstellen.', keys: ['crag_enabled'],
    alwaysOn: false, llmCalls: 1, latencyMs: 600, activation: 'active',
    values: {}, origins: {}, editable: true, ...over,
  };
}

const renderNode = (d: WorkflowNodeData) =>
  render(<WorkflowNode id={d.id} data={{ data: d }} selected={false} />);

describe('WorkflowNode', () => {
  it('shows the label and the group rail', () => {
    renderNode(data());
    expect(screen.getByText('CRAG-Bewertung')).toBeInTheDocument();
    expect(screen.getByText('Korrektur')).toBeInTheDocument();
  });

  it('marks its activation state on the root element', () => {
    const { container } = renderNode(data({ activation: 'inactive', reason: 'flag_off' }));
    expect(container.querySelector('[data-activation="inactive"]')).toBeTruthy();
  });

  it('shows a short badge for a conditional node, not the long condition prose', () => {
    renderNode(data({
      activation: 'conditional',
      reason: 'orchestrator_bypass',
      condition: 'Läuft bei komplexen Fragen im Chat nicht: dort beantwortet der Orchestrator die Frage direkt und überspringt diese Stufe des Standard-Ablaufs.',
    }));
    // "Bedingt", not "Übersprungen": project.go deliberately projects an
    // orchestrator-bypassed stage as conditional because it still runs on the
    // non-streaming, MCP and eval paths. See reasonLabel.ts.
    expect(screen.getByText('Bedingt')).toBeInTheDocument();
    expect(screen.queryByText('Übersprungen')).not.toBeInTheDocument();
    expect(screen.queryByText(/Standard-Ablaufs/)).not.toBeInTheDocument();
  });

  it('badges a genuinely inactive, lane-skipped stage "Übersprungen"', () => {
    renderNode(data({ activation: 'inactive', reason: 'lane_skipped' }));
    expect(screen.getByText('Übersprungen')).toBeInTheDocument();
  });

  it('badges a stage whose prerequisite is off, instead of rendering it silent', () => {
    renderNode(data({ activation: 'inactive', reason: 'requires:citation_validation' }));
    expect(screen.getByText('Voraussetzung fehlt')).toBeInTheDocument();
  });

  it('shows the LLM-call cost only when the node actually runs', () => {
    renderNode(data({ activation: 'active', llmCalls: 1 }));
    expect(screen.getByText(/1 LLM/)).toBeInTheDocument();
  });

  it('hides the cost chip on an inactive node', () => {
    renderNode(data({ activation: 'inactive', reason: 'flag_off', llmCalls: 1 }));
    expect(screen.queryByText(/1 LLM/)).not.toBeInTheDocument();
  });

  it('omits the cost chip entirely for a zero-cost node', () => {
    renderNode(data({ llmCalls: 0 }));
    expect(screen.queryByText(/LLM/)).not.toBeInTheDocument();
  });
});
