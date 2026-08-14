import { describe, it, expect, vi } from 'vitest';
import type { ComponentProps } from 'react';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { NodeInspector } from './NodeInspector';
import type { WorkflowConfigField, WorkflowNodeData } from '../../../types';

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

// Both keys `data()` uses, registered — the common two-key case.
function bothFields(): Record<string, WorkflowConfigField> {
  return { crag_enabled: boolField(), crag_min_relevant_chunks: intField() };
}

type InspectorProps = ComponentProps<typeof NodeInspector>;

function renderInspector(overrides: Partial<InspectorProps> = {}) {
  const props: InspectorProps = {
    node: data(),
    onClose: vi.fn(),
    fields: {},
    draft: {},
    onChange: vi.fn(),
    onReset: vi.fn(),
    ...overrides,
  };
  return render(<NodeInspector {...props} />);
}

describe('NodeInspector', () => {
  it('renders nothing when no node is selected', () => {
    const { container } = renderInspector({ node: null });
    expect(container).toBeEmptyDOMElement();
  });

  it('shows the label and help text', () => {
    renderInspector();
    expect(screen.getByText('CRAG-Bewertung')).toBeInTheDocument();
    expect(screen.getByText('Bewertet die gefundenen Textstellen.')).toBeInTheDocument();
  });

  it('shows each key with its value and origin in German when no registry field exists', () => {
    renderInspector();
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
    renderInspector();
    expect(screen.getByText('Standardwert')).toBeInTheDocument();
    expect(screen.queryByText('—')).not.toBeInTheDocument();
  });

  it('still shows an em dash when the origin is not "default" but the value is missing', () => {
    // Off-contract (the backend always ships a value for a kb/global origin),
    // so it must not be dressed up as "the code default applies".
    renderInspector({ node: data({ values: {}, origins: { crag_enabled: 'kb' }, keys: ['crag_enabled'] }) });
    expect(screen.getByText('—')).toBeInTheDocument();
  });

  it('shows a non-committal label for an origin outside the kb|global|default contract, never "Standard"', () => {
    // The backend contract promises only kb|global|default, but the panel must
    // not assert "Standard" (deployment default) for a value it can't place —
    // that's a confident wrong answer. Cast needed: the type forbids this.
    const node = data({
      origins: { crag_enabled: 'kb', crag_min_relevant_chunks: 'agent' as WorkflowNodeData['origins'][string] },
    });
    renderInspector({ node });
    expect(screen.getByText('unbekannt')).toBeInTheDocument();
    expect(screen.queryByText('Standard')).not.toBeInTheDocument();
  });

  it('shows the long condition prose, which the node badge omits', () => {
    renderInspector({
      node: data({
        activation: 'conditional',
        reason: 'orchestrator_bypass',
        condition: 'Läuft bei komplexen Fragen im Chat nicht.',
      }),
    });
    expect(screen.getByText('Läuft bei komplexen Fragen im Chat nicht.')).toBeInTheDocument();
  });

  it('shows the reason badge for a disabled stage even with no condition prose', () => {
    // flag_off (the most common inactive state) carries a Reason but no
    // Condition on the backend — without this, the inspector showed nothing
    // at all for the panel's single most common case.
    renderInspector({
      node: data({ activation: 'inactive', reason: 'flag_off', condition: undefined }),
    });
    expect(screen.getByText('Deaktiviert')).toBeInTheDocument();
  });

  it('tells the user when a node is not editable per KB', () => {
    renderInspector({ node: data({ editable: false }) });
    expect(screen.getByText(/nicht pro Knowledge Base einstellbar/i)).toBeInTheDocument();
  });

  it('calls onClose when the close button is pressed', async () => {
    const onClose = vi.fn();
    renderInspector({ onClose });
    await userEvent.click(screen.getByRole('button', { name: /schließen/i }));
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  // --- Task 4: editing ---

  it('renders a control for every key of an editable node that has a registry field', () => {
    renderInspector({ fields: bothFields() });
    expect(screen.getByRole('combobox', { name: boolField().label })).toBeInTheDocument();
    expect(screen.getByRole('spinbutton', { name: intField().label })).toBeInTheDocument();
  });

  it('falls back to the existing read-only row for a key with no registry entry, even on an editable node', () => {
    // The brief's central gotcha: node.editable says nothing about a specific
    // key's registration. Only crag_enabled is registered here.
    renderInspector({ fields: { crag_enabled: boolField() } });
    expect(screen.getByRole('combobox', { name: boolField().label })).toBeInTheDocument();
    // crag_min_relevant_chunks has no field entry -> old bare-key-name row,
    // not a crash, not a silently dropped key.
    expect(screen.getByText('crag_min_relevant_chunks')).toBeInTheDocument();
    expect(screen.queryByRole('spinbutton')).not.toBeInTheDocument();
  });

  it('calls onChange with the key and the new value when a field is edited', async () => {
    const onChange = vi.fn();
    renderInspector({ fields: bothFields(), onChange });
    const control = screen.getByRole('combobox', { name: boolField().label });
    await userEvent.selectOptions(control, 'false');
    expect(onChange).toHaveBeenCalledWith('crag_enabled', 'false');
  });

  it('shows the value already in draft immediately, without waiting for values', () => {
    renderInspector({ fields: bothFields(), draft: { crag_min_relevant_chunks: '7' } });
    const control = screen.getByRole('spinbutton', { name: intField().label });
    expect((control as HTMLInputElement).value).toBe('7');
  });

  it('marks a key present in draft as dirty via a stable data attribute, not a class name', () => {
    renderInspector({ fields: bothFields(), draft: { crag_min_relevant_chunks: '7' } });
    const dirtyRow = screen.getByRole('spinbutton', { name: intField().label }).closest('[data-dirty]');
    expect(dirtyRow).toHaveAttribute('data-dirty', 'true');

    const cleanRow = screen.getByRole('combobox', { name: boolField().label }).closest('[data-dirty]');
    expect(cleanRow).toHaveAttribute('data-dirty', 'false');
  });

  it('offers Reset only for a key whose origin is "kb"', () => {
    renderInspector({
      node: data({ origins: { crag_enabled: 'kb', crag_min_relevant_chunks: 'global' } }),
      fields: bothFields(),
    });
    expect(screen.getByRole('button', { name: new RegExp(boolField().label) })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: new RegExp(intField().label) })).not.toBeInTheDocument();
  });

  it('calls onReset with the key when Reset is pressed', async () => {
    const onReset = vi.fn();
    renderInspector({
      node: data({ origins: { crag_enabled: 'kb', crag_min_relevant_chunks: 'global' } }),
      fields: bothFields(),
      onReset,
    });
    await userEvent.click(screen.getByRole('button', { name: new RegExp(boolField().label) }));
    expect(onReset).toHaveBeenCalledWith('crag_enabled');
  });

  it('renders no editing controls for a non-editable node, even when its keys are registered', () => {
    renderInspector({ node: data({ editable: false }), fields: bothFields() });
    expect(screen.queryByRole('combobox')).not.toBeInTheDocument();
    expect(screen.queryByRole('spinbutton')).not.toBeInTheDocument();
    // Still shows the label instead of a bare key name for the registered field
    // (the registered-but-locked improvement over today).
    expect(screen.getByText(boolField().label)).toBeInTheDocument();
    expect(screen.getByText(/nicht pro Knowledge Base einstellbar/i)).toBeInTheDocument();
  });

  it('offers no Reset for a non-editable node even on a kb-origin key', () => {
    renderInspector({
      node: data({ editable: false, origins: { crag_enabled: 'kb', crag_min_relevant_chunks: 'global' } }),
      fields: bothFields(),
    });
    expect(screen.queryByRole('button', { name: new RegExp(boolField().label) })).not.toBeInTheDocument();
  });

  it('renders every field read-only and offers no Reset when readOnlyReason is set, even on an editable kb-origin key', () => {
    renderInspector({
      fields: bothFields(),
      node: data({ origins: { crag_enabled: 'kb', crag_min_relevant_chunks: 'global' } }),
      readOnlyReason: 'Wird gerade gespeichert.',
    });
    expect(screen.queryByRole('combobox')).not.toBeInTheDocument();
    expect(screen.queryByRole('spinbutton')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: new RegExp(boolField().label) })).not.toBeInTheDocument();
    expect(screen.getByText('Wird gerade gespeichert.')).toBeInTheDocument();
  });
});
