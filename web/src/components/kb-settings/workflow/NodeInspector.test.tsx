import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { NodeInspector } from './NodeInspector';
import type { WorkflowNodeData } from '../../../types';

function data(over: Partial<WorkflowNodeData> = {}): WorkflowNodeData {
  return {
    id: 'crag_grade', label: 'CRAG-Bewertung', group: 'Korrektur',
    help: 'Bewertet die gefundenen Textstellen.',
    keys: ['crag_enabled', 'crag_min_relevant_chunks'],
    alwaysOn: false, llmCalls: 1, latencyMs: 600, activation: 'active',
    values: { crag_enabled: 'true' },
    origins: { crag_enabled: 'kb', crag_min_relevant_chunks: 'default' },
    editable: true, ...over,
  };
}

describe('NodeInspector', () => {
  it('renders nothing when no node is selected', () => {
    const { container } = render(<NodeInspector node={null} onClose={vi.fn()} />);
    expect(container).toBeEmptyDOMElement();
  });

  it('shows the label and help text', () => {
    render(<NodeInspector node={data()} onClose={vi.fn()} />);
    expect(screen.getByText('CRAG-Bewertung')).toBeInTheDocument();
    expect(screen.getByText('Bewertet die gefundenen Textstellen.')).toBeInTheDocument();
  });

  it('shows each key with its value and origin in German', () => {
    render(<NodeInspector node={data()} onClose={vi.fn()} />);
    expect(screen.getByText('crag_enabled')).toBeInTheDocument();
    expect(screen.getByText('true')).toBeInTheDocument();
    expect(screen.getByText('diese KB')).toBeInTheDocument();
    expect(screen.getByText('Standard')).toBeInTheDocument();
  });

  it('says the code default applies for a key that is unset everywhere, not "nothing"', () => {
    // project.go:65-70 is explicit that Values holds only explicitly-set keys
    // and that a missing key must NOT be read as an empty value. An em dash
    // read as "nothing is set" — most damagingly on factcheck_in_chat, which
    // defaults to true (project.go:179): the canvas drew "Faktencheck" as
    // ACTIVE while its inspector row looked blank.
    render(<NodeInspector node={data()} onClose={vi.fn()} />);
    expect(screen.getByText('Standardwert')).toBeInTheDocument();
    expect(screen.queryByText('—')).not.toBeInTheDocument();
  });

  it('still shows an em dash when the origin is not "default" but the value is missing', () => {
    // Off-contract (the backend always ships a value for a kb/global origin),
    // so it must not be dressed up as "the code default applies".
    render(<NodeInspector node={data({ values: {}, origins: { crag_enabled: 'kb' }, keys: ['crag_enabled'] })} onClose={vi.fn()} />);
    expect(screen.getByText('—')).toBeInTheDocument();
  });

  it('shows a non-committal label for an origin outside the kb|global|default contract, never "Standard"', () => {
    // The backend contract promises only kb|global|default, but the panel must
    // not assert "Standard" (deployment default) for a value it can't place —
    // that's a confident wrong answer. Cast needed: the type forbids this.
    const node = data({
      origins: { crag_enabled: 'kb', crag_min_relevant_chunks: 'agent' as WorkflowNodeData['origins'][string] },
    });
    render(<NodeInspector node={node} onClose={vi.fn()} />);
    expect(screen.getByText('unbekannt')).toBeInTheDocument();
    expect(screen.queryByText('Standard')).not.toBeInTheDocument();
  });

  it('shows the long condition prose, which the node badge omits', () => {
    render(<NodeInspector node={data({
      activation: 'conditional',
      reason: 'orchestrator_bypass',
      condition: 'Läuft bei komplexen Fragen im Chat nicht.',
    })} onClose={vi.fn()} />);
    expect(screen.getByText('Läuft bei komplexen Fragen im Chat nicht.')).toBeInTheDocument();
  });

  it('shows the reason badge for a disabled stage even with no condition prose', () => {
    // flag_off (the most common inactive state) carries a Reason but no
    // Condition on the backend — without this, the inspector showed nothing
    // at all for the panel's single most common case.
    render(<NodeInspector node={data({
      activation: 'inactive',
      reason: 'flag_off',
      condition: undefined,
    })} onClose={vi.fn()} />);
    expect(screen.getByText('Deaktiviert')).toBeInTheDocument();
  });

  it('tells the user when a node is not editable per KB', () => {
    render(<NodeInspector node={data({ editable: false })} onClose={vi.fn()} />);
    expect(screen.getByText(/nicht pro Knowledge Base einstellbar/i)).toBeInTheDocument();
  });

  it('calls onClose when the close button is pressed', async () => {
    const onClose = vi.fn();
    render(<NodeInspector node={data()} onClose={onClose} />);
    await userEvent.click(screen.getByRole('button', { name: /schließen/i }));
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});
