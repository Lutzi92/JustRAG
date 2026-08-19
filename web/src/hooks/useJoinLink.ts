// Invite-link entry point. Someone opening https://<host>/join/<token> may
// not be signed in yet, and signing in via OIDC is a top-level navigation
// away from the app and back. sessionStorage survives that navigation within
// the same tab, which is exactly the scope we want: localStorage would leave
// the token lying around for every other tab and every later session.
//
// Cost of that choice: opening the link in one tab and signing in in another
// loses the join, and the person clicks the link again.
//
// The SPA catch-all in the Go server (routes.go) already serves index.html
// for /join/..., so no backend routing is involved.

export const JOIN_TOKEN_KEY = 'pendingJoinToken';

// 43 base64url characters is what the backend mints; the range stays loose
// enough to survive a future token-length change but tight enough that a
// stray path never looks like a token.
const JOIN_PATH = /^\/join\/([A-Za-z0-9_-]{20,64})$/;

function storage(): Storage | null {
    try {
        return window.sessionStorage;
    } catch {
        // Safari in private mode throws on access.
        return null;
    }
}

/**
 * Reads a /join/<token> URL, parks the token for the post-login redemption,
 * and rewrites the URL to "/" so a reload does not replay the join.
 */
export function captureJoinToken(): void {
    const match = JOIN_PATH.exec(window.location.pathname);
    if (!match) return;
    storage()?.setItem(JOIN_TOKEN_KEY, match[1]);
    window.history.replaceState(null, '', '/');
}

/** Returns the parked token without consuming it. */
export function peekJoinToken(): string | null {
    return storage()?.getItem(JOIN_TOKEN_KEY) ?? null;
}

/** Returns the parked token and clears it, so it is redeemed at most once. */
export function takeJoinToken(): string | null {
    const s = storage();
    if (!s) return null;
    const token = s.getItem(JOIN_TOKEN_KEY);
    s.removeItem(JOIN_TOKEN_KEY);
    return token;
}

/**
 * Re-parks a token that takeJoinToken() already consumed, so a retryable
 * redemption failure (network error, 5xx, rate limit) can be retried on the
 * next mount instead of forcing the user to find the original link again.
 * Not for the 404 case: a genuinely invalid/revoked token must stay cleared.
 */
export function parkJoinToken(token: string): void {
    storage()?.setItem(JOIN_TOKEN_KEY, token);
}

// Attempt budget for re-parked tokens. Re-parking is unconditional for every
// non-404 failure, so a DURABLE error — an invite row pointing at a
// half-deleted KB, a backend stuck returning 500 — would otherwise re-fire a
// request and an error toast on every page load for the rest of the tab
// session, with no way for the user to stop it. Two attempts survives a
// genuine blip (a dropped connection, one rate-limited burst) without turning
// a permanent failure into a permanent loop.
export const JOIN_ATTEMPTS_KEY = 'pendingJoinAttempts';
export const MAX_JOIN_ATTEMPTS = 2;

/**
 * Records one failed attempt and reports whether another is allowed.
 * Returns true while the budget lasts, false once it is spent.
 */
export function recordJoinAttempt(): boolean {
    const s = storage();
    if (!s) return false;
    const attempts = Number(s.getItem(JOIN_ATTEMPTS_KEY) ?? '0') + 1;
    if (attempts >= MAX_JOIN_ATTEMPTS) {
        s.removeItem(JOIN_ATTEMPTS_KEY);
        return false;
    }
    s.setItem(JOIN_ATTEMPTS_KEY, String(attempts));
    return true;
}

/** Clears the attempt budget — on success, and on a terminal 404. */
export function clearJoinAttempts(): void {
    storage()?.removeItem(JOIN_ATTEMPTS_KEY);
}
