import { describe, it, expect, vi } from 'vitest';
import { render, screen, within, fireEvent } from '@testing-library/react';
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

  // siteconfig.Validate's FieldBool arm accepts "true"/"false"/"1"/"0". The
  // select offered only the first two, so a stored "0" matched no option and
  // the browser fell back to the first — rendering "Ein" for a flag that is
  // OFF. The control reported the opposite of the truth.
  it.each([
    ['1', 'true'],
    ['0', 'false'],
    ['TRUE', 'true'],
    [' false ', 'false'],
  ])('renders the stored bool spelling %s as %s', (stored, expected) => {
    const field = boolField();
    render(<NodeFieldInput field={field} value={stored} origin="global" editable onChange={vi.fn()} />);

    const control = screen.getByRole('combobox', { name: field.label }) as HTMLSelectElement;
    expect(control.value).toBe(expected);
  });

  it('shows an uninterpretable stored bool verbatim instead of silently picking an option', () => {
    const field = boolField();
    render(<NodeFieldInput field={field} value="vielleicht" origin="global" editable onChange={vi.fn()} />);

    const control = screen.getByRole('combobox', { name: field.label }) as HTMLSelectElement;
    expect(control.value).not.toBe('true');
    expect(control.value).not.toBe('false');
    expect(screen.getByText('vielleicht')).toBeInTheDocument();
  });

  // IMPORTANT 5: the guard against writing an empty string override lives
  // HERE, with the component that emits the value — not one component away in
  // WorkflowCanvas. siteconfig.Validate's `case FieldString: return nil` means
  // the server accepts "", so any reuse of this input without the guard would
  // ship a phantom-override bug.
  describe('empty value on a string-typed field', () => {
    const stringField = (): WorkflowConfigField => ({
      key: 'chat_date_timezone', type: 'string', group: 'Datum',
      label: 'Zeitzone', help: 'Zeitzone für Datumsangaben.',
    });

    it('refuses to emit it, reporting through onRefuse instead of onChange', async () => {
      const onChange = vi.fn();
      const onRefuse = vi.fn();
      render(<NodeFieldInput field={stringField()} value="Europe/Berlin" origin="kb" editable
                             onChange={onChange} onRefuse={onRefuse} />);

      await userEvent.clear(screen.getByRole('textbox', { name: 'Zeitzone' }));

      expect(onChange).not.toHaveBeenCalled();
      expect(onRefuse).toHaveBeenCalledWith('chat_date_timezone', expect.stringContaining('Leerer Wert'));
    });

    it('refuses a whitespace-only value the same way', () => {
      const onChange = vi.fn();
      const onRefuse = vi.fn();
      render(<NodeFieldInput field={stringField()} value="Europe/Berlin" origin="kb" editable
                             onChange={onChange} onRefuse={onRefuse} />);

      fireEvent.change(screen.getByRole('textbox', { name: 'Zeitzone' }), { target: { value: '   ' } });

      expect(onChange).not.toHaveBeenCalled();
      expect(onRefuse).toHaveBeenCalled();
    });

    // The refusal is unconditional: without a callback it is silent, never
    // permissive. A reuser who forgets onRefuse gets a quiet control, not a
    // phantom override.
    it('still refuses when no onRefuse callback is supplied', async () => {
      const onChange = vi.fn();
      render(<NodeFieldInput field={stringField()} value="Europe/Berlin" origin="kb" editable onChange={onChange} />);

      await userEvent.clear(screen.getByRole('textbox', { name: 'Zeitzone' }));

      expect(onChange).not.toHaveBeenCalled();
    });
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
