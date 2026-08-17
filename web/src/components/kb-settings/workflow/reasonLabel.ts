import type { NodeActivation } from '../../../types';

/**
 * reasonLabel maps a node's (activation, reason) pair to a short German badge.
 *
 * Activation is read FIRST and deliberately: `internal/pipeline/project.go`
 * spends a paragraph (`complexBypassCondition`, :250-268) explaining why an
 * orchestrator-bypassed stage is `conditional` and NOT `inactive` — it still
 * runs for non-streaming turns, for the MCP and eval paths, and whenever the
 * orchestrator errors out. Badging that "Übersprungen" ("was skipped") asserts
 * the opposite of what the backend chose to say and collapses the three states
 * back to two on the single most operationally surprising stage. So every
 * conditional node reads "Bedingt" — the same word the canvas legend uses —
 * whatever its reason, and "Übersprungen" is reserved for a genuinely inactive
 * stage.
 *
 * Returns null for an unknown reason rather than rendering the raw enum: a new
 * backend reason should degrade to "no badge", never leak `superseded_by:foo`
 * into the UI.
 */
export function reasonLabel(activation: NodeActivation, reason?: string): string | null {
  // A stage the backend calls conditional is never "skipped" — see above.
  if (activation === 'conditional') return 'Bedingt';

  switch (reason) {
    case 'flag_off':
      return 'Deaktiviert';
    case 'lane_skipped':
    case 'orchestrator_bypass':
      return 'Übersprungen';
    default:
      // `requires:<other node>` — the stage is configured but structurally
      // cannot fire, because a prerequisite stage is off (project.go:293, :306
      // emit `requires:citation_validation`). Rendering nothing left those
      // nodes dimmed and silent beside a `flag_off` node that says "Deaktiviert".
      if (reason?.startsWith('requires:')) return 'Voraussetzung fehlt';
      if (reason?.startsWith('superseded_by:')) return 'Ersetzt';
      return null;
  }
}
