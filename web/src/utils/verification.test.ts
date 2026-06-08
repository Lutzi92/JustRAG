import { describe, it, expect } from 'vitest';
import { flaggedClaimsFor } from './verification';
import type { MessageVerification, FlaggedClaimStatus } from '../types';

const claim = (text: string): FlaggedClaimStatus => ({ claim_text: text, reason: 'unsupported' });

describe('flaggedClaimsFor', () => {
    it('returns undefined for null/undefined verification', () => {
        expect(flaggedClaimsFor(null)).toBeUndefined();
        expect(flaggedClaimsFor(undefined)).toBeUndefined();
    });

    it('uses factuality.flagged_claims when Self-RAG did not run', () => {
        const v = {
            verified: true, score: 90, issues: [],
            factuality: { flagged_claims: [claim('A')] },
        } as MessageVerification;
        expect(flaggedClaimsFor(v)).toEqual([claim('A')]);
    });

    it('prefers self_rag.issup when Self-RAG ran (replaces factuality)', () => {
        // Backend sets self_rag and leaves factuality nil; the highlight feed
        // must follow self_rag.issup, not the (absent) factuality block.
        const v = {
            verified: true, score: 0, issues: [],
            factuality: null,
            self_rag: { isrel: [], issup: [claim('B')], isuse: { verdict: 'yes', reason: '' } },
        } as MessageVerification;
        expect(flaggedClaimsFor(v)).toEqual([claim('B')]);
    });

    it('returns empty (not factuality) when Self-RAG ran clean', () => {
        const v = {
            verified: true, score: 0, issues: [],
            factuality: { flagged_claims: [claim('stale')] },
            self_rag: { isrel: [], issup: [], isuse: { verdict: 'yes', reason: '' } },
        } as MessageVerification;
        // self_rag present → its (empty) issup wins; the stale factuality block is ignored.
        expect(flaggedClaimsFor(v)).toEqual([]);
    });
});
