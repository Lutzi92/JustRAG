import { describe, it, expect } from 'vitest';
import { translations } from './translations';

// de/en parity guard (improvement #5): fails when any t() key is missing — or
// blank in — either language, so the localization pass cannot silently regress
// (e.g. a new key added with only a German value).
describe('translations de/en parity', () => {
    const entries = Object.entries(translations) as [string, { de?: string; en?: string }][];

    it('defines a non-empty German value for every key', () => {
        const missing = entries
            .filter(([, v]) => !v.de || v.de.trim() === '')
            .map(([k]) => k);
        expect(missing, `keys missing a German value: ${missing.join(', ')}`).toEqual([]);
    });

    it('defines a non-empty English value for every key', () => {
        const missing = entries
            .filter(([, v]) => !v.en || v.en.trim() === '')
            .map(([k]) => k);
        expect(missing, `keys missing an English value: ${missing.join(', ')}`).toEqual([]);
    });
});
