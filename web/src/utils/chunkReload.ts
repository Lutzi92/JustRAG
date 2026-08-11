/**
 * Guard for the one-shot reload that recovers from a stale app shell.
 *
 * When a deploy removes the bundles the current shell references, the next
 * lazy import fails and Vite emits `vite:preloadError`. Reloading once is the
 * right recovery — the shell is served no-cache, so the fresh one names the
 * bundles that actually exist.
 *
 * The hazard is reloading forever when the reload does NOT fix it (a stale
 * shell held by an intermediary, or one pod in a rollout still serving the old
 * build). The previous guard stored a boolean and cleared it on every `load`
 * event — including the load its own reload caused — so it only ever
 * suppressed a second reload within a single page session, and a persistently
 * missing chunk looped.
 *
 * Storing the attempt TIME instead makes the guard survive the reload it
 * triggered: a second failure inside the cooldown gives up and lets the error
 * surface, which is recoverable by the user and visible in logs.
 */

/** Minimum spacing between two recovery reloads. */
export const RELOAD_COOLDOWN_MS = 10_000;

export const RELOAD_GUARD_KEY = 'chunk-reload-at';

/**
 * Whether a chunk-load failure should trigger a recovery reload.
 *
 * `lastAttempt` is the timestamp of the previous reload, or null if there has
 * not been one. A malformed stored value is treated as "no previous attempt" —
 * failing toward one reload is better than never recovering.
 */
export function shouldReloadForChunkError(
    lastAttempt: number | null,
    now: number,
): boolean {
    if (lastAttempt === null || !Number.isFinite(lastAttempt)) return true;
    return now - lastAttempt >= RELOAD_COOLDOWN_MS;
}

/** Reads the stored attempt timestamp, tolerating absent/corrupt values. */
export function readLastAttempt(storage: Pick<Storage, 'getItem'>): number | null {
    const raw = storage.getItem(RELOAD_GUARD_KEY);
    if (raw === null) return null;
    const parsed = Number(raw);
    return Number.isFinite(parsed) ? parsed : null;
}
