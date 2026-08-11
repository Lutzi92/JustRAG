import { memo, lazy, Suspense, useEffect, useMemo, useRef, useState, useCallback } from 'react';
import ReactMarkdown, { type ExtraProps } from 'react-markdown';
import remarkGfm from 'remark-gfm';
import rehypeRaw from 'rehype-raw';
import rehypeSanitize, { defaultSchema } from 'rehype-sanitize';
import { Brain, Loader2, FileText, ArrowRight } from 'lucide-react';
import { useTheme } from '../contexts/ThemeContext';
import type { MessageSource, TrajectoryEvent, FlaggedClaimStatus } from '../types';
import { formatPageRanges } from '../utils/citations';
import { AnchoredPopover } from './AnchoredPopover';
import { TrajectoryPanel } from './TrajectoryPanel';
import { MarkdownTable } from './MarkdownTable';

// Lazy load ChartRenderer
const ChartRenderer = lazy(() => import('./ChartRenderer'));

interface MessageContentProps {
    content: string;
    reasoning?: string;
    isThinking?: boolean;
    sources?: MessageSource[];
    /**
     * 1-based citation numbers the deterministic n-gram validator marked as
     * suspect. Each suspect [N] is rendered with a ⚠ marker and a tooltip.
     * Pass undefined or empty when validation didn't run — citations render
     * unchanged.
     */
    suspectCitations?: Map<number, string>; // n → reason ("no_overlap" / "out_of_range")
    /**
     * 1-based citation numbers verified by the semantic-similarity fallback
     * (method = "semantic"). Rendered with a subtle solid underline and a
     * tooltip explaining that the n-gram check failed but cosine similarity
     * cleared the threshold. Pass undefined or empty to render those
     * citations the same as ngram-verified (no badge).
     */
    semanticCitations?: Set<number>;
    /**
     * Streaming trajectory: one entry per orchestrator decision point. When
     * present and non-empty, a collapsible "Reasoning steps" panel is rendered
     * above the answer body. The list is intentionally append-only — the
     * useChatStream hook owns mutation.
     */
    trajectory?: TrajectoryEvent[];
    /**
     * Flagged claims from the Phase 3 §3.3 factuality verifier. Each
     * matching substring in the answer is wrapped with a suspect-style
     * amber dashed underline + tooltip explaining the reason. Empty or
     * undefined skips the highlight pass entirely.
     */
    flaggedClaims?: FlaggedClaimStatus[];
    /**
     * Opens the source behind a citation pill (PDF viewer or text preview).
     * Clicking an inline [N] pill opens a source-preview popover (file · page ·
     * cited passage snippet); this callback fires from the popover's
     * "Im Dokument öffnen →" button, never from the pill itself. Threaded from
     * MessageBubble, which owns the onPdfOpen / onPreviewSource dispatch.
     */
    onOpenSource?: (source: MessageSource) => void;
}

/**
 * CitationPreview renders the body of the source popover for an inline [N]
 * citation pill: file name, page label, the cited passage, and the link that
 * actually opens the document. Positioning, portalling, outside-click and
 * Escape dismissal all belong to the AnchoredPopover wrapper around it.
 *
 * The popover is click-triggered. It used to open on hover, which left touch
 * users with no way to preview a source at all — a tap went straight into the
 * document instead.
 */
function CitationPreview({ source, t, onOpenSource }: {
    source: MessageSource;
    t: (key: string) => string;
    onOpenSource?: (source: MessageSource) => void;
}) {
    const pageLabel = source.pages && source.pages.length > 0 ? `S. ${formatPageRanges(source.pages)}` : '';
    const snippet = source.content && source.content.length > 320 ? `${source.content.slice(0, 320)}…` : source.content;

    return (
        <div style={{ padding: '10px 12px' }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: '6px', marginBottom: '6px', color: 'var(--text-primary)', fontWeight: 600, fontSize: '0.82rem' }}>
                <FileText size={14} aria-hidden="true" style={{ flexShrink: 0, color: 'var(--accent-primary)' }} />
                <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{source.fileName}</span>
            </div>
            {pageLabel && (
                <div style={{ fontSize: '0.72rem', color: 'var(--text-secondary)', marginBottom: '6px' }}>{pageLabel}</div>
            )}
            {snippet && (
                <div style={{ fontSize: '0.8rem', color: 'var(--text-secondary)', lineHeight: 1.45, maxHeight: '7.5em', overflow: 'hidden' }}>
                    {snippet}
                </div>
            )}
            {onOpenSource && source.fileId && (
                <button
                    type="button"
                    onClick={() => onOpenSource(source)}
                    style={{
                        marginTop: '8px', display: 'inline-flex', alignItems: 'center', gap: '4px',
                        background: 'none', border: 'none', padding: 0, cursor: 'pointer',
                        color: 'var(--accent-primary)', fontSize: '0.8rem', fontWeight: 600, fontFamily: 'inherit',
                    }}
                >
                    {t('openInDocument')} <ArrowRight size={13} aria-hidden="true" />
                </button>
            )}
        </div>
    );
}

/**
 * Replace citation references [1], [2] etc. in markdown with styled superscript HTML.
 * Shows page numbers (e.g. [S. 12]) when source page info is available, falls back to [N].
 * Protects fenced code blocks and inline code spans from transformation.
 *
 * Suspect citations (per the n-gram validator) get a `data-suspect` attribute
 * and a ⚠ glyph appended; CSS in index.css colors them. The reason string is
 * surfaced via the `title` attribute for hover tooltips.
 */
function addCitationRefs(content: string, sources?: MessageSource[], suspect?: Map<number, string>, semantic?: Set<number>, language: 'de' | 'en' = 'de'): string {
    // Extract code blocks and inline code, replacing with collision-safe placeholders
    const preserved: string[] = [];
    const placeholder = (i: number) => `\x00CITE_PRESERVE_${i}\x00`;

    // Replace fenced code blocks first (``` ... ```), then inline code (` ... `)
    let safe = content.replace(/```[\s\S]*?```|`[^`\n]+`/g, (match) => {
        const idx = preserved.length;
        preserved.push(match);
        return placeholder(idx);
    });

    // Apply citation regex on the safe text. This handles three shapes:
    //   - single:     [1], [42]
    //   - grouped:    [1, 2, 3]  (rendered as separate adjacent links)
    //   - 3-digit:    [150]
    // and tolerates markdown-escaped brackets (`\[1\]`) plus inner whitespace
    // (`[ 1, 2 ]`). The optional `\\?` before each bracket handles the escape
    // form some LLMs intermittently emit when generating prose.
    //
    // This MUST stay in sync with extractCitedSourceIndices() in
    // utils/citations.ts, which drives the per-message source list. When the
    // two diverged, grouped citations like "[1, 2]" — common on follow-up
    // turns that synthesize across several chunks — listed their sources but
    // left the inline marker as plain text with no click handler.
    safe = safe.replace(
        /(?<![`[])\\?\[\s*(\d{1,3}(?:\s*,\s*\d{1,3})*)\s*\\?\](?!\()/g,
        (_match, group: string) => {
            // Collapse indices in this marker that resolve to the same file.
            // The model often cites several chunks of one document in a single
            // marker ([1, 4, 13]); one link per chunk just repeats the same
            // file. Keep the first index per file and merge the others' pages
            // into its label so no page reference is lost. Distinct files (and
            // any index whose source is unknown) are all kept, in order.
            const byFile = new Map<string, { num: number; pages: number[] }>();
            const order: string[] = [];
            group.split(',').forEach((part, i) => {
                const num = parseInt(part.trim(), 10);
                if (isNaN(num)) return;
                const src = sources?.[num - 1];
                // Unknown sources get a per-occurrence key so they're never merged.
                const key = src ? (src.fileId || src.fileName) : `\x00idx-${num}-${i}`;
                let entry = byFile.get(key);
                if (!entry) {
                    entry = { num, pages: [] };
                    byFile.set(key, entry);
                    order.push(key);
                }
                if (src?.pages) entry.pages.push(...src.pages);
            });
            return order
                .map((key) => {
                    const { num, pages } = byFile.get(key)!;
                    return renderCitation(num, pages, suspect, semantic, language);
                })
                .join('');
        }
    );

    // Restore preserved code blocks
    for (let i = 0; i < preserved.length; i++) {
        safe = safe.replace(placeholder(i), preserved[i]);
    }

    return safe;
}

// renderCitation builds the <sup> HTML for one citation link. `pages` is the
// merged page set for the file behind `num` (collapsed from same-file indices
// in the marker — see addCitationRefs); empty means no page info, so the label
// falls back to the bare index. Each distinct file in a marker emits one
// independently clickable <sup> carrying its own data-source-index.
// Attributes that make a citation pill a real control: it opens the source
// popover on click or Enter/Space, so it has to be reachable by keyboard and
// announced as a button, not as decorative superscript. Kept in one constant
// because all three pill variants (plain, suspect, semantic) need them, and
// each name has a matching entry in sanitizeSchema below.
const CITATION_TRIGGER_ATTRS = 'role="button" tabindex="0" aria-haspopup="dialog"';

function renderCitation(num: number, pages: number[], suspect: Map<number, string> | undefined, semantic: Set<number> | undefined, language: 'de' | 'en'): string {
    const numStr = String(num);
    const label = pages.length ? `S. ${formatPageRanges(pages)}` : numStr;

    const suspectReason = suspect?.get(num);
    if (suspectReason) {
        const title = language === 'en'
            ? (suspectReason === 'out_of_range'
                ? `Citation [${num}] points to a source that does not exist.`
                : `Citation [${num}] has no recognizable word overlap with the cited source.`)
            : (suspectReason === 'out_of_range'
                ? `Zitat [${num}] verweist auf eine nicht existierende Quelle.`
                : `Zitat [${num}] hat keine erkennbare Wortüberlappung mit der zitierten Quelle.`);
        return `<sup class="source-ref source-ref-suspect" ${CITATION_TRIGGER_ATTRS} data-source-index="${numStr}" data-suspect="${escapeAttr(suspectReason)}" title="${escapeAttr(title)}">[${label}]<span class="source-ref-warn" aria-hidden="true">⚠</span></sup>`;
    }

    if (semantic?.has(num)) {
        const title = language === 'en'
            ? `Citation [${num}] verified by semantic similarity (the wording differs from the source but the meaning matches).`
            : `Zitat [${num}] über semantische Ähnlichkeit verifiziert (der Wortlaut weicht von der Quelle ab, die Bedeutung stimmt überein).`;
        return `<sup class="source-ref source-ref-semantic" ${CITATION_TRIGGER_ATTRS} data-source-index="${numStr}" title="${escapeAttr(title)}">[${label}]</sup>`;
    }

    return `<sup class="source-ref" ${CITATION_TRIGGER_ATTRS} data-source-index="${numStr}">[${label}]</sup>`;
}

// escapeAttr keeps tooltip text safe to inline as an HTML attribute. The
// downstream rehype-sanitize would already strip dangerous attributes, but
// we still want the tooltip readable, not garbled.
function escapeAttr(s: string): string {
    return s.replace(/&/g, '&amp;').replace(/"/g, '&quot;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
}

// escapeRegex protects a literal claim string from being interpreted as
// regex metacharacters when we build the flagged-claim matcher.
function escapeRegex(s: string): string {
    return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

// addFlaggedClaimHighlights wraps every literal occurrence of a flagged
// claim text in <mark> with a suspect-styled className + reason tooltip.
// The match is case-insensitive on a word boundary; verbatim quotes
// emitted by the verifier almost always survive as substrings of the
// answer prose, so a simple substring match is enough.
//
// Code blocks and inline code are preserved (same protection pattern as
// addCitationRefs) so a flagged claim that happens to contain a
// backtick doesn't break syntax highlighting.
function addFlaggedClaimHighlights(content: string, flagged?: FlaggedClaimStatus[], language: 'de' | 'en' = 'de'): string {
    if (!flagged || flagged.length === 0) return content;

    const preserved: string[] = [];
    const placeholder = (i: number) => `\x00FACT_PRESERVE_${i}\x00`;
    let safe = content.replace(/```[\s\S]*?```|`[^`\n]+`/g, (match) => {
        const idx = preserved.length;
        preserved.push(match);
        return placeholder(idx);
    });

    for (const claim of flagged) {
        const text = claim.claim_text.trim();
        if (text.length < 4) continue; // sub-word matches are noise
        const re = new RegExp(escapeRegex(text), 'i');
        const reasonLabel: Record<FlaggedClaimStatus['reason'], { en: string; de: string }> = {
            unsupported: { en: 'unsupported by sources', de: 'durch Quellen nicht gestützt' },
            contradicted: { en: 'contradicted by sources', de: 'widerspricht den Quellen' },
            out_of_scope: { en: 'outside the scope of the sources', de: 'außerhalb des Themas der Quellen' },
        };
        const localized = reasonLabel[claim.reason] ?? reasonLabel.unsupported;
        const tooltip = language === 'en'
            ? `Factuality verifier: ${localized.en}.`
            : `Faktenprüfer: ${localized.de}.`;
        safe = safe.replace(re, (m) => `<mark class="claim-flagged" data-reason="${escapeAttr(claim.reason)}" title="${escapeAttr(tooltip)}">${m}<span class="claim-flagged-warn" aria-hidden="true"> ⚠</span></mark>`);
    }

    for (let i = 0; i < preserved.length; i++) {
        safe = safe.replace(placeholder(i), preserved[i]);
    }
    return safe;
}

// Sanitization schema: extend the default to allow citation <sup> elements with data attributes.
//
// rehype-sanitize (via hast-util-sanitize 6.x) matches allowlist entries
// against HAST property names, NOT raw HTML attribute names. HAST converts
// `data-source-index` → `dataSourceIndex`, `aria-hidden` → `ariaHidden`,
// `class` → `className`. Using the hyphenated form here silently strips the
// attribute, which breaks citation click-to-open (reads `dataset.sourceIndex`)
// and the suspect-citation tooltip. See hast-util-sanitize's default schema
// for precedent (e.g. `section: ['dataFootnotes', ...]`).
//
// The same trap applies to the pill's control attributes: `ariaHasPopup` and
// `tabIndex` must be spelled the HAST way or the pill silently drops out of
// the tab order and stops announcing itself as a button.
const sanitizeSchema = {
    ...defaultSchema,
    tagNames: [...(defaultSchema.tagNames || []), 'sup', 'sub', 'details', 'summary', 'mark'],
    attributes: {
        ...defaultSchema.attributes,
        sup: ['className', 'dataSourceIndex', 'dataSuspect', 'title', 'role', 'tabIndex', 'ariaHasPopup', 'ariaExpanded'],
        mark: ['className', 'dataReason', 'title'],
        span: ['className', 'ariaHidden'],
        code: ['className'],
        img: ['src', 'alt', 'loading'],
    },
};

// Optimization: Define static configuration outside component to prevent unnecessary re-renders
const REMARK_PLUGINS = [remarkGfm];
const REHYPE_PLUGINS: import('unified').PluggableList = [rehypeRaw, [rehypeSanitize, sanitizeSchema]];

function buildMarkdownComponents(language: 'de' | 'en') {
    const loadingChartLabel = language === 'en' ? 'Loading chart...' : 'Lade Diagramm...';
    const downloadXlsxLabel = language === 'en' ? 'Download .xlsx' : 'Als Excel (.xlsx) herunterladen';
    return {
        table: (tableProps: React.ComponentProps<'table'> & ExtraProps) => {
            const { node, children } = tableProps;
            return <MarkdownTable node={node} label={downloadXlsxLabel} filename="table.xlsx">{children}</MarkdownTable>;
        },
        img: (imgProps: React.ComponentProps<'img'> & ExtraProps) => {
            // Strip the react-markdown AST `node` so it isn't spread onto the DOM element.
            const { node, ...props } = imgProps;
            void node;
            return <img {...props} alt={props.alt || ''} loading="lazy" style={{ maxWidth: '100%', height: 'auto' }} />;
        },
        code({ inline, className, children, ...props }: React.HTMLAttributes<HTMLElement> & { inline?: boolean; node?: unknown }) {
            const match = /language-(\w+)/.exec(className || '');
            return !inline && match ? (
                <Suspense fallback={
                    <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', padding: '2rem', color: 'var(--text-secondary)' }}>
                        <Loader2 className="animate-spin" size={24} />
                        <span style={{ marginLeft: '8px' }}>{loadingChartLabel}</span>
                    </div>
                }>
                    <ChartRenderer
                        content={String(children).replace(/\n$/, '')}
                        language={match[1]}
                        {...props}
                    />
                </Suspense>
            ) : (
                <code className={className} {...props}>
                    {children}
                </code>
            );
        }
    };
}

const MessageContent = memo(({ content, reasoning, isThinking, sources, suspectCitations, semanticCitations, trajectory, flaggedClaims, onOpenSource }: MessageContentProps) => {
    const { language, t } = useTheme();
    const reasoningLabel = language === 'en' ? 'Chain of Thought' : 'Gedankengang';
    const markdownComponents = useMemo(() => buildMarkdownComponents(language), [language]);

    // Click-triggered source-preview popover (§8): a delegated click on the
    // answer body detects an inline [N] pill and anchors the preview to it. The
    // pill is raw HTML (rehype-raw), so the anchor is the live DOM node held in
    // a ref rather than a React ref on a component.
    //
    // Clicking a pill used to open the document immediately, with the preview
    // reachable only by hovering. The preview is now the click target and the
    // document opens from inside it — one path instead of two, and the only
    // path that works on touch.
    const [openCitation, setOpenCitation] = useState<number | null>(null);
    const citationTriggerRef = useRef<HTMLElement | null>(null);

    const closePopover = useCallback(() => setOpenCitation(null), []);

    // activateCitation toggles the popover for the pill the event came from.
    // Returns true when the event hit a pill, so the caller knows whether to
    // swallow it.
    const activateCitation = useCallback((target: EventTarget | null) => {
        const sup = (target as HTMLElement | null)?.closest?.('.source-ref') as HTMLElement | null;
        if (!sup || !sources?.length) return false;
        const idx = parseInt(sup.dataset.sourceIndex || '', 10);
        if (Number.isNaN(idx) || idx < 1 || idx > sources.length) return false;

        // Clicking the open pill closes it; clicking another switches to it.
        const sameTrigger = citationTriggerRef.current === sup;
        citationTriggerRef.current = sup;
        setOpenCitation((prev) => (sameTrigger && prev === idx ? null : idx));
        return true;
    }, [sources]);

    const handleBodyClick = useCallback((e: React.MouseEvent) => {
        if (activateCitation(e.target)) e.stopPropagation();
    }, [activateCitation]);

    const handleBodyKeyDown = useCallback((e: React.KeyboardEvent) => {
        if (e.key !== 'Enter' && e.key !== ' ') return;
        if (!(e.target as HTMLElement).closest('.source-ref')) return;
        // Space would scroll the page, Enter could submit an enclosing form.
        e.preventDefault();
        if (activateCitation(e.target)) e.stopPropagation();
    }, [activateCitation]);

    // Reflect the open state on the trigger, and hand focus back to it when the
    // popover is dismissed from the keyboard. Focus is only restored when it
    // would otherwise be lost inside the closing popover — returning it after an
    // outside click would yank the caret away from whatever the user clicked.
    useEffect(() => {
        const trigger = citationTriggerRef.current;
        if (!trigger) return;
        if (openCitation == null) return;
        trigger.setAttribute('aria-expanded', 'true');
        return () => {
            trigger.removeAttribute('aria-expanded');
            if (document.activeElement === document.body || !document.body.contains(document.activeElement)) {
                trigger.focus?.();
            }
        };
    }, [openCitation]);

    const openSource = openCitation != null ? sources?.[openCitation - 1] : undefined;

    return (
        <>
            {trajectory && trajectory.length > 0 && (
                <TrajectoryPanel trajectory={trajectory} language={language} />
            )}
            {reasoning && (
                <details open={isThinking || undefined} style={{
                    marginBottom: '1rem',
                    background: 'var(--tag-bg)',
                    borderRadius: '8px',
                    padding: '0.5rem',
                    border: '1px solid var(--border-color)'
                }}>
                    <summary style={{
                        cursor: 'pointer',
                        fontSize: '0.8rem',
                        fontWeight: 600,
                        color: 'var(--accent-primary)',
                        display: 'flex',
                        alignItems: 'center',
                        gap: '8px',
                        listStyle: 'none'
                    }}>
                        <div style={{ display: 'flex', alignItems: 'center', gap: '4px' }}>
                            <Brain
                                size={16}
                                style={isThinking ? { animation: 'reasoning-pulse 1.5s ease-in-out infinite' } : undefined}
                            />
                            <span>{reasoningLabel}</span>
                        </div>
                    </summary>
                    <div className="markdown-content" style={{
                        marginTop: '0.5rem',
                        fontSize: '0.85rem',
                        color: 'var(--text-secondary)',
                        lineHeight: '1.5',
                        borderLeft: '2px solid var(--accent-primary)',
                        paddingLeft: '0.75rem',
                        paddingBottom: '0.25rem'
                    }}>
                        <ReactMarkdown remarkPlugins={REMARK_PLUGINS} rehypePlugins={REHYPE_PLUGINS} components={markdownComponents}>
                            {reasoning}
                        </ReactMarkdown>
                        {isThinking && (
                            <span style={{ animation: 'reasoning-cursor-blink 1s step-end infinite' }}>|</span>
                        )}
                    </div>
                </details>
            )}
            {/* The handlers here are pure event delegation: citation pills are raw
                HTML injected by rehype-raw, so no React handler can be attached to
                them directly. The pills themselves carry the interactive semantics
                (role="button", tabindex, Enter/Space), and this wrapper is marked
                presentational so it adds none of its own. */}
            <div
                className="markdown-content"
                role="presentation"
                onClick={handleBodyClick}
                onKeyDown={handleBodyKeyDown}
            >
                <ReactMarkdown
                    remarkPlugins={REMARK_PLUGINS}
                    rehypePlugins={REHYPE_PLUGINS}
                    components={markdownComponents}
                >
                    {addFlaggedClaimHighlights(addCitationRefs(content, sources, suspectCitations, semanticCitations, language), flaggedClaims, language)}
                </ReactMarkdown>
            </div>
            {/* Keyed on the citation so switching pills remounts the popover.
                useAnchoredPosition measures in a layout effect keyed on `open`,
                and citationTriggerRef is a mutable ref — moving it to another
                pill would not re-measure on its own. A mouse switch happens to
                survive (the outside-click handler closes the popover first), but
                a keyboard switch keeps `open` true and would leave the popover
                parked at the previously clicked pill. */}
            <AnchoredPopover
                key={openCitation ?? 'closed'}
                open={openSource != null}
                triggerRef={citationTriggerRef}
                onClose={closePopover}
                placement="bottom"
                align="center"
                width={320}
                role="dialog"
                ariaLabel={openSource?.fileName}
            >
                {openSource && (
                    <CitationPreview
                        source={openSource}
                        t={t}
                        onOpenSource={onOpenSource}
                    />
                )}
            </AnchoredPopover>
        </>
    );
});

export default MessageContent;
