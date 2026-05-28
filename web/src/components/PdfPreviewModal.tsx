import React, { useEffect, useState } from 'react';
import { X, Loader2 } from 'lucide-react';
import axios from 'axios';
import { API_BASE_URL } from '../api';
import { useTheme } from '../contexts/ThemeContext';
import { useToast } from '../contexts/ToastContext';

interface PdfPreviewModalProps {
    show: boolean;
    onClose: () => void;
    fileId: string;
    fileName: string;
    page: number;
}

export const PdfPreviewModal: React.FC<PdfPreviewModalProps> = ({ show, onClose, fileId, fileName, page }) => {
    const { t } = useTheme();
    const toast = useToast();
    const [blobUrl, setBlobUrl] = useState<string | null>(null);
    const [error, setError] = useState<string | null>(null);
    const [loading, setLoading] = useState(false);

    useEffect(() => {
        if (!show || !fileId) return;

        let revoke: string | null = null;
        setLoading(true);
        setError(null);

        axios.get(`${API_BASE_URL}/api/files/${fileId}/download`, { responseType: 'blob' })
            .then(res => {
                const blob = new Blob([res.data], { type: 'application/pdf' });
                const url = URL.createObjectURL(blob);
                revoke = url;
                setBlobUrl(url);
            })
            .catch(() => {
                setError(t('pdfLoadError'));
                toast.error(t('pdfLoadError'));
            })
            .finally(() => {
                setLoading(false);
            });

        return () => {
            if (revoke) URL.revokeObjectURL(revoke);
            setBlobUrl(null);
            setError(null);
        };
    }, [show, fileId]);

    if (!show) return null;

    const iframeSrc = blobUrl ? `${blobUrl}#page=${page}` : '';

    return (
        // eslint-disable-next-line jsx-a11y/click-events-have-key-events, jsx-a11y/no-static-element-interactions
        <div className="modal-overlay" onClick={onClose}>
            <div
                className="modal-content"
                onClick={e => e.stopPropagation()}
                role="dialog"
                aria-modal="true"
                aria-labelledby="pdf-preview-title"
                style={{ maxWidth: '90vw', width: '900px', maxHeight: '90vh', height: '85vh', display: 'flex', flexDirection: 'column', padding: 0 }}
            >
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', padding: '0.75rem 1rem', borderBottom: '1px solid var(--border-color)', flexShrink: 0 }}>
                    <h2 id="pdf-preview-title" style={{ margin: 0, fontSize: '1rem', color: 'var(--text-primary)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                        {fileName}{page > 1 ? ` — Seite ${page}` : ''}
                    </h2>
                    <button onClick={onClose} className="icon-button" aria-label={t('close')}><X size={20} /></button>
                </div>
                <div style={{ flex: 1, overflow: 'hidden' }}>
                    {loading && (
                        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', height: '100%', gap: '8px', color: 'var(--text-secondary)' }}>
                            <Loader2 className="animate-spin" size={20} /> PDF wird geladen...
                        </div>
                    )}
                    {error && (
                        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', height: '100%', color: 'var(--error-color)', padding: '2rem' }}>
                            {error}
                        </div>
                    )}
                    {blobUrl && (
                        <iframe
                            src={iframeSrc}
                            title={fileName}
                            style={{ width: '100%', height: '100%', border: 'none' }}
                        />
                    )}
                </div>
            </div>
        </div>
    );
};
