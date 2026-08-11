import { describe, it, expect } from 'vitest';
import {
    shouldReloadForChunkError,
    readLastAttempt,
    RELOAD_COOLDOWN_MS,
    RELOAD_GUARD_KEY,
} from './chunkReload';

const T0 = 1_700_000_000_000;

describe('shouldReloadForChunkError', () => {
    it('reloads on the first failure', () => {
        expect(shouldReloadForChunkError(null, T0)).toBe(true);
    });

    // The loop the old boolean guard allowed: reload -> load clears guard ->
    // chunk still missing -> reload -> ... forever.
    it('does not reload again immediately after a failed recovery', () => {
        expect(shouldReloadForChunkError(T0, T0)).toBe(false);
        expect(shouldReloadForChunkError(T0, T0 + RELOAD_COOLDOWN_MS - 1)).toBe(false);
    });

    it('allows a fresh recovery once the cooldown has passed', () => {
        expect(shouldReloadForChunkError(T0, T0 + RELOAD_COOLDOWN_MS)).toBe(true);
        // A later, unrelated deploy must still be recoverable.
        expect(shouldReloadForChunkError(T0, T0 + 60 * 60_000)).toBe(true);
    });

    it('treats a corrupt timestamp as no previous attempt', () => {
        expect(shouldReloadForChunkError(Number.NaN, T0)).toBe(true);
    });
});

describe('readLastAttempt', () => {
    const storageWith = (value: string | null) => ({ getItem: () => value });

    it('returns null when nothing is stored', () => {
        expect(readLastAttempt(storageWith(null))).toBeNull();
    });

    it('parses a stored timestamp', () => {
        expect(readLastAttempt(storageWith(String(T0)))).toBe(T0);
    });

    it('returns null for a non-numeric value', () => {
        expect(readLastAttempt(storageWith('nonsense'))).toBeNull();
    });

    it('uses the documented storage key', () => {
        expect(RELOAD_GUARD_KEY).toBe('chunk-reload-at');
    });
});
