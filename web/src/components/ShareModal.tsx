import React, { useState, useEffect, useCallback } from 'react';
import { X, User, Eye, Edit3, Loader2, Trash2, Clock, Users } from 'lucide-react';
import type { KnowledgeBase } from '../types';
import { motion } from 'framer-motion';
import axios from 'axios';
import { API_BASE_URL } from '../api';
import { useTheme } from '../contexts/ThemeContext';
import { useAuth } from '../contexts/AuthContext';
import { useModalContext } from '../contexts/ModalContext';
import { useToast } from '../contexts/ToastContext';
import { useReducedMotion, getMotionProps } from '../hooks/useReducedMotion';
import { useFormValidation } from '../hooks/useFormValidation';
import { splitUsernames } from '../utils/splitUsernames';

interface ShareModalProps {
    show: boolean;
    onClose: () => void;
    sharingKb: KnowledgeBase | null;
    shareUserId: string;
    setShareUserId: (id: string) => void;
    shareTargetUser: { id: string; username: string; firstName?: string; lastName?: string } | null;
    shareLoading: boolean;
    sharePermission: 'view' | 'edit';
    setSharePermission: (perm: 'view' | 'edit') => void;
    onLookupUser: () => void;
    onConfirmShare: () => void;
    /** Username the lookup could not resolve — offer a pending invite for it. */
    notFoundUsername: string | null;
    /** Called after a pending invite is created, so the parent can reset its lookup state. */
    onPendingInvited: () => void;
}

export const ShareModal: React.FC<ShareModalProps> = ({
    show, onClose, sharingKb, shareUserId, setShareUserId, shareTargetUser,
    shareLoading, sharePermission, setSharePermission, onLookupUser, onConfirmShare,
    notFoundUsername, onPendingInvited,
}) => {
    const { t } = useTheme();
    const { token } = useAuth();
    const { showConfirm } = useModalContext();
    const toast = useToast();
    const reducedMotion = useReducedMotion();
    const { errors, validate, clearError } = useFormValidation({
        username: (v) => !v.trim() && t('fieldRequired'),
    });
    const [sharedUsers, setSharedUsers] = useState<{ id: string; userId: string; username: string; firstName: string; lastName: string; permission: string }[]>([]);
    const [loadingShares, setLoadingShares] = useState(false);
    const [pendingInvites, setPendingInvites] = useState<{ username: string; permission: string; createdAt: string }[]>([]);
    const [mode, setMode] = useState<'single' | 'bulk'>('single');
    const [bulkText, setBulkText] = useState('');
    const [bulkPermission, setBulkPermission] = useState<'view' | 'edit'>('view');
    const [bulkLoading, setBulkLoading] = useState(false);
    const [bulkSummary, setBulkSummary] = useState<{ shared: string[]; pending: string[]; alreadyHadAccess: string[] } | null>(null);

    const fetchShares = useCallback(async () => {
        if (!sharingKb) return;
        setLoadingShares(true);
        try {
            if (!token) return;

            const res = await axios.get(`${API_BASE_URL}/api/kb/${sharingKb.id}/shares`);
            setSharedUsers(res.data.shares ?? []);
            setPendingInvites(res.data.pending ?? []);
        } catch (err: unknown) {
            console.error('Failed to fetch shares:', err);
            toast.error(t('sharesFetchError'));
        } finally {
            setLoadingShares(false);
        }
    }, [sharingKb, token, toast, t]);

    useEffect(() => {
        if (show && sharingKb) {
            fetchShares();
        }
    }, [show, sharingKb, fetchShares]);

    // Parent closes the modal on successful share, so the share list refreshes on re-open.
    const handleConfirmShare = async () => {
        await onConfirmShare();
    };

    const handleRemoveShare = async (userId: string) => {
        if (!sharingKb) return;
        if (!await showConfirm(t('confirmRemoveShare') || 'Are you sure you want to remove this user?')) return;

        if (!token) return;

        const prev = sharedUsers;
        setSharedUsers(sharedUsers.filter(u => u.userId !== userId));
        try {
            await axios.delete(`${API_BASE_URL}/api/kb/${sharingKb.id}/share/${userId}`);
        } catch {
            setSharedUsers(prev);
            toast.error(t('removeShareError'));
        }
    };

    const handleBulkInvite = async () => {
        if (!sharingKb) return;
        const usernames = splitUsernames(bulkText);
        if (usernames.length === 0) return;
        setBulkLoading(true);
        try {
            const res = await axios.post(`${API_BASE_URL}/api/kb/${sharingKb.id}/share/bulk`, {
                usernames,
                permission: bulkPermission,
            });
            setBulkSummary(res.data);
            setBulkText('');
            await fetchShares(); // refresh shares + pending lists
        } catch (err: unknown) {
            console.error('Bulk invite failed:', err);
            toast.error(t('bulkInviteError'));
        } finally {
            setBulkLoading(false);
        }
    };

    const handleRevokePending = async (username: string) => {
        if (!sharingKb) return;
        const prev = pendingInvites;
        setPendingInvites(pendingInvites.filter(p => p.username !== username));
        try {
            await axios.delete(`${API_BASE_URL}/api/kb/${sharingKb.id}/share/pending/${encodeURIComponent(username)}`);
        } catch {
            setPendingInvites(prev);
            toast.error(t('removeShareError'));
        }
    };

    const [pendingInviteLoading, setPendingInviteLoading] = useState(false);

    // Invite a username with no account yet. Reuses the bulk endpoint, which
    // already parks unknown usernames in pending_kb_invites; they are promoted
    // to a real share on the user's first login.
    const handleInviteAnyway = async () => {
        if (!sharingKb || !notFoundUsername) return;
        setPendingInviteLoading(true);
        try {
            await axios.post(`${API_BASE_URL}/api/kb/${sharingKb.id}/share/bulk`, {
                usernames: [notFoundUsername],
                permission: sharePermission,
            });
            await fetchShares(); // surface the new invite in the pending list below
            toast.success(t('inviteAnywaySuccess').replace('{username}', notFoundUsername));
            onPendingInvited();
        } catch (err: unknown) {
            console.error('Pending invite failed:', err);
            toast.error(t('bulkInviteError'));
        } finally {
            setPendingInviteLoading(false);
        }
    };

    if (!show) return null;

    return (
        <div className="modal-overlay" role="presentation" onClick={e => { if (e.target === e.currentTarget) onClose(); }}>
            <div className="modal-content" role="dialog" aria-modal="true" aria-labelledby="share-modal-title" style={{ maxWidth: '500px' }}>
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1.5rem' }}>
                    <h3 id="share-modal-title" style={{ margin: 0 }}>"{sharingKb?.name}" {t('shareKb')}</h3>
                    <button onClick={onClose} className="icon-button" aria-label={t('closeShareModal')}><X size={20} /></button>
                </div>

                <div style={{ display: 'flex', gap: '0.5rem', marginBottom: '1.25rem' }} role="tablist">
                    <button
                        role="tab"
                        aria-selected={mode === 'single'}
                        onClick={() => { setMode('single'); setBulkSummary(null); }}
                        className={mode === 'single' ? 'search-button' : 'secondary-button'}
                        style={{ flex: 1, padding: '0.6rem', display: 'flex', alignItems: 'center', justifyContent: 'center', gap: '0.4rem' }}
                    >
                        <User size={16} aria-hidden="true" /> {t('singleShare')}
                    </button>
                    <button
                        role="tab"
                        aria-selected={mode === 'bulk'}
                        onClick={() => { setMode('bulk'); setBulkSummary(null); }}
                        className={mode === 'bulk' ? 'search-button' : 'secondary-button'}
                        style={{ flex: 1, padding: '0.6rem', display: 'flex', alignItems: 'center', justifyContent: 'center', gap: '0.4rem' }}
                    >
                        <Users size={16} aria-hidden="true" /> {t('bulkInvite')}
                    </button>
                </div>

                {mode === 'single' && (<>
                <div className="input-group" style={{ marginBottom: '1.5rem' }}>
                    <label htmlFor="share-username" style={{ fontSize: '0.85rem', color: 'var(--text-secondary)', marginBottom: '0.5rem', display: 'block' }}>{t('enterRecipientUsername')}</label>
                    <div style={{ display: 'flex', gap: '0.5rem' }}>
                        <input
                            id="share-username"
                            type="text"
                            value={shareUserId}
                            onChange={e => { setShareUserId(e.target.value); clearError('username'); }}
                            placeholder={t('username')}
                            style={{ flex: 1, padding: '0.75rem', borderRadius: '8px', border: '1px solid var(--border-color)', background: 'var(--bg-primary)', color: 'var(--text-primary)' }}
                            onKeyDown={(e) => { if (e.key === 'Enter' && validate({ username: shareUserId })) onLookupUser(); }}
                        />
                        <button
                            onClick={() => { if (validate({ username: shareUserId })) onLookupUser(); }}
                            className="secondary-button"
                            style={{ padding: '0.75rem 1rem' }}
                            disabled={shareLoading}
                        >
                            {t('search')}
                        </button>
                    </div>
                    {errors.username && <span className="field-error" role="alert">{errors.username}</span>}
                </div>

                {notFoundUsername && !shareTargetUser && (
                    <div
                        role="status"
                        style={{
                            padding: '1rem',
                            background: 'var(--warning-bg)',
                            borderRadius: '12px',
                            marginBottom: '1.5rem',
                            border: '1px solid var(--warning-text)',
                        }}
                    >
                        <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem', marginBottom: '0.5rem' }}>
                            <Clock size={18} aria-hidden="true" />
                            <span style={{ fontWeight: 600, color: 'var(--text-primary)' }}>
                                {t('noAccountYet').replace('{username}', notFoundUsername)}
                            </span>
                        </div>
                        <p style={{ fontSize: '0.8rem', color: 'var(--text-secondary)', margin: '0 0 1rem 0' }}>
                            {t('pendingInviteHint')}
                        </p>
                        <div style={{ display: 'flex', gap: '0.75rem', marginBottom: '1rem' }} role="group" aria-label={t('selectPermission')}>
                            <button
                                onClick={() => setSharePermission('view')}
                                className={sharePermission === 'view' ? 'search-button' : 'secondary-button'}
                                style={{ flex: 1, padding: '0.6rem', display: 'flex', alignItems: 'center', justifyContent: 'center', gap: '0.5rem' }}
                                aria-pressed={sharePermission === 'view'}
                            >
                                <Eye size={16} aria-hidden="true" />
                                {t('viewPermission')}
                            </button>
                            <button
                                onClick={() => setSharePermission('edit')}
                                className={sharePermission === 'edit' ? 'search-button' : 'secondary-button'}
                                style={{ flex: 1, padding: '0.6rem', display: 'flex', alignItems: 'center', justifyContent: 'center', gap: '0.5rem' }}
                                aria-pressed={sharePermission === 'edit'}
                            >
                                <Edit3 size={16} aria-hidden="true" />
                                {t('editPermission')}
                            </button>
                        </div>
                        <button
                            onClick={handleInviteAnyway}
                            className="search-button"
                            style={{ width: '100%' }}
                            disabled={pendingInviteLoading}
                        >
                            {pendingInviteLoading ? <Loader2 className="animate-spin" size={18} /> : t('inviteAnyway')}
                        </button>
                    </div>
                )}

                {shareTargetUser && (
                    <motion.div
                        initial={{ opacity: 0, scale: 0.95 }}
                        animate={{ opacity: 1, scale: 1 }}
                        {...getMotionProps(reducedMotion)}
                        style={{
                            padding: '1rem',
                            background: 'var(--tag-bg)',
                            borderRadius: '12px',
                            marginBottom: '1.5rem',
                            border: '1px solid var(--accent-primary)',
                            display: 'flex',
                            alignItems: 'center',
                            gap: '1rem'
                        }}
                    >
                        <div style={{ padding: '0.5rem', background: 'var(--accent-primary)', borderRadius: '50%', color: 'white' }}>
                            <User size={24} aria-hidden="true" />
                        </div>
                        <div style={{ textAlign: 'left', flex: 1 }}>
                            <div style={{ fontWeight: 600, color: 'var(--text-primary)' }}>
                                {shareTargetUser.firstName} {shareTargetUser.lastName}
                            </div>
                            <div style={{ fontSize: '0.85rem', color: 'var(--text-secondary)' }}>
                                @{shareTargetUser.username}
                            </div>
                        </div>
                    </motion.div>
                )}

                {shareTargetUser && (
                    <div className="input-group" style={{ marginBottom: '1.5rem' }}>
                        <label style={{ fontSize: '0.85rem', color: 'var(--text-secondary)', marginBottom: '0.5rem', display: 'block' }}>
                            {t('selectPermission')}
                        </label>
                        <div style={{ display: 'flex', gap: '0.75rem' }} role="group" aria-label={t('selectPermission')}>
                            <button
                                onClick={() => setSharePermission('view')}
                                className={sharePermission === 'view' ? 'search-button' : 'secondary-button'}
                                style={{ flex: 1, padding: '0.75rem', display: 'flex', alignItems: 'center', justifyContent: 'center', gap: '0.5rem' }}
                                aria-pressed={sharePermission === 'view'}
                            >
                                <Eye size={18} aria-hidden="true" />
                                {t('viewPermission')}
                            </button>
                            <button
                                onClick={() => setSharePermission('edit')}
                                className={sharePermission === 'edit' ? 'search-button' : 'secondary-button'}
                                style={{ flex: 1, padding: '0.75rem', display: 'flex', alignItems: 'center', justifyContent: 'center', gap: '0.5rem' }}
                                aria-pressed={sharePermission === 'edit'}
                            >
                                <Edit3 size={18} aria-hidden="true" />
                                {t('editPermission')}
                            </button>
                        </div>
                        <p style={{ fontSize: '0.8rem', color: 'var(--text-secondary)', marginTop: '0.5rem' }}>
                            {sharePermission === 'view' ? t('viewPermissionDesc') : t('editPermissionDesc')}
                        </p>
                    </div>
                )}

                {shareTargetUser ? (
                    <div style={{ display: 'flex', gap: '1rem', marginBottom: sharedUsers.length > 0 ? '2rem' : 0 }}>
                        <button
                            onClick={handleConfirmShare}
                            className="search-button"
                            style={{ flex: 1 }}
                            disabled={shareLoading}
                        >
                            {shareLoading ? <Loader2 className="animate-spin" size={18} /> : t('confirmShare')}
                        </button>
                        <button onClick={() => { setShareUserId(''); onLookupUser(); }} className="secondary-button" style={{ flex: 1 }}>
                            {t('cancel')}
                        </button>
                    </div>
                ) : null}
                </>)}

                {mode === 'bulk' && (
                    <div className="input-group" style={{ marginBottom: '1.5rem' }}>
                        <label htmlFor="bulk-usernames" style={{ fontSize: '0.85rem', color: 'var(--text-secondary)', marginBottom: '0.5rem', display: 'block' }}>
                            {t('bulkInvite')}
                        </label>
                        <textarea
                            id="bulk-usernames"
                            aria-label={t('bulkInvite')}
                            value={bulkText}
                            onChange={e => setBulkText(e.target.value)}
                            placeholder={t('bulkInvitePaste')}
                            rows={5}
                            style={{ width: '100%', padding: '0.75rem', borderRadius: '8px', border: '1px solid var(--border-color)', background: 'var(--bg-primary)', color: 'var(--text-primary)', resize: 'vertical' }}
                        />
                        <div style={{ display: 'flex', gap: '0.75rem', margin: '0.75rem 0' }} role="group" aria-label={t('selectPermission')}>
                            <button onClick={() => setBulkPermission('view')} className={bulkPermission === 'view' ? 'search-button' : 'secondary-button'} style={{ flex: 1, padding: '0.6rem', display: 'flex', alignItems: 'center', justifyContent: 'center', gap: '0.4rem' }} aria-pressed={bulkPermission === 'view'}>
                                <Eye size={16} aria-hidden="true" /> {t('viewPermission')}
                            </button>
                            <button onClick={() => setBulkPermission('edit')} className={bulkPermission === 'edit' ? 'search-button' : 'secondary-button'} style={{ flex: 1, padding: '0.6rem', display: 'flex', alignItems: 'center', justifyContent: 'center', gap: '0.4rem' }} aria-pressed={bulkPermission === 'edit'}>
                                <Edit3 size={16} aria-hidden="true" /> {t('editPermission')}
                            </button>
                        </div>
                        <button onClick={handleBulkInvite} className="search-button" style={{ width: '100%' }} disabled={bulkLoading || splitUsernames(bulkText).length === 0}>
                            {bulkLoading ? <Loader2 className="animate-spin" size={18} /> : t('bulkInviteButton')}
                        </button>
                        {bulkSummary && (
                            <p style={{ fontSize: '0.8rem', color: 'var(--text-secondary)', marginTop: '0.75rem' }}>
                                {t('bulkInviteSummary')
                                    .replace('{shared}', String(bulkSummary.shared.length))
                                    .replace('{pending}', String(bulkSummary.pending.length))
                                    .replace('{already}', String(bulkSummary.alreadyHadAccess.length))}
                            </p>
                        )}
                    </div>
                )}

                {/* Divider if we have shared users */}
                {sharedUsers.length > 0 && (
                    <div style={{ marginTop: '1.5rem', paddingTop: '1.5rem', borderTop: '1px solid var(--border-color)' }}>
                        <h4 style={{ margin: '0 0 1rem 0', fontSize: '0.95rem', color: 'var(--text-secondary)' }}>
                            Shared with {loadingShares && <Loader2 className="animate-spin" size={14} style={{ display: 'inline', marginLeft: '8px' }} />}
                        </h4>

                        <div style={{ display: 'flex', flexDirection: 'column', gap: '0.75rem', maxHeight: '200px', overflowY: 'auto' }}>
                            {sharedUsers.map(user => (
                                <div key={user.userId} style={{
                                    display: 'flex',
                                    alignItems: 'center',
                                    justifyContent: 'space-between',
                                    padding: '0.75rem',
                                    background: 'var(--bg-secondary)',
                                    borderRadius: '8px',
                                    border: '1px solid var(--border-color)'
                                }}>
                                    <div style={{ display: 'flex', alignItems: 'center', gap: '0.75rem' }}>
                                        <div style={{
                                            padding: '0.4rem',
                                            background: 'var(--tag-bg)',
                                            borderRadius: '50%',
                                            color: 'var(--accent-primary)',
                                            display: 'flex', alignItems: 'center', justifyContent: 'center'
                                        }}>
                                            <User size={16} aria-hidden="true" />
                                        </div>
                                        <div>
                                            <div style={{ fontWeight: 500, fontSize: '0.9rem', color: 'var(--text-primary)' }}>
                                                {user.firstName} {user.lastName}
                                            </div>
                                            <div style={{ fontSize: '0.75rem', color: 'var(--text-secondary)' }}>
                                                @{user.username} • {user.permission === 'edit' ? t('editPermission') : t('viewPermission')}
                                            </div>
                                        </div>
                                    </div>
                                    <button
                                        onClick={() => handleRemoveShare(user.userId)}
                                        className="icon-button"
                                        style={{ color: 'var(--error-color)', padding: '6px' }}
                                        title={t('removeShare') || "Remove access"}
                                        aria-label={t('removeShare') || "Remove access"}
                                    >
                                        <Trash2 size={16} />
                                    </button>
                                </div>
                            ))}
                        </div>
                    </div>
                )}

                {pendingInvites.length > 0 && (
                    <div style={{ marginTop: '1.5rem', paddingTop: '1.5rem', borderTop: '1px solid var(--border-color)' }}>
                        <h4 style={{ margin: '0 0 0.5rem 0', fontSize: '0.95rem', color: 'var(--text-secondary)' }}>
                            {t('pendingInvites')}
                        </h4>
                        <p style={{ fontSize: '0.75rem', color: 'var(--text-secondary)', margin: '0 0 1rem 0' }}>{t('pendingInviteHint')}</p>
                        <div style={{ display: 'flex', flexDirection: 'column', gap: '0.5rem', maxHeight: '160px', overflowY: 'auto' }}>
                            {pendingInvites.map(p => (
                                <div key={p.username} style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', padding: '0.6rem 0.75rem', background: 'var(--bg-secondary)', borderRadius: '8px', border: '1px solid var(--border-color)' }}>
                                    <div style={{ display: 'flex', alignItems: 'center', gap: '0.6rem' }}>
                                        <Clock size={14} aria-hidden="true" style={{ color: 'var(--text-secondary)' }} />
                                        <span style={{ fontSize: '0.85rem', color: 'var(--text-primary)' }}>
                                            @{p.username} • {p.permission === 'edit' ? t('editPermission') : t('viewPermission')}
                                        </span>
                                    </div>
                                    <button onClick={() => handleRevokePending(p.username)} className="icon-button" style={{ color: 'var(--error-color)', padding: '6px' }} title={t('revokeInvite')} aria-label={t('revokeInvite')}>
                                        <Trash2 size={16} />
                                    </button>
                                </div>
                            ))}
                        </div>
                    </div>
                )}
            </div>
        </div>
    );
};
