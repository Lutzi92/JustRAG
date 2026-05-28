/**
 * Opens an external URL in a new tab safely:
 * - Rejects anything that isn't http:/https: (blocks javascript:, data:, etc.)
 * - Always passes `noopener,noreferrer` so the opened page can't reach
 *   `window.opener` (reverse tabnabbing).
 *
 * Use this instead of a bare `window.open()` for any URL that originates from
 * an API response or other untrusted source.
 *
 * @returns true if the URL was opened, false if it was rejected.
 */
export function openExternal(href: string): boolean {
    try {
        // Parse without a base so relative/garbage input is rejected rather
        // than silently resolved against the current origin. External link
        // targets are always absolute URLs.
        const u = new URL(href);
        if (u.protocol !== 'http:' && u.protocol !== 'https:') {
            return false;
        }
        window.open(u.href, '_blank', 'noopener,noreferrer');
        return true;
    } catch {
        // Malformed URL — swallow and report failure.
        return false;
    }
}
