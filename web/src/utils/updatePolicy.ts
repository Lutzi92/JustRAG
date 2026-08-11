/**
 * Policy for how long a client may keep running a superseded build.
 *
 * Background: the service worker registers with `registerType: 'prompt'`, so a
 * newly deployed worker installs and then WAITS. Until something calls
 * updateServiceWorker(true), the tab keeps serving the previously precached
 * app shell — a plain location.reload() does not help, it just re-serves the
 * precache. That makes the prompt the only exit, and an unconditional dismiss
 * button turns "not now" into "never" for the lifetime of the tab.
 *
 * The rules below bound staleness without ever yanking the page out from under
 * someone who is mid-sentence:
 *
 *   - Dismissing snoozes the banner, it does not cancel the update. The banner
 *     comes back, so "Später" means what it says.
 *   - Past a grace period, the update applies on its own — but only while the
 *     tab is hidden, so the reload lands on a tab nobody is looking at. Any
 *     tab switch is a safe moment; in practice that bounds real-world
 *     staleness to roughly the grace period.
 */

/** How long the banner stays hidden after the user dismisses it. */
export const SNOOZE_MS = 30 * 60_000; // 30 minutes

/**
 * How long a client may stay on a superseded build before the update is
 * applied without asking. Deliberately long: the prompt is the primary path
 * and this is only the backstop for tabs that are never acted on.
 */
export const MAX_DEFERRAL_MS = 2 * 60 * 60_000; // 2 hours

/**
 * Whether the update banner should currently be on screen.
 *
 * `snoozedUntil` is a timestamp, not a boolean, precisely so that dismissing
 * cannot latch permanently.
 */
export function isBannerVisible(
    needRefresh: boolean,
    snoozedUntil: number | null,
    now: number,
): boolean {
    if (!needRefresh) return false;
    if (snoozedUntil !== null && now < snoozedUntil) return false;
    return true;
}

/**
 * Whether to apply the waiting service worker without asking.
 *
 * Requires BOTH that the grace period has elapsed and that the tab is hidden.
 * Reloading a visible tab would discard whatever the user was reading or
 * typing; reloading a hidden one costs them nothing.
 */
export function shouldApplyAutomatically(
    pendingSince: number | null,
    now: number,
    documentHidden: boolean,
): boolean {
    if (pendingSince === null) return false;
    if (!documentHidden) return false;
    return now - pendingSince >= MAX_DEFERRAL_MS;
}
