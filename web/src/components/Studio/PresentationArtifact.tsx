import React from 'react';
import { Download, FileText } from 'lucide-react';
import axios from 'axios';
import type { PresentationContentData } from '../../types';
import { API_BASE_URL } from '../../api';
import { useTheme } from '../../contexts/ThemeContext';
import { useToast } from '../../contexts/ToastContext';

/**
 * Presentation artifact view: slide summary + PPTX download. Ported from the
 * retired `ContentModal` (fix wave item 1) so presentations opened from the
 * Workspace keep a viewer and a download control.
 */
export const PresentationArtifact: React.FC<{ id: string; content: PresentationContentData }> = ({ id, content }) => {
    const { t } = useTheme();
    const toast = useToast();
    const canDownload = !!(content.filePath || content.markdown);

    const handleDownload = () => {
        if (content.filePath) {
            axios.get(`${API_BASE_URL}/api/generated-content/${id}/download`, {
                responseType: 'blob',
            }).then((response) => {
                const url = window.URL.createObjectURL(new Blob([response.data]));
                const link = document.createElement('a');
                link.href = url;

                const contentDisposition = response.headers['content-disposition'];
                let fileName = 'presentation.pptx';
                if (contentDisposition) {
                    const match = contentDisposition.match(/filename="?(.+)"?/);
                    if (match && match[1]) fileName = match[1].replace(/"/g, '');
                }

                link.setAttribute('download', fileName);
                document.body.appendChild(link);
                link.click();
                link.remove();
                window.URL.revokeObjectURL(url);
            }).catch((err: unknown) => {
                console.error('Download failed', err);
                toast.error(t('downloadFailed'));
            });
        } else if (content.markdown) {
            // Fallback for old style if any
            const blob = new Blob([content.markdown], { type: 'text/markdown' });
            const url = URL.createObjectURL(blob);
            const link = document.createElement('a');
            link.href = url;
            link.download = 'presentation.md';
            document.body.appendChild(link);
            link.click();
            document.body.removeChild(link);
            URL.revokeObjectURL(url);
        }
    };

    return (
        <div style={{ display: 'flex', flexDirection: 'column', gap: '1rem' }}>
            {canDownload && (
                <div>
                    <button className="secondary-button" onClick={handleDownload}>
                        <Download size={16} aria-hidden="true" /> {t('downloadPptx')}
                    </button>
                </div>
            )}
            <div style={{ whiteSpace: 'pre-wrap', lineHeight: '1.6' }}>
                {content.summary ? (
                    <div style={{ textAlign: 'center', padding: '2rem' }}>
                        <FileText size={48} style={{ color: 'var(--accent-primary)', marginBottom: '1rem' }} aria-hidden="true" />
                        <h3>{t('presentationCreated')}</h3>
                        <p>{content.summary}</p>
                    </div>
                ) : (
                    content.markdown
                )}
            </div>
        </div>
    );
};
