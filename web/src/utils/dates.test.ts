import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { formatDate, formatRelative } from './dates';

describe('formatDate', () => {
    it('formats an ISO date for German', () => {
        expect(formatDate('2026-01-05T00:00:00Z', 'de')).toBe('05.01.2026');
    });

    it('formats an ISO date for English', () => {
        expect(formatDate('2026-01-05T00:00:00Z', 'en')).toBe('01/05/2026');
    });

    it('returns the em-dash placeholder for undefined', () => {
        expect(formatDate(undefined, 'de')).toBe('—');
        expect(formatDate(undefined, 'en')).toBe('—');
    });

    it('returns the em-dash placeholder for an invalid date string', () => {
        expect(formatDate('not-a-date', 'de')).toBe('—');
        expect(formatDate('not-a-date', 'en')).toBe('—');
    });
});

describe('formatRelative', () => {
    beforeEach(() => {
        vi.useFakeTimers();
        vi.setSystemTime(new Date('2026-09-06T12:00:00Z'));
    });

    afterEach(() => {
        vi.useRealTimers();
    });

    it('formats a past hour in German', () => {
        expect(formatRelative('2026-09-06T10:00:00Z', 'de')).toBe('vor 2 Stunden');
    });

    it('formats a past hour in English', () => {
        expect(formatRelative('2026-09-06T10:00:00Z', 'en')).toBe('2 hours ago');
    });

    it('formats a past day in German', () => {
        expect(formatRelative('2026-09-03T12:00:00Z', 'de')).toBe('vor 3 Tagen');
    });

    it('formats a past day in English', () => {
        expect(formatRelative('2026-09-03T12:00:00Z', 'en')).toBe('3 days ago');
    });

    it('returns the em-dash placeholder for undefined', () => {
        expect(formatRelative(undefined, 'de')).toBe('—');
        expect(formatRelative(undefined, 'en')).toBe('—');
    });

    it('returns the em-dash placeholder for an invalid date string', () => {
        expect(formatRelative('not-a-date', 'de')).toBe('—');
        expect(formatRelative('not-a-date', 'en')).toBe('—');
    });
});
