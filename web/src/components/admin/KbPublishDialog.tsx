import { Globe, Loader2 } from 'lucide-react';
import { useTheme } from '../../contexts/ThemeContext';

export interface KbPublishDialogProps {
    kbName: string;
    busy: boolean;
    error: string | null;
    onCancel: () => void;
    onConfirm: () => void;
}

/**
 * Confirmation modal for making a private KB public from the admin KB-Übersicht.
 *
 * Publishing hands read access to every authenticated user and strips the KB's
 * owner, so the dialog states both up front rather than leaving the operator to
 * infer them from a button label. It also names the staging step: the backend
 * forces is_published = false on publish, so the KB is public but invisible to
 * ordinary users until an admin flips the catalog toggle in the global-KB tab.
 *
 * No type-to-confirm here, unlike KbDeleteDialog — publishing is reversible
 * (the unpublish path in the global-KB tab), deleting is not. Presentational
 * only: the parent owns the request.
 */
export function KbPublishDialog({ kbName, busy, error, onCancel, onConfirm }: KbPublishDialogProps) {
    const { t } = useTheme();

    return (
        <div className="modal-overlay" role="presentation" onClick={(e) => { if (e.target === e.currentTarget) onCancel(); }}>
            <div className="modal-content" role="dialog" aria-modal="true" aria-labelledby="kb-publish-title" style={{ maxWidth: '480px' }}>
                <h3 id="kb-publish-title" style={{ margin: '0 0 1rem', display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
                    <Globe size={18} aria-hidden="true" style={{ color: 'var(--accent-primary)' }} />
                    {t('kbPublishTitle')}
                </h3>

                <p style={{ margin: '0 0 1rem', fontWeight: 600 }}>{kbName}</p>

                <p style={{ margin: '0 0 1rem', color: 'var(--error-text)', fontSize: '0.9rem' }}>{t('kbPublishWarning')}</p>
                <p style={{ margin: '0 0 1rem', color: 'var(--text-secondary)', fontSize: '0.9rem' }}>{t('kbPublishStagedNote')}</p>
                <p style={{ margin: '0 0 1rem', color: 'var(--text-secondary)', fontSize: '0.9rem' }}>{t('kbPublishOwnerNote')}</p>

                {error && <p style={{ color: 'var(--error-text)', fontSize: '0.9rem', margin: '0 0 1rem' }}>{error}</p>}

                <div style={{ display: 'flex', justifyContent: 'flex-end', gap: '0.5rem' }}>
                    <button type="button" onClick={onCancel} disabled={busy} style={{
                        background: 'var(--bg-primary)', border: '1px solid var(--border-color)', color: 'var(--text-primary)',
                        padding: '0.5rem 1rem', borderRadius: 'var(--shape-md)', cursor: busy ? 'default' : 'pointer',
                    }}>
                        {t('cancel')}
                    </button>
                    <button type="button" onClick={onConfirm} disabled={busy} style={{
                        background: busy ? 'var(--bg-primary)' : 'var(--accent-primary)',
                        border: '1px solid var(--border-color)', color: busy ? 'var(--text-secondary)' : 'white',
                        padding: '0.5rem 1rem', borderRadius: 'var(--shape-md)', cursor: busy ? 'default' : 'pointer',
                        display: 'flex', alignItems: 'center', gap: '0.4rem',
                    }}>
                        {busy && <Loader2 size={15} className="spin" aria-hidden="true" />}
                        {t('kbActionPublish')}
                    </button>
                </div>
            </div>
        </div>
    );
}
