import { useEffect, useState } from 'react';
import axios from 'axios';
import { Loader2 } from 'lucide-react';
import { API_BASE_URL } from '../../api';
import { getApiErrorMessage } from '../../utils/apiError';
import { useTheme } from '../../contexts/ThemeContext';

export interface UnpublishCandidate {
    userId: string;
    username: string;
    firstName?: string | null;
    lastName?: string | null;
}

export interface UnpublishImpact {
    subscribers: number;
    candidates: UnpublishCandidate[];
}

export interface AdminUnpublishDialogProps {
    kbId: string;
    kbName: string;
    /** Acting admin's own user id — the fallback owner when there are no
     * KB-admin candidates to pick from. */
    currentUserId: string;
    busy: boolean;
    error: string | null;
    onCancel: () => void;
    onConfirm: (newOwnerId: string) => void;
}

function displayName(c: UnpublishCandidate): string {
    const full = `${c.firstName ?? ''} ${c.lastName ?? ''}`.trim();
    return full ? `${full} (${c.username})` : c.username;
}

/**
 * Unpublish confirmation modal for the public-KB admin tab.
 *
 * Loads GET /api/admin/kb/{id}/unpublish-impact on mount so the admin sees
 * the subscriber count and the pool of KB-admin candidates before confirming
 * — POST /api/admin/kb/{id}/unpublish requires newOwnerId, since a private KB
 * without an owner would be unreachable for everyone but superadmins. When
 * there are no candidates the acting admin becomes the owner automatically
 * (no picker to block on); otherwise confirmation stays disabled until one is
 * chosen.
 */
export function AdminUnpublishDialog({
    kbId, kbName, currentUserId, busy, error, onCancel, onConfirm,
}: AdminUnpublishDialogProps) {
    const { t } = useTheme();
    const [impact, setImpact] = useState<UnpublishImpact | null>(null);
    const [loading, setLoading] = useState(true);
    const [loadError, setLoadError] = useState<string | null>(null);
    const [selectedOwnerId, setSelectedOwnerId] = useState('');

    useEffect(() => {
        let cancelled = false;
        (async () => {
            try {
                const res = await axios.get(`${API_BASE_URL}/api/admin/kb/${kbId}/unpublish-impact`);
                const data = res.data as UnpublishImpact;
                if (cancelled) return;
                setImpact(data);
                setLoadError(null);
                if (data.candidates.length === 0) setSelectedOwnerId(currentUserId);
            } catch (err: unknown) {
                if (!cancelled) setLoadError(getApiErrorMessage(err, t('unpublishImpactLoadError')));
            } finally {
                if (!cancelled) setLoading(false);
            }
        })();
        return () => { cancelled = true; };
    }, [kbId, currentUserId, t]);

    const hasCandidates = (impact?.candidates.length ?? 0) > 0;
    const ready = impact !== null && (!hasCandidates || selectedOwnerId !== '');

    return (
        <div className="modal-overlay" role="presentation" onClick={(e) => { if (e.target === e.currentTarget) onCancel(); }}>
            <div className="modal-content" role="dialog" aria-modal="true" aria-labelledby="admin-unpublish-title" style={{ maxWidth: '480px' }}>
                <h3 id="admin-unpublish-title" style={{ margin: '0 0 1rem' }}>{t('unpublishTitle')}</h3>

                <p style={{ margin: '0 0 1rem', fontWeight: 600 }}>{kbName}</p>

                {loading && (
                    <div style={{ padding: '0.75rem 0', color: 'var(--text-secondary)', display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
                        <Loader2 size={15} className="spin" aria-hidden="true" /> {t('loading')}
                    </div>
                )}

                {impact && (
                    <>
                        <p style={{ margin: '0 0 1rem', color: 'var(--text-secondary)', fontSize: '0.9rem' }}>
                            {t('unpublishSubscriberWarning').replace('{count}', String(impact.subscribers))}
                        </p>

                        {hasCandidates ? (
                            <>
                                <label htmlFor="admin-unpublish-owner" style={{ display: 'block', marginBottom: '0.4rem', fontSize: '0.9rem' }}>
                                    {t('unpublishOwnerLabel')}
                                </label>
                                <select
                                    id="admin-unpublish-owner"
                                    aria-label={t('unpublishOwnerLabel')}
                                    value={selectedOwnerId}
                                    onChange={(e) => setSelectedOwnerId(e.target.value)}
                                    style={{
                                        width: '100%', background: 'var(--bg-primary)', border: '1px solid var(--border-color)',
                                        color: 'var(--text-primary)', padding: '0.5rem 0.75rem', borderRadius: 'var(--shape-md)',
                                        fontSize: '0.9rem', marginBottom: '1rem',
                                    }}
                                >
                                    <option value="" disabled>{t('unpublishOwnerLabel')}</option>
                                    {impact.candidates.map((c) => (
                                        <option key={c.userId} value={c.userId}>{displayName(c)}</option>
                                    ))}
                                </select>
                            </>
                        ) : (
                            <p style={{ margin: '0 0 1rem', color: 'var(--text-secondary)', fontSize: '0.9rem' }}>
                                {t('unpublishNoCandidates')}
                            </p>
                        )}
                    </>
                )}

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
                        onClick={() => { if (ready && !busy) onConfirm(selectedOwnerId); }}
                        disabled={!ready || busy}
                        style={{
                            background: ready && !busy ? 'var(--error-text)' : 'var(--bg-primary)',
                            border: '1px solid var(--border-color)', color: ready && !busy ? 'white' : 'var(--text-secondary)',
                            padding: '0.5rem 1rem', borderRadius: 'var(--shape-md)',
                            cursor: ready && !busy ? 'pointer' : 'default',
                            display: 'flex', alignItems: 'center', gap: '0.4rem',
                        }}
                    >
                        {busy && <Loader2 size={15} className="spin" aria-hidden="true" />}
                        {t('unpublishKb')}
                    </button>
                </div>
            </div>
        </div>
    );
}
