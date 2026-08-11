import { useState } from 'react';
import { AlertTriangle, Loader2 } from 'lucide-react';
import { useTheme } from '../../contexts/ThemeContext';

export interface KbDeleteDialogProps {
    kbName: string;
    isGlobal: boolean;
    fileCount: number;
    /** Pre-formatted size (the dashboard owns formatBytes). */
    sizeLabel: string;
    chatCount: number;
    busy: boolean;
    error: string | null;
    onCancel: () => void;
    onConfirm: () => void;
}

/**
 * Type-to-confirm delete modal for the admin KB-Übersicht.
 *
 * Deletion cascades across the main DB, the vector DB, and object storage, so
 * the operator must retype the KB name exactly — a plain confirm is too easy to
 * fire from the wrong table row. Presentational only: the parent owns the request.
 */
export function KbDeleteDialog({
    kbName, isGlobal, fileCount, sizeLabel, chatCount, busy, error, onCancel, onConfirm,
}: KbDeleteDialogProps) {
    const { t } = useTheme();
    const [typed, setTyped] = useState('');
    const armed = typed === kbName && !busy;

    return (
        <div className="modal-overlay" role="presentation" onClick={(e) => { if (e.target === e.currentTarget) onCancel(); }}>
            <div className="modal-content" role="dialog" aria-modal="true" aria-labelledby="kb-delete-title" style={{ maxWidth: '480px' }}>
                <h3 id="kb-delete-title" style={{ margin: '0 0 1rem', display: 'flex', alignItems: 'center', gap: '0.5rem' }}>
                    <AlertTriangle size={18} aria-hidden="true" style={{ color: 'var(--error-text)' }} />
                    {t('kbDeleteTitle')}
                </h3>

                <p style={{ margin: '0 0 0.5rem', fontWeight: 600 }}>{kbName}</p>
                <p style={{ margin: '0 0 1rem', color: 'var(--text-secondary)', fontSize: '0.9rem' }}>
                    {t('kbDeleteStats')
                        .replace('{files}', String(fileCount))
                        .replace('{size}', sizeLabel)
                        .replace('{chats}', String(chatCount))}
                </p>

                <p style={{ margin: '0 0 1rem', color: 'var(--error-text)', fontSize: '0.9rem' }}>{t('kbDeleteWarning')}</p>
                {isGlobal && (
                    <p style={{ margin: '0 0 1rem', color: 'var(--text-secondary)', fontSize: '0.9rem' }}>{t('kbDeleteGlobalNote')}</p>
                )}

                <label htmlFor="kb-delete-confirm" style={{ display: 'block', marginBottom: '0.4rem', fontSize: '0.9rem' }}>
                    {t('kbDeleteConfirmLabel')}
                </label>
                <input
                    id="kb-delete-confirm"
                    type="text"
                    value={typed}
                    onChange={(e) => setTyped(e.target.value)}
                    autoComplete="off"
                    style={{
                        width: '100%', background: 'var(--bg-primary)', border: '1px solid var(--border-color)',
                        color: 'var(--text-primary)', padding: '0.5rem 0.75rem', borderRadius: 'var(--shape-md)',
                        fontSize: '0.9rem', marginBottom: '1rem',
                    }}
                />

                {error && <p style={{ color: 'var(--error-text)', fontSize: '0.9rem', margin: '0 0 1rem' }}>{error}</p>}

                <div style={{ display: 'flex', justifyContent: 'flex-end', gap: '0.5rem' }}>
                    <button type="button" onClick={onCancel} disabled={busy} style={{
                        background: 'var(--bg-primary)', border: '1px solid var(--border-color)', color: 'var(--text-primary)',
                        padding: '0.5rem 1rem', borderRadius: 'var(--shape-md)', cursor: busy ? 'default' : 'pointer',
                    }}>
                        {t('cancel')}
                    </button>
                    <button type="button" onClick={() => { if (armed) onConfirm(); }} disabled={!armed} style={{
                        background: armed ? 'var(--error-text)' : 'var(--bg-primary)',
                        border: '1px solid var(--border-color)', color: armed ? 'white' : 'var(--text-secondary)',
                        padding: '0.5rem 1rem', borderRadius: 'var(--shape-md)', cursor: armed ? 'pointer' : 'default',
                        display: 'flex', alignItems: 'center', gap: '0.4rem',
                    }}>
                        {busy && <Loader2 size={15} className="spin" aria-hidden="true" />}
                        {t('delete')}
                    </button>
                </div>
            </div>
        </div>
    );
}
