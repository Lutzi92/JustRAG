import type { ValueOrigin } from '../../../types';

// Single source of truth for both NodeFieldInput.tsx and NodeInspector.tsx.
// Pulled into their own module (rather than exported alongside a component,
// which trips `react-refresh/only-export-components` — that rule fired for a
// real reason elsewhere in this project) and rather than suppressing the
// rule: a constants-only file needs no HMR-boundary exception at all.
export const ORIGIN_LABEL: Record<ValueOrigin, string> = {
  kb: 'diese KB',
  global: 'global',
  default: 'Standard',
};

// An origin outside kb|global|default should never assert "Standard" — that
// tells the user "deployment default" when the truth might be an override
// from a layer this panel can't see. Fail visibly instead.
export const UNKNOWN_ORIGIN_LABEL = 'unbekannt';

// What to print in the value slot for a key nobody has set anywhere.
//
// project.go:65-70 is explicit: Values holds ONLY explicitly-set keys, an
// unset key is absent from the map and shows up in Origins as "default", and
// the UI "must therefore not assume a missing key means an empty value". An
// unset key must read as "the code default applies", never as an
// empty/cleared box.
export const DEFAULT_VALUE_LABEL = 'Standardwert';
