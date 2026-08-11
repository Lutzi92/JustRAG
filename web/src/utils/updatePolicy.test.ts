import { describe, it, expect } from 'vitest';
import {
    isBannerVisible,
    shouldApplyAutomatically,
    SNOOZE_MS,
    MAX_DEFERRAL_MS,
} from './updatePolicy';

const T0 = 1_700_000_000_000;

describe('isBannerVisible', () => {
    it('stays hidden while no update is waiting', () => {
        expect(isBannerVisible(false, null, T0)).toBe(false);
    });

    it('shows as soon as an update is waiting', () => {
        expect(isBannerVisible(true, null, T0)).toBe(true);
    });

    it('hides for the snooze window after a dismiss', () => {
        const snoozedUntil = T0 + SNOOZE_MS;
        expect(isBannerVisible(true, snoozedUntil, T0)).toBe(false);
        expect(isBannerVisible(true, snoozedUntil, T0 + SNOOZE_MS - 1)).toBe(false);
    });

    // The regression this whole change exists for: dismissing used to clear
    // needRefresh outright, and the banner never returned for that tab.
    it('comes back once the snooze expires', () => {
        const snoozedUntil = T0 + SNOOZE_MS;
        expect(isBannerVisible(true, snoozedUntil, snoozedUntil)).toBe(true);
        expect(isBannerVisible(true, snoozedUntil, snoozedUntil + 60_000)).toBe(true);
    });
});

describe('shouldApplyAutomatically', () => {
    it('does nothing when no update is pending', () => {
        expect(shouldApplyAutomatically(null, T0 + MAX_DEFERRAL_MS * 10, true)).toBe(false);
    });

    it('waits out the grace period', () => {
        expect(shouldApplyAutomatically(T0, T0, true)).toBe(false);
        expect(shouldApplyAutomatically(T0, T0 + MAX_DEFERRAL_MS - 1, true)).toBe(false);
    });

    it('never reloads a tab the user is looking at', () => {
        const wayPastDeadline = T0 + MAX_DEFERRAL_MS * 5;
        expect(shouldApplyAutomatically(T0, wayPastDeadline, false)).toBe(false);
    });

    it('applies once the grace period has passed and the tab is hidden', () => {
        expect(shouldApplyAutomatically(T0, T0 + MAX_DEFERRAL_MS, true)).toBe(true);
        expect(shouldApplyAutomatically(T0, T0 + MAX_DEFERRAL_MS + 1, true)).toBe(true);
    });

    it('is independent of snoozing — dismissing cannot defer past the deadline', () => {
        // A user who dismisses every 30 minutes for hours still converges.
        const dismissedRepeatedly = T0 + MAX_DEFERRAL_MS + SNOOZE_MS * 4;
        expect(shouldApplyAutomatically(T0, dismissedRepeatedly, true)).toBe(true);
    });
});
