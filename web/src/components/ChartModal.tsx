import React from 'react';
import { Sparkles, FileSpreadsheet, FileText } from 'lucide-react';
import type { FileEntry } from '../types';
import { useTheme } from '../contexts/ThemeContext';
import { useFormValidation } from '../hooks/useFormValidation';

const DATA_FILE_EXTENSIONS = ['.xlsx', '.xls', '.csv', '.ods', '.json', '.parquet'];

interface ChartModalProps {
    show: boolean;
    onClose: () => void;
    chartPrompt: string;
    setChartPrompt: (val: string) => void;
    selectedFileId: string;
    setSelectedFileId: (val: string) => void;
    files: FileEntry[];
    onSubmit: () => void;
}

export const ChartModal: React.FC<ChartModalProps> = ({
    show, onClose, chartPrompt, setChartPrompt, selectedFileId, setSelectedFileId, files, onSubmit
}) => {
    const { t } = useTheme();
    const { errors, validate, clearError } = useFormValidation({
        prompt: (v) => !v.trim() && t('fieldRequired'),
    });
    if (!show) return null;

    return (
        // eslint-disable-next-line jsx-a11y/click-events-have-key-events, jsx-a11y/no-static-element-interactions, jsx-a11y/no-noninteractive-element-interactions
        <div className="modal-overlay" onClick={onClose}>
            <div className="modal-content" onClick={e => e.stopPropagation()} style={{ maxWidth: '500px' }} role="dialog" aria-modal="true" aria-labelledby="chart-modal-title">
                <div style={{ display: 'flex', alignItems: 'center', gap: '12px', marginBottom: '1.5rem' }}>
                    <div style={{ padding: '10px', background: 'var(--tag-bg)', borderRadius: '12px', color: 'var(--accent-primary)', display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
                        <Sparkles size={24} aria-hidden="true" />
                    </div>
                    <h3 id="chart-modal-title" style={{ margin: 0, fontSize: '1.25rem' }}>{t('generateChartTitle')}</h3>
                </div>

                <div className="input-group" style={{ marginBottom: '1.5rem' }}>
                    <label htmlFor="chart-prompt" style={{ fontSize: '0.85rem', color: 'var(--text-secondary)', marginBottom: '0.5rem', display: 'block' }}>{t('chartPromptLabel')}</label>
                    <textarea
                        id="chart-prompt"
                        // eslint-disable-next-line jsx-a11y/no-autofocus
                        autoFocus
                        value={chartPrompt}
                        onChange={e => { setChartPrompt(e.target.value); clearError('prompt'); }}
                        placeholder={t('chartPromptPlaceholder')}
                        style={{ width: '100%', minHeight: '100px', padding: '0.75rem', borderRadius: '8px', border: '1px solid var(--border-color)', background: 'var(--bg-primary)', color: 'var(--text-primary)', resize: 'vertical' }}
                    />
                    {errors.prompt && <span className="field-error" role="alert">{errors.prompt}</span>}
                </div>

                <div className="input-group" style={{ marginBottom: '1rem' }}>
                    <label htmlFor="chart-file-select" style={{ fontSize: '0.85rem', color: 'var(--text-secondary)', marginBottom: '0.5rem', display: 'block' }}>{t('selectFile')}</label>
                    <select
                        id="chart-file-select"
                        value={selectedFileId}
                        onChange={e => setSelectedFileId(e.target.value)}
                        style={{ width: '100%', padding: '0.75rem', borderRadius: '8px', border: '1px solid var(--border-color)', background: 'var(--bg-primary)', color: 'var(--text-primary)' }}
                    >
                        <option value="">{t('automaticLastFile')}</option>
                        {files
                            .filter(f => f.status === 'completed')
                            .map(f => {
                                const isData = DATA_FILE_EXTENSIONS.some(ext => f.name.toLowerCase().endsWith(ext));
                                return (
                                    <option key={f.id} value={f.id}>
                                        {isData ? '\u{1F4CA} ' : '\u{1F4C4} '}{f.name}
                                    </option>
                                );
                            })
                        }
                    </select>
                </div>
                <div style={{ fontSize: '0.78rem', color: 'var(--text-secondary)', marginBottom: '1.5rem', display: 'flex', gap: '1rem' }}>
                    <span style={{ display: 'flex', alignItems: 'center', gap: '4px' }}><FileSpreadsheet size={13} /> {t('dataFileDirect')}</span>
                    <span style={{ display: 'flex', alignItems: 'center', gap: '4px' }}><FileText size={13} /> {t('documentAiExtraction')}</span>
                </div>

                <div style={{ display: 'flex', gap: '12px', justifyContent: 'flex-end' }}>
                    <button
                        onClick={onClose}
                        className="secondary-button"
                        style={{ flex: 1, padding: '0.75rem' }}
                    >
                        {t('cancel')}
                    </button>
                    <button
                        onClick={() => { if (validate({ prompt: chartPrompt })) onSubmit(); }}
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
