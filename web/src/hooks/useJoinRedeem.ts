import { useEffect, useRef } from 'react';
import axios from 'axios';
import { API_BASE_URL } from '../api';
import { useToast } from '../contexts/ToastContext';
import { useTheme } from '../contexts/ThemeContext';
import { takeJoinToken } from './useJoinLink';

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
 * KB. A failure (revoked or invalid link) only toasts and leaves them on the
 * overview.
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
            try {
                const res = await axios.post<RedeemResponse>(
                    `${API_BASE_URL}/api/invites/${encodeURIComponent(token)}/redeem`);
                toast.success(res.data.alreadyMember
                    ? t('joinLinkAlreadyMember')
                    : t('joinLinkJoined').replace('{kb}', res.data.kbName));
                await openKbById(res.data.kbId);
            } catch (err: unknown) {
                console.error('Invite-link redemption failed:', err);
                toast.error(t('joinLinkInvalid'));
            }
        })();
    }, [openKbById, toast, t]);
}
