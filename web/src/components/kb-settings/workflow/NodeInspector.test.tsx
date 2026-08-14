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

  it('shows an em dash for a key that is unset everywhere', () => {
    render(<NodeInspector node={data()} onClose={vi.fn()} />);
    expect(screen.getByText('—')).toBeInTheDocument();
  });

  it('shows the long condition prose, which the node badge omits', () => {
    render(<NodeInspector node={data({
      activation: 'conditional',
      reason: 'orchestrator_bypass',
      condition: 'Läuft bei komplexen Fragen im Chat nicht.',
    })} onClose={vi.fn()} />);
    expect(screen.getByText('Läuft bei komplexen Fragen im Chat nicht.')).toBeInTheDocument();
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
