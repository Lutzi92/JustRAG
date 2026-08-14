import { describe, it, expect, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { NodeFieldInput } from './NodeFieldInput';
import type { WorkflowConfigField } from '../../../types';

function boolField(over: Partial<WorkflowConfigField> = {}): WorkflowConfigField {
  return {
    key: 'crag_enabled', type: 'bool', group: 'Korrektur',
    label: 'CRAG aktiviert', help: 'Schaltet die CRAG-Bewertung ein.', ...over,
  };
}

function intField(over: Partial<WorkflowConfigField> = {}): WorkflowConfigField {
  return {
    key: 'crag_min_relevant_chunks', type: 'int', group: 'Korrektur',
    label: 'Minimale Trefferzahl', help: 'Mindestanzahl relevanter Treffer.',
    min: 1, max: 10, ...over,
  };
}

function enumField(over: Partial<WorkflowConfigField> = {}): WorkflowConfigField {
  return {
    key: 'docling_table_mode', type: 'enum', group: 'Ingestion',
    label: 'Tabellenmodus', help: 'Wählt den Docling-Tabellenmodus.',
    enum: ['fast', 'accurate'], ...over,
  };
}

describe('NodeFieldInput', () => {
  it('renders a two-state control for a bool field and calls onChange with true/false', async () => {
    const onChange = vi.fn();
    const field = boolField();
    render(<NodeFieldInput field={field} value="false" origin="kb" editable onChange={onChange} />);

    const control = screen.getByRole('combobox', { name: field.label });
    await userEvent.selectOptions(control, 'true');
    expect(onChange).toHaveBeenCalledWith('crag_enabled', 'true');

    await userEvent.selectOptions(control, 'false');
    expect(onChange).toHaveBeenCalledWith('crag_enabled', 'false');
  });

  it('renders a number input carrying min/max for an int field', () => {
    const field = intField();
    render(<NodeFieldInput field={field} value="3" origin="global" editable onChange={vi.fn()} />);

    const control = screen.getByRole('spinbutton', { name: field.label });
    expect(control).toHaveAttribute('min', '1');
    expect(control).toHaveAttribute('max', '10');
    expect((control as HTMLInputElement).value).toBe('3');
  });

  it('renders exactly the enum field options', () => {
    const field = enumField();
    render(<NodeFieldInput field={field} value="fast" origin="kb" editable onChange={vi.fn()} />);

    const control = screen.getByRole('combobox', { name: field.label });
    const options = within(control).getAllByRole('option').map((o) => (o as HTMLOptionElement).value);
    expect(options).toEqual(['fast', 'accurate']);
  });

  it('renders a non-editable field read-only, with no form control', () => {
    const field = boolField();
    render(<NodeFieldInput field={field} value="true" origin="kb" editable={false} onChange={vi.fn()} />);

    expect(screen.queryByRole('textbox')).not.toBeInTheDocument();
    expect(screen.queryByRole('combobox')).not.toBeInTheDocument();
    expect(screen.queryByRole('spinbutton')).not.toBeInTheDocument();
    expect(screen.getByText(field.label)).toBeInTheDocument();
    expect(screen.getByText('true')).toBeInTheDocument();
  });

  it('shows the origin badge using the German word already used in the inspector', () => {
    const field = intField();
    const { rerender } = render(<NodeFieldInput field={field} value="3" origin="kb" editable onChange={vi.fn()} />);
    expect(screen.getByText('diese KB')).toBeInTheDocument();

    rerender(<NodeFieldInput field={field} value="3" origin="global" editable onChange={vi.fn()} />);
    expect(screen.getByText('global')).toBeInTheDocument();

    rerender(<NodeFieldInput field={field} value="3" origin="default" editable onChange={vi.fn()} />);
    expect(screen.getByText('Standard')).toBeInTheDocument();
  });

  it('shows the Standardwert affordance for an unset value instead of an empty box', () => {
    const field = intField();
    render(<NodeFieldInput field={field} value={undefined} origin="default" editable onChange={vi.fn()} />);

    const control = screen.getByRole('spinbutton', { name: field.label });
    expect((control as HTMLInputElement).value).toBe('');
    expect(control).toHaveAttribute('placeholder', 'Standardwert');
  });

  it('shows the Standardwert affordance for a non-editable field with no value anywhere', () => {
    const field = intField();
    render(<NodeFieldInput field={field} value={undefined} origin="default" editable={false} onChange={vi.fn()} />);

    expect(screen.getByText('Standardwert')).toBeInTheDocument();
  });

  it('never emits a clearing signal when the control is emptied — always an explicit string through onChange', async () => {
    const onChange = vi.fn();
    const field = intField();
    render(<NodeFieldInput field={field} value="3" origin="kb" editable onChange={onChange} />);

    const control = screen.getByRole('spinbutton', { name: field.label });
    await userEvent.clear(control);

    expect(onChange).toHaveBeenLastCalledWith('crag_min_relevant_chunks', '');
    for (const call of onChange.mock.calls) {
      expect(typeof call[1]).toBe('string');
    }
  });
});
