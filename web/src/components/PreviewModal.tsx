import React from 'react';
import { X } from 'lucide-react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import rehypeRaw from 'rehype-raw';
import rehypeSanitize, { defaultSchema } from 'rehype-sanitize';
import { useTheme } from '../contexts/ThemeContext';

const sanitizeSchema = {
    ...defaultSchema,
    tagNames: [...(defaultSchema.tagNames || []), 'sup', 'sub', 'details', 'summary'],
    attributes: {
        ...defaultSchema.attributes,
        code: ['className'],
    },
};

const REMARK_PLUGINS = [remarkGfm];
const REHYPE_PLUGINS: import('unified').PluggableList = [rehypeRaw, [rehypeSanitize, sanitizeSchema]];

interface PreviewModalProps {
    show: boolean;
    onClose: () => void;
    previewPage: { title: string; content: string; url: string } | null;
}

export const PreviewModal: React.FC<PreviewModalProps> = ({ show, onClose, previewPage }) => {
    const { t } = useTheme();
    if (!show || !previewPage) return null;

    const isMarkdown = previewPage.title.toLowerCase().endsWith('.md');

    return (
        <div className="modal-overlay" role="presentation" onClick={e => { if (e.target === e.currentTarget) onClose(); }}>
            <div className="modal-content" style={{ maxWidth: '80vw', maxHeight: '85vh', display: 'flex', flexDirection: 'column' }} role="dialog" aria-modal="true" aria-labelledby="preview-title">
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', marginBottom: '1rem', borderBottom: '1px solid var(--border-color)', paddingBottom: '1rem' }}>
                    <div style={{ overflow: 'hidden' }}>
                        <h2 id="preview-title" style={{ margin: 0, fontSize: '1.25rem', color: 'var(--text-primary)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{previewPage.title}</h2>
                        {previewPage.url && (
                            <a href={previewPage.url} target="_blank" rel="noopener noreferrer" style={{ fontSize: '0.8rem', color: 'var(--accent-primary)', textDecoration: 'none' }}>
                                {previewPage.url}
                            </a>
                        )}
                    </div>
                    <button onClick={onClose} className="icon-button" aria-label={t('close')}><X size={20} /></button>
                </div>
                <div style={{ flex: 1, overflowY: 'auto', textAlign: 'left', color: 'var(--text-primary)', lineHeight: 1.6, fontSize: '0.95rem' }}>
                    <div className="markdown-content" style={{ whiteSpace: isMarkdown ? 'normal' : 'pre-wrap', fontFamily: 'inherit' }}>
                        {isMarkdown ? (
                            <ReactMarkdown remarkPlugins={REMARK_PLUGINS} rehypePlugins={REHYPE_PLUGINS}>
                                {previewPage.content}
                            </ReactMarkdown>
                        ) : (
                            previewPage.content
                        )}
                    </div>
                </div>
            </div>
        </div>
    );
};
