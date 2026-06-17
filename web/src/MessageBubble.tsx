import { memo, useState, useMemo, useEffect, useRef, useCallback } from 'react';
import { motion } from 'framer-motion';
import type { Message, BranchInfo, MessageVerification } from './types';
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
    animationDelay?: number;
}

interface VerificationBadgeProps {
    verification: MessageVerification;
    t: (key: string) => string;
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

function VerificationBadge({ verification, t }: VerificationBadgeProps) {
    const { verified, score, issues } = verification;

    let label: string;
    let bgColor: string;
    let color: string;

    if (!verified) {
        label = t('verificationUnverified');
        bgColor = '#fee2e2';
        color = '#991b1b';
    } else if (score >= 80) {
        label = t('verificationVerified');
        bgColor = '#d1fae5';
        color = '#065f46';
    } else if (score >= 60) {
        label = t('verificationPartial');
        bgColor = '#fef3c7';
        color = '#92400e';
    } else {
        label = t('verificationLowConfidence');
        bgColor = '#ffedd5';
        color = '#9a3412';
    }

    const tooltipParts: string[] = [`${t('verificationScoreLabel')}: ${score}`];
    if (issues.length > 0) {
        tooltipParts.push(`${t('verificationIssuesLabel')}: ${issues.join('; ')}`);
    }
    const tooltip = tooltipParts.join(' | ');

    return (
        <span
            title={tooltip}
            style={{
                display: 'inline-block',
                padding: '2px 10px',
                borderRadius: '12px',
                fontSize: '0.75rem',
                fontWeight: 500,
                backgroundColor: bgColor,
                color: color,
                cursor: 'default',
                userSelect: 'none',
            }}
        >
            {label}
        </span>
    );
}

function MessageBubble({ message, isStreaming, onPdfOpen, onFollowUpClick, showFollowUps, branchInfo, onSwitchBranch, onEdit, onFork, onCompare, onRegenerate, onFeedback, isEditing, onEditCancel, onPreviewSource, animationDelay }: MessageBubbleProps) {
    const { t } = useTheme();
    const isThinking = Boolean(isStreaming && message.reasoning && !message.content);
    const isMobile = useIsMobileContext();
    const reducedMotion = useReducedMotion();
    const [isHovered, setIsHovered] = useState(false);
    const [isFocused, setIsFocused] = useState(false);
    const [tapped, setTapped] = useState(false);
    const bubbleRef = useRef<HTMLDivElement>(null);

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

    // Memoize source grouping to prevent recalculation on every render
    const sourceElements = useMemo(() => {
        if (!message.sources || message.sources.length === 0) return null;

        const cited = extractCitedSourceIndices(message.content, message.sources.length);
        const visibleSources = cited.size > 0
            ? message.sources.filter((_, i) => cited.has(i + 1))
            : message.sources;

        if (visibleSources.length === 0) return null;

        const grouped = new Map<string, { fileId?: string; pages: Set<number> }>();
        for (const s of visibleSources) {
            if (!s.fileName) continue;
            const entry = grouped.get(s.fileName) || { fileId: s.fileId, pages: new Set() };
            if (s.pages) s.pages.forEach(p => entry.pages.add(p));
            if (s.fileId) entry.fileId = s.fileId;
            grouped.set(s.fileName, entry);
        }

        return [...grouped.entries()].map(([name, { fileId, pages }], idx) => {
            const sortedPages = [...pages].sort((a, b) => a - b);
            const pageLabel = sortedPages.length > 0 ? `, S. ${formatPageRanges(sortedPages)}` : '';
            const isPdf = name.toLowerCase().endsWith('.pdf');
            const firstPage = sortedPages[0] || 1;

            if (isPdf && fileId && onPdfOpen) {
                return (
                    <span
                        key={`${name}-${idx}`}
                        className="source-tag source-tag-link"
                        role="button"
                        tabIndex={0}
                        aria-label={`Open ${name}`}
                        onClick={(e) => {
                            e.stopPropagation();
                            onPdfOpen(fileId, name, firstPage);
                        }}
                        onKeyDown={e => { if (e.key === 'Enter' || e.key === ' ') onPdfOpen(fileId, name, firstPage); }}
                    >
                        {name}{pageLabel}
                    </span>
                );
            } else if (onPreviewSource && fileId) {
                return (
                    <span
                        key={`${name}-${idx}`}
                        className="source-tag source-tag-link"
                        role="button"
                        tabIndex={0}
                        aria-label={`Preview ${name}`}
                        onClick={(e) => {
                            e.stopPropagation();
                            onPreviewSource(fileId, name);
                        }}
                        onKeyDown={e => { if (e.key === 'Enter' || e.key === ' ') onPreviewSource(fileId, name); }}
                    >
                        {name}{pageLabel}
                    </span>
                );
            }
            return <span key={`${name}-${idx}`} className="source-tag">{name}{pageLabel}</span>;
        });
    }, [message.sources, message.content, onPdfOpen, onPreviewSource]);

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
            {showActions && !isStreaming && !message.isEnhanced && message.id && (
                <MessageActions
                    message={message}
                    onEdit={onEdit ? () => onEdit(message.id!, message.content) : undefined}
                    onFork={onFork ? () => onFork(message.id!) : undefined}
                    onCompare={onCompare ? () => onCompare(message.id!) : undefined}
                    onRegenerate={onRegenerate ? () => onRegenerate(message.id!) : undefined}
                    onFeedback={message.role === 'ai' && onFeedback ? (fb, comment) => onFeedback(message.id!, fb, comment) : undefined}
                    isMobile={isMobile}
                />
            )}

            {message.role === 'ai' ? (
                // role+tabIndex (instead of <button>) because the message body
                // contains nested interactive elements of its own.
                <div role="button" tabIndex={0} onClick={handleCitationClick} onKeyDown={handleCitationKeyDown}>
                    <MessageContent
                        content={message.content}
                        reasoning={message.reasoning}
                        isThinking={isThinking}
                        sources={message.sources}
                        suspectCitations={suspectCitationMap(message.verification)}
                        semanticCitations={semanticCitationSet(message.verification)}
                        trajectory={message.trajectory}
                        flaggedClaims={flaggedClaimsFor(message.verification)}
                    />
                </div>
            ) : (
                message.content
            )}

            {message.role === 'ai' && message.comparisonFindings && message.comparisonFindings.length > 0 && (
                <ComparisonFindings findings={message.comparisonFindings} t={t} />
            )}

            {message.role === 'ai' && !isStreaming && message.verification != null && hasFactcheckOutput(message.verification) && (
                <div style={{ marginTop: '0.75rem' }}>
                    <VerificationBadge verification={message.verification} t={t} />
                </div>
            )}
            {sourceElements && (
                <div style={{ marginTop: '1rem' }}>
                    <div className="source-label" style={{ marginBottom: '0.5rem' }}>{t('sourcesLabel')}</div>
                    {sourceElements}
                </div>
            )}
            {showFollowUps && message.followUpQuestions && message.followUpQuestions.length > 0 && onFollowUpClick && (
                <div className="follow-up-suggestions">
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
            )}
            {showActions && !isStreaming && !message.isEnhanced && message.id && message.role === 'ai' && (
                <MessageActions
                    message={message}
                    onFork={onFork ? () => onFork(message.id!) : undefined}
                    onCompare={onCompare ? () => onCompare(message.id!) : undefined}
                    onRegenerate={onRegenerate ? () => onRegenerate(message.id!) : undefined}
                    onFeedback={onFeedback ? (fb, comment) => onFeedback(message.id!, fb, comment) : undefined}
                    position="bottom"
                    isMobile={isMobile}
                />
            )}
        </motion.div>
    );
}

export default memo(MessageBubble);
