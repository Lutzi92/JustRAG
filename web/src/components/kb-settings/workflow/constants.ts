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

// The projection's id for the KB-default agent/team node — pipeline.NodeAgentBinding
// (go-backend/internal/pipeline/nodes.go). It is the ONE place the frontend
// matches a node by id, because it is the one node whose inspector shows a
// control that is not driven by the site_config registry: `keys` is empty, so
// there is nothing else to key the control off.
//
// Drift risk, stated rather than hidden: renaming the Go NodeID would make the
// control silently disappear (the node would still draw, its inspector would
// just lose the dropdown), and no test on either side of the wire can see that
// — the Go wire pin checks the SHAPE of `agentBinding`, not this id. If the
// vocabulary ever gains a second non-registry control, give the projection an
// explicit per-node control marker instead of growing this list.
export const WF_NODE_AGENT_BINDING = 'agent_binding';
