import type { ChangeEvent } from 'react';
import type { ValueOrigin, WorkflowConfigField } from '../../../types';
import { ORIGIN_LABEL, UNKNOWN_ORIGIN_LABEL, DEFAULT_VALUE_LABEL } from './constants';
import './NodeFieldInput.css';

interface Props {
  field: WorkflowConfigField;
  /** Resolved value, or undefined when nobody has set this key anywhere. */
  value?: string;
  origin?: ValueOrigin;
  editable: boolean;
  /**
   * Always an explicit string — there is no "clear" channel here. Clearing an
   * override is a separate Reset action (DELETE /settings/{key}, wired by a
   * later task) that this control must never trigger implicitly. An empty
   * input still calls onChange with "" — that is a PUT of an empty value,
   * which the server validates and rejects for typed fields; it is not a
   * silent wipe.
   */
  onChange: (key: string, value: string) => void;
}

/**
 * NodeFieldInput renders one config-registry field as one control, per the
 * shapes established by AgentConfigFields.tsx (bool -> select, enum -> select,
 * otherwise input with min/max/step). The semantics differ from that sibling
 * component: AgentConfigFields uses an empty value to mean "inherit from the
 * KB" and removes the override. Here an override lives at the KB layer
 * itself, so there is no implicit parent to fall back to by emptying a box —
 * emptying it is just editing it. Clearing back to the deployment default is
 * an explicit, separate action.
 */
export function NodeFieldInput({ field, value, origin, editable, onChange }: Props) {
  const isUnset = value === undefined;

  const originBadge = origin && (
    <span className="wf-field__origin">{ORIGIN_LABEL[origin] ?? UNKNOWN_ORIGIN_LABEL}</span>
  );

  if (!editable) {
    return (
      <div className="wf-field">
        <span className="wf-field__row" title={field.help}>
          <span className="wf-field__label">{field.label}</span>
          <span className={isUnset ? 'wf-field__value wf-field__value--default' : 'wf-field__value'}>
            {isUnset ? DEFAULT_VALUE_LABEL : value}
          </span>
        </span>
        {originBadge}
      </div>
    );
  }

  const handleChange = (e: ChangeEvent<HTMLInputElement | HTMLSelectElement>) => {
    onChange(field.key, e.target.value);
  };

  let control: React.ReactNode;
  if (field.type === 'bool') {
    control = (
      <select value={value ?? ''} onChange={handleChange}>
        {isUnset && <option value="" disabled>{DEFAULT_VALUE_LABEL}</option>}
        <option value="true">Ein</option>
        <option value="false">Aus</option>
      </select>
    );
  } else if (field.type === 'enum') {
    control = (
      <select value={value ?? ''} onChange={handleChange}>
        {isUnset && <option value="" disabled>{DEFAULT_VALUE_LABEL}</option>}
        {(field.enum ?? []).map((v) => (
          <option key={v} value={v}>{v}</option>
        ))}
      </select>
    );
  } else {
    control = (
      <input
        type={field.type === 'string' ? 'text' : 'number'}
        min={field.min}
        max={field.max}
        step={field.type === 'float' ? 0.05 : 1}
        value={value ?? ''}
        placeholder={isUnset ? DEFAULT_VALUE_LABEL : undefined}
        onChange={handleChange}
      />
    );
  }

  return (
    <div className="wf-field">
      <label className="wf-field__row" title={field.help}>
        <span className="wf-field__label">{field.label}</span>
        {control}
      </label>
      {originBadge}
    </div>
  );
}
