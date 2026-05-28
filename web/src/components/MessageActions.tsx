import { useState } from 'react';
import { Pencil, GitBranch, Columns, RefreshCw, ThumbsUp, ThumbsDown, ExternalLink } from 'lucide-react';
import type { Message } from '../types';
import { HAPTIC_PATTERNS, triggerHaptic } from '../utils/haptics';
import { openExternal } from '../utils/openExternal';
import { useTheme } from '../contexts/ThemeContext';
import { useAuth } from '../contexts/AuthContext';

interface MessageActionsProps {
    message: Message;
    onEdit?: () => void;
    onFork?: () => void;
    onCompare?: () => void;
    onRegenerate?: () => void;
    onFeedback?: (feedback: 'positive' | 'negative' | null, comment?: string) => void;
    position?: 'top' | 'bottom';
    isMobile?: boolean;
}

const MAX_COMMENT_LEN = 2000;

interface FeedbackThumbsProps {
    message: Message;
    onFeedback: (feedback: 'positive' | 'negative' | null, comment?: string) => void;
    buttonStyle: React.CSSProperties;
    iconSize: number;
    t: (key: string) => string;
}

function FeedbackThumbs({ message, onFeedback, buttonStyle, iconSize, t }: FeedbackThumbsProps) {
    const [pendingRating, setPendingRating] = useState<'positive' | 'negative' | null>(null);
    const [comment, setComment] = useState('');

    const submitWithComment = () => {
        if (pendingRating === null) return;
        const trimmed = comment.trim();
        onFeedback(pendingRating, trimmed.length > 0 ? trimmed : undefined);
        setPendingRating(null);
        setComment('');
    };

    const handleThumbClick = (rating: 'positive' | 'negative') => {
        // Toggle off if same rating already active.
        if (message.feedback === rating) {
            onFeedback(null);
            setPendingRating(null);
            setComment('');
            return;
        }
        // First click: send the rating immediately AND open the comment box.
        triggerHaptic(HAPTIC_PATTERNS.action);
        onFeedback(rating);
        setPendingRating(rating);
        setComment('');
    };

    return (
        <>
            <button
                type="button"
                onClick={(e) => { e.stopPropagation(); handleThumbClick('positive'); }}
                style={{
                    ...buttonStyle,
                    color: message.feedback === 'positive' ? 'var(--success-text, #2d8f4e)' : 'var(--text-secondary)',
                }}
                title={t('helpfulAnswer')}
                aria-label={t('helpfulAnswer')}
                onMouseEnter={(e) => { if (message.feedback !== 'positive') { e.currentTarget.style.color = 'var(--success-text, #2d8f4e)'; e.currentTarget.style.background = 'var(--tag-bg)'; }}}
                onMouseLeave={(e) => { if (message.feedback !== 'positive') { e.currentTarget.style.color = 'var(--text-secondary)'; e.currentTarget.style.background = 'none'; }}}
            >
                <ThumbsUp size={iconSize} />
            </button>
            <button
                type="button"
                onClick={(e) => { e.stopPropagation(); handleThumbClick('negative'); }}
                style={{
                    ...buttonStyle,
                    color: message.feedback === 'negative' ? 'var(--error-text, #c0392b)' : 'var(--text-secondary)',
                }}
                title={t('unhelpfulAnswer')}
                aria-label={t('unhelpfulAnswer')}
                onMouseEnter={(e) => { if (message.feedback !== 'negative') { e.currentTarget.style.color = 'var(--error-text, #c0392b)'; e.currentTarget.style.background = 'var(--tag-bg)'; }}}
                onMouseLeave={(e) => { if (message.feedback !== 'negative') { e.currentTarget.style.color = 'var(--text-secondary)'; e.currentTarget.style.background = 'none'; }}}
            >
                <ThumbsDown size={iconSize} />
            </button>

            {pendingRating !== null && (
                // eslint-disable-next-line jsx-a11y/click-events-have-key-events, jsx-a11y/no-static-element-interactions
                <div
                    onClick={(e) => e.stopPropagation()}
                    style={{
                        position: 'absolute',
                        top: '32px',
                        right: 0,
                        background: 'var(--bg-primary)',
                        border: '1px solid var(--border-color)',
                        borderRadius: '6px',
                        padding: '8px',
                        boxShadow: 'var(--shadow-md)',
                        zIndex: 20,
                        width: '280px',
                    }}
                >
                    {/* eslint-disable-next-line jsx-a11y/no-autofocus */}
                    <textarea autoFocus
                        value={comment}
                        onChange={(e) => setComment(e.target.value.slice(0, MAX_COMMENT_LEN))}
                        placeholder={t('feedbackCommentPlaceholder')}
                        rows={3}
                        style={{
                            width: '100%',
                            background: 'var(--bg-secondary)',
                            color: 'var(--text-primary)',
                            border: '1px solid var(--border-color)',
                            borderRadius: '4px',
                            padding: '4px 6px',
                            fontFamily: 'inherit',
                            fontSize: '0.85rem',
                            resize: 'vertical',
                        }}
                    />
                    <div style={{ display: 'flex', justifyContent: 'flex-end', marginTop: '4px', gap: '4px' }}>
                        <button
                            type="button"
                            onClick={submitWithComment}
                            disabled={comment.trim() === ''}
                            style={{
                                padding: '4px 10px',
                                background: 'var(--accent-primary)',
                                color: 'white',
                                border: 'none',
                                borderRadius: '4px',
                                cursor: comment.trim() === '' ? 'not-allowed' : 'pointer',
                                opacity: comment.trim() === '' ? 0.5 : 1,
                                fontSize: '0.85rem',
                            }}
                        >
                            {t('feedbackCommentSubmit')}
                        </button>
                    </div>
                </div>
            )}
        </>
    );
}

export function MessageActions({ message, onEdit, onFork, onCompare, onRegenerate, onFeedback, position = 'top', isMobile }: MessageActionsProps) {
    const { t } = useTheme();
    const { user, siteConfigs } = useAuth();
    const isUser = message.role === 'user';
    const iconSize = isMobile ? 18 : 14;

    const buttonStyle: React.CSSProperties = {
        background: 'none',
        border: 'none',
        padding: isMobile ? '8px' : '4px',
        cursor: 'pointer',
        color: 'var(--text-secondary)',
        borderRadius: '4px',
        display: 'flex',
        alignItems: 'center',
        transition: 'color 0.15s, background 0.15s',
        minWidth: isMobile ? '44px' : undefined,
        minHeight: isMobile ? '44px' : undefined,
        justifyContent: isMobile ? 'center' : undefined,
    };

    return (
        <div style={{
            position: isMobile ? 'relative' : 'absolute',
            ...(isMobile ? { marginTop: '4px' } : (position === 'top' ? { top: '-8px' } : { bottom: '-8px' })),
            right: !isMobile && isUser ? '0' : undefined,
            left: !isMobile && isUser ? undefined : '0',
            display: 'flex',
            gap: '2px',
            background: 'var(--bg-primary)',
            border: '1px solid var(--border-color)',
            borderRadius: '6px',
            padding: '2px',
            boxShadow: 'var(--shadow-sm)',
            zIndex: 10,
        }}>
            {isUser && onEdit && (
                <button
                    type="button"
                    onClick={(e) => { e.stopPropagation(); triggerHaptic(HAPTIC_PATTERNS.action); onEdit(); }}
                    style={buttonStyle}
                    title={t('editMessage')}
                    aria-label={t('editMessage')}
                    onMouseEnter={(e) => { e.currentTarget.style.color = 'var(--accent-primary)'; e.currentTarget.style.background = 'var(--tag-bg)'; }}
                    onMouseLeave={(e) => { e.currentTarget.style.color = 'var(--text-secondary)'; e.currentTarget.style.background = 'none'; }}
                >
                    <Pencil size={iconSize} />
                </button>
            )}
            {!isUser && onFork && (
                <button
                    type="button"
                    onClick={(e) => { e.stopPropagation(); triggerHaptic(HAPTIC_PATTERNS.action); onFork(); }}
                    style={buttonStyle}
                    title={t('forkFromHere')}
                    aria-label={t('forkFromHere')}
                    onMouseEnter={(e) => { e.currentTarget.style.color = 'var(--accent-primary)'; e.currentTarget.style.background = 'var(--tag-bg)'; }}
                    onMouseLeave={(e) => { e.currentTarget.style.color = 'var(--text-secondary)'; e.currentTarget.style.background = 'none'; }}
                >
                    <GitBranch size={iconSize} />
                </button>
            )}
            {!isUser && onRegenerate && (
                <button
                    type="button"
                    onClick={(e) => { e.stopPropagation(); triggerHaptic(HAPTIC_PATTERNS.action); onRegenerate(); }}
                    style={buttonStyle}
                    title={t('regenerateResponse')}
                    aria-label={t('regenerateResponse')}
                    onMouseEnter={(e) => { e.currentTarget.style.color = 'var(--accent-primary)'; e.currentTarget.style.background = 'var(--tag-bg)'; }}
                    onMouseLeave={(e) => { e.currentTarget.style.color = 'var(--text-secondary)'; e.currentTarget.style.background = 'none'; }}
                >
                    <RefreshCw size={iconSize} />
                </button>
            )}
            {onCompare && (
                <button
                    type="button"
                    onClick={(e) => { e.stopPropagation(); triggerHaptic(HAPTIC_PATTERNS.action); onCompare(); }}
                    style={buttonStyle}
                    title={t('compareBranches')}
                    aria-label={t('compareBranches')}
                    onMouseEnter={(e) => { e.currentTarget.style.color = 'var(--accent-primary)'; e.currentTarget.style.background = 'var(--tag-bg)'; }}
                    onMouseLeave={(e) => { e.currentTarget.style.color = 'var(--text-secondary)'; e.currentTarget.style.background = 'none'; }}
                >
                    <Columns size={iconSize} />
                </button>
            )}
            {!isUser && onFeedback && (
                <FeedbackThumbs
                    message={message}
                    onFeedback={onFeedback}
                    buttonStyle={buttonStyle}
                    iconSize={iconSize}
                    t={t}
                />
            )}
            {/* View trace — admin-only, requires both message.traceId AND siteConfigs.langfuse_base_url */}
            {!isUser && message.traceId && (() => {
                const langfuseBaseUrl = siteConfigs.langfuse_base_url;
                const role = user?.role;
                if (!langfuseBaseUrl || (role !== 'admin' && role !== 'superadmin')) {
                    return null;
                }
                return (
                    <button
                        type="button"
                        onClick={(e) => {
                            e.stopPropagation();
                            triggerHaptic(HAPTIC_PATTERNS.action);
                            const base = langfuseBaseUrl.replace(/\/$/, '');
                            openExternal(`${base}/${message.traceId}`);
                        }}
                        style={buttonStyle}
                        title={t('viewTrace')}
                        aria-label={t('viewTrace')}
                        onMouseEnter={(e) => { e.currentTarget.style.color = 'var(--accent-primary)'; e.currentTarget.style.background = 'var(--tag-bg)'; }}
                        onMouseLeave={(e) => { e.currentTarget.style.color = 'var(--text-secondary)'; e.currentTarget.style.background = 'none'; }}
                    >
                        <ExternalLink size={iconSize} />
                    </button>
                );
            })()}
        </div>
    );
}
