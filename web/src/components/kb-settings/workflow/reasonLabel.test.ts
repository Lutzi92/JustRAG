import { describe, it, expect } from 'vitest';
import { reasonLabel } from './reasonLabel';

// Mirrors the Reason enum documented on ProjectedNode in
// go-backend/internal/pipeline/project.go:57-59, verbatim and in order:
//
//   "flag_off" | "lane_skipped" | "orchestrator_bypass" |
//   "superseded_by:self_rag" | "requires:citation_validation"
//
// The completeness assertion below iterates THIS list rather than a hand-picked
// set of calls, so the suite's name ("maps every known reason") is true by
// construction: adding a reason to the backend enum and to this list without
// adding a case to reasonLabel fails the test. `requires:citation_validation`
// is exactly the entry that used to be missing — it fell through to null, so
// those nodes rendered dimmed and silent next to a `flag_off` node that said
// "Deaktiviert".
const BACKEND_REASONS = [
  'flag_off',
  'lane_skipped',
  'orchestrator_bypass',
  'superseded_by:self_rag',
  'requires:citation_validation',
] as const;

describe('reasonLabel', () => {
  it('maps every known reason to short German', () => {
    for (const reason of BACKEND_REASONS) {
      expect(reasonLabel('inactive', reason), reason).not.toBeNull();
    }
  });

  it('uses the specific wording per inactive reason', () => {
    expect(reasonLabel('inactive', 'flag_off')).toBe('Deaktiviert');
    expect(reasonLabel('inactive', 'lane_skipped')).toBe('Übersprungen');
    expect(reasonLabel('inactive', 'orchestrator_bypass')).toBe('Übersprungen');
    expect(reasonLabel('inactive', 'superseded_by:self_rag')).toBe('Ersetzt');
    expect(reasonLabel('inactive', 'requires:citation_validation')).toBe('Voraussetzung fehlt');
  });

  it('badges a conditional node "Bedingt", never "Übersprungen"', () => {
    // project.go's complexBypassCondition is explicit that an
    // orchestrator-bypassed stage is conditional and NOT inactive — it still
    // runs on the non-streaming, MCP and eval paths, and on orchestrator
    // failure. "Übersprungen" asserts the opposite.
    expect(reasonLabel('conditional', 'orchestrator_bypass')).toBe('Bedingt');
    expect(reasonLabel('conditional', undefined)).toBe('Bedingt');
    expect(reasonLabel('conditional', 'lane_skipped')).toBe('Bedingt');
  });

  it('badges nothing for a plainly active node', () => {
    expect(reasonLabel('active', undefined)).toBeNull();
  });

  it('returns null for an unknown or absent reason rather than leaking the raw enum', () => {
    expect(reasonLabel('inactive', undefined)).toBeNull();
    expect(reasonLabel('inactive', 'something_new')).toBeNull();
  });
});
