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

// citationSpanFor returns the span-verifier's RUNE offsets for citation `n`,
// or undefined when the entry is missing, unverified, or was verified by a
// method that doesn't carry a span (ngram/semantic). The offsets index
// `sources[n-1].content` — see the CitationStatus doc comment in types.ts.
export function citationSpanFor(
    v: MessageVerification | null | undefined,
    n: number,
): { start: number; end: number } | undefined {
    const c = v?.citations?.find((entry) => entry.n === n);
    if (c && c.verified && c.span) return c.span;
    return undefined;
}

// SpanExcerpt is the windowed-context view of a citation span: up to
// `radius` runes of context before and after the quoted passage, each
// prefixed/suffixed with an ellipsis when the source content was truncated
// to fit. `quote` is exactly the cited span, unmodified.
export interface SpanExcerpt {
    before: string;
    quote: string;
    after: string;
}

// excerptAroundSpan builds a windowed excerpt around a citation span.
// `start`/`end` are RUNE offsets (Unicode code points, end exclusive) —
// content MUST be split with Array.from (never content.slice) because JS
// strings index UTF-16 code units and the offsets come from a Go backend
// that counts runes. Splitting the wrong way silently shifts every offset
// that follows an astral character (emoji, some CJK) earlier in the string.
export function excerptAroundSpan(
    content: string,
    span: { start: number; end: number },
    radius = 160,
): SpanExcerpt {
    const runes = Array.from(content);
    const start = Math.max(0, Math.min(span.start, runes.length));
    const end = Math.max(start, Math.min(span.end, runes.length));

    const beforeStart = Math.max(0, start - radius);
    const afterEnd = Math.min(runes.length, end + radius);

    const before = (beforeStart > 0 ? '…' : '') + runes.slice(beforeStart, start).join('');
    const quote = runes.slice(start, end).join('');
    const after = runes.slice(end, afterEnd).join('') + (afterEnd < runes.length ? '…' : '');

    return { before, quote, after };
}
