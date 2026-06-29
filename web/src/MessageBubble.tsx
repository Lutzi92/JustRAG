import { memo, useState, useMemo, useEffect, useRef, useCallback } from 'react';
import { motion } from 'framer-motion';
import { FileText, ChevronDown, Check, ArrowRight } from 'lucide-react';
import type { Message, BranchInfo, MessageVerification, MessageSource } from './types';
import { flaggedClaimsFor } from './utils/verification';
import { useReducedMotion, getMotionProps } from './hooks/useReducedMotion';
import { BranchIndicator } from './components/BranchIndicator';
import { MessageActions } from './components/MessageActions';
import { InlineMessageEditor } from './components/InlineMessageEditor';
import MessageContent from './components/MessageContent';
import { ComparisonFindings } from './components/ComparisonFindings';
import { useIsMobileContext } from './contexts/MobileContext';
import { useTheme } from './contexts/ThemeContext';
import { HAPTIC_PATTERNS, triggerHaptic } from './utils/haptics';
import { extractCitedSourceIndices, formatPageRanges } from './utils/citations';

interface MessageBubbleProps {
    message: Message;
    isStreaming?: boolean;
    onPdfOpen?: (fileId: string, fileName: string, page: number) => void;
    onFollowUpClick?: (question: string) => void;
    showFollowUps?: boolean;
    branchInfo?: BranchInfo | null;
    onSwitchBranch?: (siblingId: string) => void;
    onEdit?: (messageId: string, newContent: string) => void;
    onFork?: (messageId: string) => void;
    onCompare?: (messageId: string) => void;
    onRegenerate?: (messageId: string) => void;
    onFeedback?: (messageId: string, feedback: 'positive' | 'negative' | null, comment?: string) => void;
    isEditing?: boolean;
    onEditCancel?: () => void;
    onPreviewSource?: (fileId: string, fileName: string) => void;
    onViewGraph?: (messageId: string) => void;
    animationDelay?: number;
    kbId?: string;
    questionText?: string;
}

interface ConfidenceProps {
    verification: MessageVerification;
    t: (key: string) => string;
}

// confidenceColors maps a verification verdict to the citation-trust palette
// (shared with the old VerificationBadge): green ≥80, amber ≥60, orange <60,
// red unverified. Used by both the always-visible confidence chip and its
// expandable breakdown.
function confidenceColors(verification: MessageVerification): { bg: string; color: string; labelKey: string } {
    const { verified, score } = verification;
    if (!verified) return { bg: '#fee2e2', color: '#991b1b', labelKey: 'verificationUnverified' };
    if (score >= 80) return { bg: '#d1fae5', color: '#065f46', labelKey: 'verificationVerified' };
    if (score >= 60) return { bg: '#fef3c7', color: '#92400e', labelKey: 'verificationPartial' };
    return { bg: '#ffedd5', color: '#9a3412', labelKey: 'verificationLowConfidence' };
}

// hasFactcheckOutput reports whether the LLM-based factchecker actually
// produced a verdict on this message. The MessageVerification blob is shared
// with the deterministic citation validator, which leaves score=0 / issues=[]
// untouched when the factchecker didn't run. Without this gate, an answer
// that only carries citation-validator output would render a spurious red
// "Unverified" badge driven by the falsy factchecker defaults.
function hasFactcheckOutput(v: MessageVerification): boolean {
    return v.score > 0 || (v.issues?.length ?? 0) > 0;
}

// suspectCitationMap distills the n-gram validator output into the shape
// MessageContent expects: a Map of suspect citation N → reason string. Returns
// undefined when validation didn't run (so MessageContent skips the marker
// path entirely instead of decorating every citation).
function suspectCitationMap(v: MessageVerification | null | undefined): Map<number, string> | undefined {
    if (!v?.citations?.length) return undefined;
    const out = new Map<number, string>();
    for (const c of v.citations) {
        if (!c.verified && c.reason) {
            out.set(c.n, c.reason);
        }
    }
    return out.size > 0 ? out : undefined;
}

// semanticCitationSet collects the citation Ns whose verified=true result
// came from the cosine-similarity fallback rather than the n-gram fast
// path. The frontend renders these with a subtle solid underline (no
// warning glyph) — distinct from suspect (amber dashed + ⚠) and from
// ngram-verified (no decoration). Returns undefined when no semantic
// verifications exist so MessageContent can skip the marker path.
function semanticCitationSet(v: MessageVerification | null | undefined): Set<number> | undefined {
    if (!v?.citations?.length) return undefined;
    const out = new Set<number>();
    for (const c of v.citations) {
        if (c.verified && c.method === 'semantic') {
            out.add(c.n);
        }
    }
    return out.size > 0 ? out : undefined;
}

// ConfidenceChip is the always-visible answer-footer chip ("✓ Geprüft · 92%").
// Clicking it toggles the ConfidenceDetails breakdown (owned by MessageBubble).
function ConfidenceChip({ verification, t, open, onToggle }: ConfidenceProps & { open: boolean; onToggle: () => void }) {
    const { verified, score } = verification;
    const c = confidenceColors(verification);
    return (
        <button
            type="button"
            onClick={(e) => { e.stopPropagation(); onToggle(); }}
            aria-expanded={open}
            style={{
                display: 'inline-flex', alignItems: 'center', gap: '4px',
                padding: '2px 10px', borderRadius: '12px',
                fontSize: '0.75rem', fontWeight: 500,
                backgroundColor: c.bg, color: c.color,
                border: 'none', cursor: 'pointer', userSelect: 'none', fontFamily: 'inherit',
            }}
        >
            <Check size={12} aria-hidden="true" />
            <span>{t(c.labelKey)}{verified ? ` · ${score}%` : ''}</span>
            <ChevronDown size={12} aria-hidden="true" style={{ transition: 'transform 0.15s', transform: open ? 'rotate(180deg)' : 'none' }} />
        </button>
    );
}

// ConfidenceDetails is the expandable fact-check breakdown: a score bar plus
// per-method counts (wortgenau / semantisch / Hinweise) derived from the
// citation validator + factuality verifier output.
function ConfidenceDetails({ verification, t }: ConfidenceProps) {
    const { score, issues, citations } = verification;
    const c = confidenceColors(verification);
    const exact = citations?.filter(x => x.verified && x.method !== 'semantic').length ?? 0;
    const semantic = citations?.filter(x => x.verified && x.method === 'semantic').length ?? 0;
    const hints = (issues?.length ?? 0) + (citations?.filter(x => !x.verified).length ?? 0);
    return (
        <div style={{
            marginTop: '0.5rem', padding: '0.75rem',
            background: 'var(--bg-secondary)', border: '1px solid var(--border-color)',
            borderRadius: '8px', maxWidth: '420px',
        }}>
            <div style={{ height: 6, borderRadius: 3, background: 'var(--border-color)', overflow: 'hidden', marginBottom: '0.6rem' }}>
                <div style={{ height: '100%', width: `${Math.max(0, Math.min(100, score))}%`, background: c.color, borderRadius: 3, transition: 'width 0.3s' }} />
            </div>
            <div style={{ display: 'flex', gap: '1rem', flexWrap: 'wrap', fontSize: '0.78rem', color: 'var(--text-secondary)' }}>
                <span><strong style={{ color: 'var(--text-primary)' }}>{exact}</strong> {t('confidenceExactMatch')}</span>
                <span><strong style={{ color: 'var(--text-primary)' }}>{semantic}</strong> {t('confidenceSemantic')}</span>
                <span><strong style={{ color: 'var(--text-primary)' }}>{hints}</strong> {t('confidenceHints')}</span>
            </div>
            {issues && issues.length > 0 && (
                <ul style={{ margin: '0.5rem 0 0', paddingLeft: '1.1rem', fontSize: '0.76rem', color: 'var(--text-secondary)' }}>
                    {issues.map((iss, i) => <li key={i}>{iss}</li>)}
                </ul>
            )}
        </div>
    );
}

function MessageBubble({ message, isStreaming, onPdfOpen, onFollowUpClick, showFollowUps, branchInfo, onSwitchBranch, onEdit, onFork, onCompare, onRegenerate, onFeedback, isEditing, onEditCancel, onPreviewSource, onViewGraph, animationDelay, kbId, questionText }: MessageBubbleProps) {
    const { t } = useTheme();
    const isThinking = Boolean(isStreaming && message.reasoning && !message.content);
    const isMobile = useIsMobileContext();
    const reducedMotion = useReducedMotion();
    const [isHovered, setIsHovered] = useState(false);
    const [isFocused, setIsFocused] = useState(false);
    const [tapped, setTapped] = useState(false);
    // §8 answer-footer expandable sections.
    const [confOpen, setConfOpen] = useState(false);
    const [sourcesExpanded, setSourcesExpanded] = useState(false);
    const bubbleRef = useRef<HTMLDivElement>(null);
    const contentRef = useRef<HTMLDivElement>(null);

    // Close actions on outside click (mobile)
    useEffect(() => {
        if (!isMobile || !tapped) return;
        const handler = (e: MouseEvent) => {
            if (bubbleRef.current && !bubbleRef.current.contains(e.target as Node)) {
                setTapped(false);
            }
        };
        document.addEventListener('click', handler, true);
        return () => document.removeEventListener('click', handler, true);
    }, [isMobile, tapped]);

    const showActions = isMobile ? tapped : (isHovered || isFocused);

    // Handle clicks on inline citation references [1], [2], etc.
    const handleCitationClick = useCallback((e: React.MouseEvent | React.KeyboardEvent) => {
        const target = (e.target as HTMLElement).closest('.source-ref') as HTMLElement | null;
        if (!target || !message.sources?.length) return;

        const idx = parseInt(target.dataset.sourceIndex || '', 10);
        if (isNaN(idx) || idx < 1 || idx > message.sources.length) return;

        const source = message.sources[idx - 1];
        if (!source?.fileId) return;

        e.stopPropagation();
        const isPdf = source.fileName.toLowerCase().endsWith('.pdf');
        if (isPdf && onPdfOpen) {
            onPdfOpen(source.fileId, source.fileName, source.pages?.[0] || 1);
        } else if (onPreviewSource) {
            onPreviewSource(source.fileId, source.fileName);
        }
    }, [message.sources, onPdfOpen, onPreviewSource]);

    // Keyboard equivalent of the delegated citation click handler. Only acts
    // when the event originates from a citation ref, so Enter/Space on nested
    // interactive elements (links, buttons) keeps its default behavior.
    const handleCitationKeyDown = useCallback((e: React.KeyboardEvent) => {
        if (e.key !== 'Enter' && e.key !== ' ') return;
        if (!(e.target as HTMLElement).closest('.source-ref')) return;
        e.preventDefault();
        handleCitationClick(e);
    }, [handleCitationClick]);

    // openSourceByFile dispatches a citation/source open to the PDF viewer or
    // the text-preview handler, depending on file type.
    const openSourceByFile = useCallback((fileName: string, fileId?: string, firstPage = 1) => {
        if (!fileId) return;
        const isPdf = fileName.toLowerCase().endsWith('.pdf');
        if (isPdf && onPdfOpen) onPdfOpen(fileId, fileName, firstPage);
        else if (onPreviewSource) onPreviewSource(fileId, fileName);
    }, [onPdfOpen, onPreviewSource]);

    // handleOpenSource powers the §8 citation-preview "Im Dokument öffnen" link.
    const handleOpenSource = useCallback((source: MessageSource) => {
        openSourceByFile(source.fileName, source.fileId, source.pages?.[0] || 1);
    }, [openSourceByFile]);

    // Grouped, cited sources for the §8 answer footer (one entry per file, with
    // merged page ranges and a per-file match count). Replaces the old
    // "Quellen:" pill list with scannable source cards.
    const sourceGroups = useMemo(() => {
        if (!message.sources || message.sources.length === 0) return [];

        const cited = extractCitedSourceIndices(message.content, message.sources.length);
        const visibleSources = cited.size > 0
            ? message.sources.filter((_, i) => cited.has(i + 1))
            : message.sources;

        if (visibleSources.length === 0) return [];

        const grouped = new Map<string, { fileId?: string; pages: Set<number>; count: number }>();
        for (const s of visibleSources) {
            if (!s.fileName) continue;
            const entry = grouped.get(s.fileName) || { fileId: s.fileId, pages: new Set<number>(), count: 0 };
            if (s.pages) s.pages.forEach(p => entry.pages.add(p));
            if (s.fileId) entry.fileId = s.fileId;
            entry.count += 1;
            grouped.set(s.fileName, entry);
        }

        return [...grouped.entries()].map(([name, { fileId, pages, count }]) => ({
            name, fileId, pages: [...pages].sort((a, b) => a - b), count,
        }));
    }, [message.sources, message.content]);

    // If editing this message inline, show editor instead
    if (isEditing && message.role === 'user' && onEdit && onEditCancel && message.id) {
        return (
            <div style={{ alignSelf: 'flex-end', width: '100%', maxWidth: '85%' }}>
                {branchInfo && onSwitchBranch && (
                    <BranchIndicator branchInfo={branchInfo} onSwitchBranch={onSwitchBranch} />
                )}
                <InlineMessageEditor
                    initialContent={message.content}
                    onSave={(newContent) => onEdit(message.id!, newContent)}
                    onCancel={onEditCancel}
                />
            </div>
        );
    }

    return (
        <motion.div
            ref={bubbleRef}
            {...getMotionProps(reducedMotion)}
            initial={{ opacity: 0, y: 10, scale: 0.99 }}
            animate={{ opacity: 1, y: 0, scale: 1 }}
            transition={{ duration: 0.3, ease: "easeOut", delay: animationDelay ?? 0 }}
            className={`message-bubble ${message.role === 'user' ? 'message-user' : 'message-ai'}`}
            style={message.isEnhanced ? {
                background: 'var(--msg-enhanced-bg)',
                color: 'var(--msg-enhanced-text)',
                fontStyle: 'italic',
                fontSize: '0.9rem',
                border: `1px dashed var(--msg-enhanced-border)`,
                alignSelf: 'flex-end',
                borderRadius: '20px 20px 4px 20px',
                padding: '0.5rem 1rem'
            } : { position: 'relative' as const }}
            onMouseEnter={isMobile ? undefined : () => setIsHovered(true)}
            onMouseLeave={isMobile ? undefined : () => setIsHovered(false)}
            onClick={isMobile ? () => {
                setTapped(prev => {
                    const next = !prev;
                    if (next) {
                        triggerHaptic(HAPTIC_PATTERNS.reveal);
                    }
                    return next;
                });
            } : undefined}
            onFocus={isMobile ? undefined : () => setIsFocused(true)}
            onBlur={isMobile ? undefined : () => setIsFocused(false)}
            tabIndex={0}
        >
            {branchInfo && onSwitchBranch && (
                <BranchIndicator branchInfo={branchInfo} onSwitchBranch={onSwitchBranch} />
            )}
            {/* Top hover toolbar — user messages only (edit). AI actions live in the §8 footer row. */}
            {message.role === 'user' && showActions && !isStreaming && !message.isEnhanced && message.id && (
                <MessageActions
                    message={message}
                    onEdit={onEdit ? () => onEdit(message.id!, message.content) : undefined}
                    isMobile={isMobile}
                    kbId={kbId}
                    questionText={questionText}
                    contentRef={contentRef}
                />
            )}

            {message.role === 'ai' ? (
                // role+tabIndex (instead of <button>) because the message body
                // contains nested interactive elements of its own.
                <div ref={contentRef} role="button" tabIndex={0} onClick={handleCitationClick} onKeyDown={handleCitationKeyDown}>
                    <MessageContent
                        content={message.content}
                        reasoning={message.reasoning}
                        isThinking={isThinking}
                        sources={message.sources}
                        suspectCitations={suspectCitationMap(message.verification)}
                        semanticCitations={semanticCitationSet(message.verification)}
                        trajectory={message.trajectory}
                        flaggedClaims={flaggedClaimsFor(message.verification)}
                        messageId={message.id}
                        onViewGraph={onViewGraph}
                        onOpenSource={handleOpenSource}
                    />
                </div>
            ) : (
                message.content
            )}

            {message.role === 'ai' && message.comparisonFindings && message.comparisonFindings.length > 0 && (
                <ComparisonFindings findings={message.comparisonFindings} t={t} />
            )}

            {/* §8: single calm answer footer — confidence chip + Quellen·N toggle + one action row */}
            {message.role === 'ai' && !isStreaming && !message.isEnhanced && message.id && (
                <>
                    <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', flexWrap: 'wrap', marginTop: '1rem', paddingTop: '0.75rem', borderTop: '1px solid var(--border-color)' }}>
                        {message.verification != null && hasFactcheckOutput(message.verification) && (
                            <ConfidenceChip
                                verification={message.verification}
                                t={t}
                                open={confOpen}
                                onToggle={() => setConfOpen(o => !o)}
                            />
                        )}
                        {sourceGroups.length > 0 && (
                            <button
                                type="button"
                                onClick={(e) => { e.stopPropagation(); setSourcesExpanded(s => !s); }}
                                aria-expanded={sourcesExpanded}
                                style={{
                                    display: 'inline-flex', alignItems: 'center', gap: '4px',
                                    padding: '2px 10px', borderRadius: '12px',
                                    fontSize: '0.75rem', fontWeight: 600,
                                    background: 'var(--tag-bg)', color: 'var(--accent-primary)',
                                    border: 'none', cursor: 'pointer', fontFamily: 'inherit',
                                }}
                            >
                                <FileText size={13} aria-hidden="true" />
                                {t('answerSourcesToggle')} · {sourceGroups.length}
                                <ChevronDown size={12} aria-hidden="true" style={{ transition: 'transform 0.15s', transform: sourcesExpanded ? 'rotate(180deg)' : 'none' }} />
                            </button>
                        )}
                        <div style={{ marginLeft: 'auto' }}>
                            <MessageActions
                                message={message}
                                variant="inline"
                                onFork={onFork ? () => onFork(message.id!) : undefined}
                                onCompare={onCompare ? () => onCompare(message.id!) : undefined}
                                onRegenerate={onRegenerate ? () => onRegenerate(message.id!) : undefined}
                                onFeedback={onFeedback ? (fb, comment) => onFeedback(message.id!, fb, comment) : undefined}
                                isMobile={isMobile}
                                kbId={kbId}
                                questionText={questionText}
                                contentRef={contentRef}
                            />
                        </div>
                    </div>

                    {confOpen && message.verification != null && (
                        <ConfidenceDetails verification={message.verification} t={t} />
                    )}

                    {sourcesExpanded && sourceGroups.length > 0 && (
                        <div style={{ display: 'flex', flexDirection: 'column', gap: '0.5rem', marginTop: '0.5rem' }}>
                            {sourceGroups.map((g, i) => {
                                const isPdf = g.name.toLowerCase().endsWith('.pdf');
                                const pageLabel = g.pages.length ? `S. ${formatPageRanges(g.pages)} · ` : '';
                                const canOpen = !!g.fileId && (isPdf ? !!onPdfOpen : !!onPreviewSource);
                                return (
                                    <div key={`${g.name}-${i}`} style={{ display: 'flex', alignItems: 'center', gap: '0.6rem', padding: '0.6rem 0.75rem', background: 'var(--bg-secondary)', border: '1px solid var(--border-color)', borderRadius: '8px' }}>
                                        <FileText size={18} aria-hidden="true" style={{ flexShrink: 0, color: 'var(--accent-primary)' }} />
                                        <div style={{ minWidth: 0, flex: 1 }}>
                                            <div style={{ fontWeight: 500, fontSize: '0.875rem', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{g.name}</div>
                                            <div style={{ fontSize: '0.75rem', color: 'var(--text-secondary)' }}>{pageLabel}{g.count} {t('hitsLabel')}</div>
                                        </div>
                                        {canOpen && (
                                            <button
                                                type="button"
                                                onClick={(e) => { e.stopPropagation(); openSourceByFile(g.name, g.fileId, g.pages[0] || 1); }}
                                                style={{ display: 'inline-flex', alignItems: 'center', gap: '3px', background: 'none', border: 'none', cursor: 'pointer', color: 'var(--accent-primary)', fontSize: '0.8rem', fontWeight: 600, fontFamily: 'inherit', flexShrink: 0 }}
                                            >
                                                {t('openInDocument')} <ArrowRight size={13} aria-hidden="true" />
                                            </button>
                                        )}
                                    </div>
                                );
                            })}
                        </div>
                    )}
                </>
            )}

            {showFollowUps && message.followUpQuestions && message.followUpQuestions.length > 0 && onFollowUpClick && (
                <div style={{ marginTop: '1rem' }}>
                    <div className="source-label" style={{ marginBottom: '0.5rem' }}>{t('followUpsLabel')}</div>
                    <div className="follow-up-suggestions" style={{ marginTop: 0, paddingTop: 0, borderTop: 'none' }}>
                        {message.followUpQuestions.map((q, idx) => (
                            <button
                                key={idx}
                                className="follow-up-chip"
                                onClick={(e) => {
                                    e.stopPropagation();
                                    onFollowUpClick(q);
                                }}
                            >
                                {q}
                            </button>
                        ))}
                    </div>
                </div>
            )}
        </motion.div>
    );
}

export default memo(MessageBubble);
