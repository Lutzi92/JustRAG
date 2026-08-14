/**
 * reasonLabel maps a projection reason enum to a short German badge.
 *
 * Returns null for an unknown reason rather than rendering the raw enum: a new
 * backend reason should degrade to "no badge", never leak `superseded_by:foo`
 * into the UI.
 */
export function reasonLabel(reason?: string): string | null {
  switch (reason) {
    case 'flag_off':
      return 'Deaktiviert';
    case 'lane_skipped':
    case 'orchestrator_bypass':
      return 'Übersprungen';
    default:
      if (reason?.startsWith('superseded_by:')) return 'Ersetzt';
      return null;
  }
}
