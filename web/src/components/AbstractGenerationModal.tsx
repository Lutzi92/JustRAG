import React from 'react';
import { Sparkles } from 'lucide-react';
import type { FileEntry } from '../types';
import { useTheme } from '../contexts/ThemeContext';

interface AbstractGenerationModalProps {
    show: boolean;
    onClose: () => void;
    abstractFileId: string;
    setAbstractFileId: (val: string) => void;
    abstractType: 'academic' | 'executive';
    setAbstractType: (val: 'academic' | 'executive') => void;
    files: FileEntry[];
    onSubmit: () => void;
}

export const AbstractGenerationModal: React.FC<AbstractGenerationModalProps> = ({
    show, onClose, abstractFileId, setAbstractFileId, abstractType, setAbstractType, files, onSubmit,
}) => {
    const { t } = useTheme();
    if (!show) return null;

    const completedFiles = files.filter(f => f.status === 'completed');

    return (
        <div className="modal-overlay" role="presentation" onClick={e => { if (e.target === e.currentTarget) onClose(); }}>
            <div className="modal-content" style={{ maxWidth: '500px' }} role="dialog" aria-modal="true" aria-labelledby="abstract-modal-title">
                <div style={{ display: 'flex', alignItems: 'center', gap: '12px', marginBottom: '1.5rem' }}>
                    <div style={{ padding: '10px', background: 'var(--tag-bg)', borderRadius: '12px', color: 'var(--accent-primary)', display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
                        <Sparkles size={24} aria-hidden="true" />
                    </div>
                    <h3 id="abstract-modal-title" style={{ margin: 0, fontSize: '1.25rem' }}>{t('generateAbstractTitle')}</h3>
                </div>

                <div className="input-group" style={{ marginBottom: '1.5rem' }}>
                    <label htmlFor="abstract-file-select" style={{ fontSize: '0.85rem', color: 'var(--text-secondary)', marginBottom: '0.5rem', display: 'block' }}>
                        {t('abstractSelectFile')}
                    </label>
                    <select
                        id="abstract-file-select"
                        value={abstractFileId}
                        onChange={e => setAbstractFileId(e.target.value)}
                        style={{ width: '100%', padding: '0.75rem', borderRadius: '8px', border: '1px solid var(--border-color)', background: 'var(--bg-primary)', color: 'var(--text-primary)' }}
                    >
                        {completedFiles.length === 0 && (
                            <option value="">{t('abstractNoFiles')}</option>
                        )}
                        {completedFiles.map(f => (
                            <option key={f.id} value={f.id}>
                                {f.name}
                            </option>
                        ))}
                    </select>
                </div>

                <div className="input-group" style={{ marginBottom: '1.5rem' }}>
                    <label style={{ fontSize: '0.85rem', color: 'var(--text-secondary)', marginBottom: '0.5rem', display: 'block' }}>
                        {t('abstractTypeLabel')}
                    </label>
                    <div style={{ display: 'flex', gap: '12px' }}>
                        <label style={{ flex: 1, display: 'flex', alignItems: 'center', gap: '8px', padding: '0.75rem', borderRadius: '8px', border: `1px solid ${abstractType === 'academic' ? 'var(--accent-primary)' : 'var(--border-color)'}`, background: abstractType === 'academic' ? 'var(--accent-primary-translucent, rgba(99,102,241,0.08))' : 'var(--bg-primary)', cursor: 'pointer' }}>
                            <input
                                type="radio"
                                name="abstractType"
                                value="academic"
                                checked={abstractType === 'academic'}
                                onChange={() => setAbstractType('academic')}
                            />
                            {t('abstractTypeAcademic')}
                        </label>
                        <label style={{ flex: 1, display: 'flex', alignItems: 'center', gap: '8px', padding: '0.75rem', borderRadius: '8px', border: `1px solid ${abstractType === 'executive' ? 'var(--accent-primary)' : 'var(--border-color)'}`, background: abstractType === 'executive' ? 'var(--accent-primary-translucent, rgba(99,102,241,0.08))' : 'var(--bg-primary)', cursor: 'pointer' }}>
                            <input
                                type="radio"
                                name="abstractType"
                                value="executive"
                                checked={abstractType === 'executive'}
                                onChange={() => setAbstractType('executive')}
                            />
                            {t('abstractTypeExecutive')}
                        </label>
                    </div>
                </div>

                <div style={{ display: 'flex', gap: '12px', justifyContent: 'flex-end' }}>
                    <button onClick={onClose} className="secondary-button" style={{ flex: 1, padding: '0.75rem' }}>
                        {t('cancel')}
                    </button>
                    <button
                        onClick={onSubmit}
                        disabled={!abstractFileId}
                        className="search-button"
                        style={{ flex: 1, padding: '0.75rem', display: 'flex', justifyContent: 'center', alignItems: 'center', gap: '8px' }}
                    >
                        <Sparkles size={16} aria-hidden="true" /> {t('generate')}
                    </button>
                </div>
            </div>
        </div>
    );
};
