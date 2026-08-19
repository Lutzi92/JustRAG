import { useEffect, useRef } from 'react';
import axios from 'axios';
import { API_BASE_URL } from '../api';
import { useToast } from '../contexts/ToastContext';
import { useTheme } from '../contexts/ThemeContext';
import { takeJoinToken, parkJoinToken, recordJoinAttempt, clearJoinAttempts } from './useJoinLink';

interface RedeemResponse {
    kbId: string;
    kbName: string;
    role: string;
    alreadyMember: boolean;
}

interface UseJoinRedeemParams {
    /** Opens a KB known only by id — useKnowledgeBases.handleOpenKbById. */
    openKbById: (id: string) => Promise<void>;
}

/**
 * Redeems a token parked by the /join/<token> entry route, once, right after
 * the user is authenticated: grant the role, then drop them straight into the
 * KB.
 *
 * Failures are discriminated: a 404 means the link is genuinely invalid or
 * revoked, so the already-cleared token stays gone. Anything else (429 rate
 * limit, 5xx, a dropped connection) is not a verdict on the link, so the
 * token is re-parked for a retry and the user gets a distinct, retryable
 * message instead of being told the link is broken.
 *
 * Re-parking is budgeted (MAX_JOIN_ATTEMPTS). Without a budget a durable
 * non-404 error would re-fire a request and an error toast on every page load
 * for the rest of the session; once the budget is spent the token is dropped
 * and the user is told plainly that it did not work.
 */
export function useJoinRedeem({ openKbById }: UseJoinRedeemParams): void {
    const toast = useToast();
    const { t } = useTheme();
    // React 19 StrictMode runs effects twice in development; the ref makes
    // the redemption idempotent regardless.
    const ran = useRef(false);

    useEffect(() => {
        if (ran.current) return;
        ran.current = true;

        // Taken BEFORE the request on purpose: a 404 from a revoked link must
        // not leave the token parked to be retried on every later mount.
        const token = takeJoinToken();
        if (!token) return;

        void (async () => {
            let result: RedeemResponse;
            try {
                const res = await axios.post<RedeemResponse>(
                    `${API_BASE_URL}/api/invites/${encodeURIComponent(token)}/redeem`);
                result = res.data;
            } catch (err: unknown) {
                // Logged without the error object: an AxiosError carries
                // config.url, which contains the token — printing it would put
                // a live credential in the browser console (and in any
                // error-reporting SDK wired up later).
                const status = axios.isAxiosError(err) ? err.response?.status ?? 0 : 0;
                console.error('Invite-link redemption failed with status', status);

                if (status === 404) {
                    clearJoinAttempts();
                    toast.error(t('joinLinkInvalid'));
                } else if (recordJoinAttempt()) {
                    parkJoinToken(token);
                    toast.error(t('joinLinkRetry'));
                } else {
                    // Budget spent — stop re-parking, or this repeats on every
                    // page load for the rest of the session.
                    toast.error(t('joinLinkFailed'));
                }
                return;
            }

            // Outside the try block on purpose: openKbById swallows its own
            // errors today, but nothing here should assume that — if it ever
            // threw, catching it above would produce a contradictory
            // "invalid link" toast right after the success toast for a join
            // that actually succeeded.
            clearJoinAttempts();
            toast.success(result.alreadyMember
                ? t('joinLinkAlreadyMember')
                : t('joinLinkJoined').replace('{kb}', result.kbName));
            await openKbById(result.kbId);
        })();
    }, [openKbById, toast, t]);
}
