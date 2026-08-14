import type { ChangeEvent } from 'react';
import type { ValueOrigin, WorkflowConfigField } from '../../../types';
import { ORIGIN_LABEL, UNKNOWN_ORIGIN_LABEL, DEFAULT_VALUE_LABEL } from './constants';
import './NodeFieldInput.css';

// Sentinel option value for a stored bool that is not one of the four literals
// siteconfig.Validate accepts. Deliberately not a legal bool spelling, and the
// option carrying it is disabled, so it can never be submitted.
const UNKNOWN_BOOL = '__wf_unknown_bool__';

/**
 * normaliseBool maps a stored value onto the two option values this control
 * renders. siteconfig.Validate's FieldBool arm (registry.go) accepts
 * "true"/"false"/"1"/"0" after TrimSpace+ToLower — four spellings, and the
 * global layer or a direct API write can legitimately store any of them. The
 * select offered only "true"/"false", so a stored "0" matched NO option and
 * the browser fell back to the first one, rendering "Ein" for a flag that is
 * off: the control reported the opposite of the truth.
 *
 * Returns undefined for anything unrecognised — which must render as visibly
 * unknown, never as a silent "Ein".
 */
function normaliseBool(value: string | undefined): 'true' | 'false' | undefined {
  if (value === undefined) return undefined;
  switch (value.trim().toLowerCase()) {
    case 'true':
    case '1':
      return 'true';
    case 'false':
    case '0':
      return 'false';
    default:
      return undefined;
  }
}

interface Props {
  field: WorkflowConfigField;
  /** Resolved value, or undefined when nobody has set this key anywhere. */
  value?: string;
  origin?: ValueOrigin;
  editable: boolean;
  /**
   * Always an explicit string — there is no "clear" channel here. Clearing an
   * override is a separate Reset action (DELETE /settings/{key}) that this
   * control must never trigger implicitly.
   *
   * onChange is never called with an empty (or whitespace-only) value for a
   * STRING-typed field: see `onRefuse` and handleChange below for why the
   * server cannot be relied on to catch that one.
   */
  onChange: (key: string, value: string) => void;
  /**
   * Called INSTEAD of onChange when this control refuses to emit an edit, with
   * a ready-to-display German reason. Today the only refusal is the empty
   * string on a string-typed field.
   *
   * The refusal itself is unconditional — omitting this callback makes it
   * silent, never permissive — because it guards a real write: siteconfig's
   * `Validate` returns nil for `FieldString` (registry.go, `case FieldString:
   * return nil`), so the server ACCEPTS "" for a string key and would store a
   * genuine kb-origin override holding an invisible empty value. The settings
   * UI would then read `origin: kb` for a value the admin never knowingly set,
   * and only Reset could clear it. The guard therefore lives here, with the
   * component that emits the value, rather than in one particular caller —
   * anything reusing this input inherits it.
   */
  onRefuse?: (key: string, message: string) => void;
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
export function NodeFieldInput({ field, value, origin, editable, onChange, onRefuse }: Props) {
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
    const next = e.target.value;
    // Trimmed, not just `=== ''`: a whitespace-only value is just as invisible
    // once stored and would slip the same phantom override past a bare
    // equality check. Numeric and enum fields need no such guard — an empty
    // string genuinely fails their `Validate` arm on the server.
    if (field.type === 'string' && next.trim() === '') {
      onRefuse?.(
        field.key,
        `${field.label}: Leerer Wert wird nicht übernommen — zum Löschen "Zurücksetzen" verwenden.`,
      );
      return;
    }
    onChange(field.key, next);
  };

  let control: React.ReactNode;
  if (field.type === 'bool') {
    const selected = normaliseBool(value);
    const unknown = !isUnset && selected === undefined;
    control = (
      <select value={selected ?? (unknown ? UNKNOWN_BOOL : '')} onChange={handleChange}>
        {isUnset && <option value="" disabled>{DEFAULT_VALUE_LABEL}</option>}
        {/* A stored value this control cannot interpret is shown verbatim and
            disabled, so it reads as "something unexpected is set here" rather
            than silently picking one of the two real options for the admin. */}
        {unknown && <option value={UNKNOWN_BOOL} disabled>{value}</option>}
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
