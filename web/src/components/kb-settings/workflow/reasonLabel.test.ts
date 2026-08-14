import { describe, it, expect } from 'vitest';
import { reasonLabel } from './reasonLabel';

describe('reasonLabel', () => {
  it('maps every known reason to short German', () => {
    expect(reasonLabel('flag_off')).toBe('Deaktiviert');
    expect(reasonLabel('lane_skipped')).toBe('Übersprungen');
    expect(reasonLabel('orchestrator_bypass')).toBe('Übersprungen');
    expect(reasonLabel('superseded_by:self_rag')).toBe('Ersetzt');
  });

  it('returns null for an unknown or absent reason rather than leaking the raw enum', () => {
    expect(reasonLabel(undefined)).toBeNull();
    expect(reasonLabel('something_new')).toBeNull();
  });
});
