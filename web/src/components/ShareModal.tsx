import React, { useState, useEffect, useCallback } from 'react';
import { X, User, Eye, Edit3, Loader2, Trash2 } from 'lucide-react';
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
}

export const ShareModal: React.FC<ShareModalProps> = ({
    show, onClose, sharingKb, shareUserId, setShareUserId, shareTargetUser,
    shareLoading, sharePermission, setSharePermission, onLookupUser, onConfirmShare,
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

    const fetchShares = useCallback(async () => {
        if (!sharingKb) return;
        setLoadingShares(true);
        try {
            if (!token) return;

            const res = await axios.get(`${API_BASE_URL}/api/kb/${sharingKb.id}/shares`);
            setSharedUsers(res.data);
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

    if (!show) return null;

    return (
        <div className="modal-overlay" role="presentation" onClick={e => { if (e.target === e.currentTarget) onClose(); }}>
            <div className="modal-content" role="dialog" aria-modal="true" aria-labelledby="share-modal-title" style={{ maxWidth: '500px' }}>
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1.5rem' }}>
                    <h3 id="share-modal-title" style={{ margin: 0 }}>"{sharingKb?.name}" {t('shareKb')}</h3>
                    <button onClick={onClose} className="icon-button" aria-label={t('closeShareModal')}><X size={20} /></button>
                </div>

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
            </div>
        </div>
    );
};
