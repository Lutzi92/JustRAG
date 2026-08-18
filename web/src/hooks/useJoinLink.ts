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
