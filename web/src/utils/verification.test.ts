import { describe, it, expect } from 'vitest';
import { flaggedClaimsFor, citationSpanFor, excerptAroundSpan } from './verification';
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

describe('citationSpanFor', () => {
    it('returns the span for a verified citation with a span', () => {
        const v = {
            verified: true, score: 0, issues: [],
            citations: [{ n: 1, verified: true, method: 'span', span: { start: 6, end: 14 } }],
        } as MessageVerification;
        expect(citationSpanFor(v, 1)).toEqual({ start: 6, end: 14 });
    });

    it('returns undefined when the entry has no span', () => {
        const v = {
            verified: true, score: 0, issues: [],
            citations: [{ n: 1, verified: true, method: 'ngram' }],
        } as MessageVerification;
        expect(citationSpanFor(v, 1)).toBeUndefined();
    });

    it('returns undefined for an unverified citation', () => {
        const v = {
            verified: true, score: 0, issues: [],
            citations: [{ n: 1, verified: false, reason: 'no_overlap' }],
        } as MessageVerification;
        expect(citationSpanFor(v, 1)).toBeUndefined();
    });

    it('returns undefined when there is no matching citation number', () => {
        const v = {
            verified: true, score: 0, issues: [],
            citations: [{ n: 2, verified: true, method: 'span', span: { start: 0, end: 3 } }],
        } as MessageVerification;
        expect(citationSpanFor(v, 1)).toBeUndefined();
    });

    it('returns undefined for null/undefined verification or missing citations', () => {
        expect(citationSpanFor(null, 1)).toBeUndefined();
        expect(citationSpanFor(undefined, 1)).toBeUndefined();
        expect(citationSpanFor({ verified: true, score: 0, issues: [] } as MessageVerification, 1)).toBeUndefined();
    });
});

describe('excerptAroundSpan', () => {
    // Leading emoji is one astral code point (2 UTF-16 code units) BEFORE the
    // span, so a UTF-16-indexed slice and a rune-indexed slice disagree about
    // every offset that follows it. Mutation: replace the `Array.from`
    // slicing in excerptAroundSpan with plain `content.slice` — this fixture
    // must then fail because the quote comes out as " zitiert" instead of
    // "zitierte".
    const content = '😀 Der zitierte Aussage über wichtige Sache.';
    const span = { start: 6, end: 14 }; // rune offsets → "zitierte"

    it('is rune-safe: extracts the correct quote across an astral prefix', () => {
        const result = excerptAroundSpan(content, span);
        expect(result.quote).toBe('zitierte');
        expect(result.quote).toBe(Array.from(content).slice(span.start, span.end).join(''));
    });

    it('includes up to `radius` chars of context on each side without ellipsis when short', () => {
        const result = excerptAroundSpan(content, span, 160);
        const runes = Array.from(content);
        expect(result.before).toBe(runes.slice(0, span.start).join(''));
        expect(result.after).toBe(runes.slice(span.end).join(''));
    });

    it('truncates context to `radius` runes per side and adds an ellipsis', () => {
        const long = 'x'.repeat(500) + 'MITTE' + 'y'.repeat(500);
        const longSpan = { start: 500, end: 505 };
        const result = excerptAroundSpan(long, longSpan, 160);

        expect(result.quote).toBe('MITTE');
        expect(result.before.startsWith('…')).toBe(true);
        expect(result.before.length).toBe(161); // ellipsis + 160 runes of context
        expect(result.after.endsWith('…')).toBe(true);
        expect(result.after.length).toBe(161);
    });
});
