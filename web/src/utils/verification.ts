import type { MessageVerification, FlaggedClaimStatus } from '../types';

// flaggedClaimsFor selects which per-claim flags drive the inline sub-claim
// highlighting in a message. It mirrors the backend contract documented on
// MessageVerification.SelfRAG: when the AP-D2 Self-RAG verifier ran it
// REPLACES the legacy factuality block and the claims live under
// self_rag.issup; otherwise fall back to factuality.flagged_claims. Without
// this fallback, enabling chat_self_rag silently drops all highlights.
export function flaggedClaimsFor(
    v: MessageVerification | null | undefined,
): FlaggedClaimStatus[] | undefined {
    if (!v) return undefined;
    if (v.self_rag) return v.self_rag.issup;
    return v.factuality?.flagged_claims;
}
