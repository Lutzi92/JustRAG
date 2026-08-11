import { useState, useEffect, useMemo } from 'react';
import axios from 'axios';
import { Loader2 } from 'lucide-react';
import { API_BASE_URL } from '../../api';
import { getApiErrorMessage } from '../../utils/apiError';
import { useTheme } from '../../contexts/ThemeContext';

export interface AdminUserOption {
    id: string;
    username: string;
    firstName?: string;
    lastName?: string;
}

export interface KbTransferOwnerDialogProps {
    kbName: string;
    currentOwnerId?: string | null;
    currentOwnerName?: string | null;
    busy: boolean;
    error: string | null;
    onCancel: () => void;
    onConfirm: (userId: string) => void;
}

function displayName(u: AdminUserOption): string {
    const full = `${u.firstName ?? ''} ${u.lastName ?? ''}`.trim();
    return full ? `${full} (${u.username})` : u.username;
}

/**
 * Owner-transfer modal for the admin KB-Übersicht.
 *
 * Loads the admin user list once on open and filters client-side — the list is
 * small and unpaginated (GET /api/admin/users), so a round trip per keystroke
 * would buy nothing. The current owner is not offered: the backend rejects a
 * self-transfer with 400 and there is no sense in showing a dead option.
 */
export function KbTransferOwnerDialog({
    kbName, currentOwnerId, currentOwnerName, busy, error, onCancel, onConfirm,
}: KbTransferOwnerDialogProps) {
    const { t } = useTheme();
    const [users, setUsers] = useState<AdminUserOption[]>([]);
    const [loadError, setLoadError] = useState<string | null>(null);
    const [loading, setLoading] = useState(true);
    const [search, setSearch] = useState('');
    const [selectedId, setSelectedId] = useState('');

    useEffect(() => {
        let cancelled = false;
        (async () => {
            try {
                const res = await axios.get(`${API_BASE_URL}/api/admin/users`);
                if (!cancelled) {
                    setUsers(res.data as AdminUserOption[]);
                    setLoadError(null);
                }
            } catch (err: unknown) {
                if (!cancelled) setLoadError(getApiErrorMessage(err, t('kbTransferUsersLoadError')));
            } finally {
                if (!cancelled) setLoading(false);
            }
        })();
        return () => { cancelled = true; };
    }, [t]);

    const candidates = useMemo(() => {
        const needle = search.trim().toLowerCase();
        return users
            .filter((u) => u.id !== currentOwnerId)
            .filter((u) => !needle || displayName(u).toLowerCase().includes(needle));
    }, [users, search, currentOwnerId]);

    // A selection that has been filtered out of `candidates` (e.g. the admin
    // picked a user, then typed a search term that hides them) must not be
    // submittable — the admin can no longer see who they'd be confirming.
    const selectionVisible = selectedId !== '' && candidates.some((u) => u.id === selectedId);

    return (
        <div className="modal-overlay" role="presentation" onClick={(e) => { if (e.target === e.currentTarget) onCancel(); }}>
            <div className="modal-content" role="dialog" aria-modal="true" aria-labelledby="kb-transfer-title" style={{ maxWidth: '480px' }}>
                <h3 id="kb-transfer-title" style={{ margin: '0 0 1rem' }}>{t('kbTransferTitle')}</h3>

                <p style={{ margin: '0 0 0.5rem', fontWeight: 600 }}>{kbName}</p>
                <p style={{ margin: '0 0 0.5rem', color: 'var(--text-secondary)', fontSize: '0.9rem' }}>
                    {t('kbTransferCurrentOwner')}: {currentOwnerName || t('kbTransferNoOwner')}
                </p>
                <p style={{ margin: '0 0 1rem', color: 'var(--text-secondary)', fontSize: '0.9rem' }}>{t('kbTransferIntro')}</p>

                <input
                    type="text"
                    value={search}
                    onChange={(e) => setSearch(e.target.value)}
                    placeholder={t('kbTransferSearchPlaceholder')}
                    aria-label={t('kbTransferSearchPlaceholder')}
                    style={{
                        width: '100%', background: 'var(--bg-primary)', border: '1px solid var(--border-color)',
                        color: 'var(--text-primary)', padding: '0.5rem 0.75rem', borderRadius: 'var(--shape-md)',
                        fontSize: '0.9rem', marginBottom: '0.75rem',
                    }}
                />

                <div style={{ maxHeight: '220px', overflowY: 'auto', border: '1px solid var(--border-color)', borderRadius: 'var(--shape-md)', marginBottom: '1rem' }}>
                    {loading && (
                        <div style={{ padding: '0.75rem', color: 'var(--text-secondary)', display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
                            <Loader2 size={15} className="spin" aria-hidden="true" /> {t('loading')}
                        </div>
                    )}
                    {!loading && candidates.length === 0 && (
                        <div style={{ padding: '0.75rem', color: 'var(--text-secondary)', fontSize: '0.9rem' }}>{t('kbTransferNoUsers')}</div>
                    )}
                    {candidates.map((u) => (
                        <button
                            key={u.id}
                            type="button"
                            onClick={() => setSelectedId(u.id)}
                            aria-pressed={selectedId === u.id}
                            style={{
                                display: 'block', width: '100%', textAlign: 'left', border: 'none', cursor: 'pointer',
                                padding: '0.5rem 0.75rem', fontSize: '0.9rem',
                                background: selectedId === u.id ? 'var(--accent-primary)' : 'transparent',
                                color: selectedId === u.id ? 'white' : 'var(--text-primary)',
                            }}
                        >
                            {displayName(u)}
                        </button>
                    ))}
                </div>

                {(error || loadError) && (
                    <p style={{ color: 'var(--error-text)', fontSize: '0.9rem', margin: '0 0 1rem' }}>{error || loadError}</p>
                )}

                <div style={{ display: 'flex', justifyContent: 'flex-end', gap: '0.5rem' }}>
                    <button type="button" onClick={onCancel} disabled={busy} style={{
                        background: 'var(--bg-primary)', border: '1px solid var(--border-color)', color: 'var(--text-primary)',
                        padding: '0.5rem 1rem', borderRadius: 'var(--shape-md)', cursor: busy ? 'default' : 'pointer',
                    }}>
                        {t('cancel')}
                    </button>
                    <button
                        type="button"
                        onClick={() => { if (selectionVisible) onConfirm(selectedId); }}
                        disabled={!selectionVisible || busy}
                        style={{
                            background: selectionVisible && !busy ? 'var(--accent-primary)' : 'var(--bg-primary)',
                            border: '1px solid var(--border-color)', color: selectionVisible && !busy ? 'white' : 'var(--text-secondary)',
                            padding: '0.5rem 1rem', borderRadius: 'var(--shape-md)',
                            cursor: selectionVisible && !busy ? 'pointer' : 'default',
                            display: 'flex', alignItems: 'center', gap: '0.4rem',
                        }}
                    >
                        {busy && <Loader2 size={15} className="spin" aria-hidden="true" />}
                        {t('kbTransferSubmit')}
                    </button>
                </div>
            </div>
        </div>
    );
}
