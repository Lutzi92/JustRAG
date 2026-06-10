import React, { useEffect } from 'react';
import { X, Trash2, Rss } from 'lucide-react';
import { motion } from 'framer-motion';
import type { RssFeed, FileEntry } from '../types';
import { useTheme } from '../contexts/ThemeContext';
import { useReducedMotion, getMotionProps } from '../hooks/useReducedMotion';
import './RssFeedEntriesModal.css';

interface RssFeedEntriesModalProps {
    feed: RssFeed;
    files: FileEntry[];
    onDeleteFile: (fileId: string, e: React.MouseEvent) => void;
    onClose: () => void;
}

export const RssFeedEntriesModal: React.FC<RssFeedEntriesModalProps> = ({ feed, files, onDeleteFile, onClose }) => {
    const { t } = useTheme();
    const reducedMotion = useReducedMotion();

    useEffect(() => {
        const handleEsc = (e: KeyboardEvent) => {
            if (e.key === 'Escape') onClose();
        };
        window.addEventListener('keydown', handleEsc);
        return () => window.removeEventListener('keydown', handleEsc);
    }, [onClose]);

    return (
        <div className="rss-entries-modal__overlay" role="presentation" onClick={(e) => { if (e.target === e.currentTarget) onClose(); }}>
            <div className="rss-entries-modal" role="dialog" aria-modal="true" aria-label={feed.title || feed.url}>
                <div className="rss-entries-modal__header">
                    <Rss size={16} />
                    <h2 className="rss-entries-modal__title">{feed.title || feed.url}</h2>
                    <span className="rss-entries-modal__count">{files.length} {t('rssEntries')}</span>
                    <button onClick={onClose} className="rss-entries-modal__close" aria-label={t('close')}>
                        <X size={18} />
                    </button>
                </div>
                <ul className="rss-entries-modal__list">
                    {files.length === 0 && (
                        <li className="rss-entries-modal__empty">{t('rssNoEntries')}</li>
                    )}
                    {files.map(file => (
                        <li key={file.id} className="rss-entries-modal__item">
                            <div className="rss-entries-modal__item-main">
                                <span className="rss-entries-modal__item-name">{file.name}</span>
                                <div className="rss-entries-modal__item-meta">
                                    <span style={{ color: file.status === 'error' ? 'var(--error-text)' : undefined }}>
                                        {file.status}
                                    </span>
                                    <span>{new Date(file.createdAt).toLocaleDateString()}</span>
                                </div>
                                {file.status === 'processing' && (
                                    <div className="rss-entries-modal__progress-track">
                                        <motion.div
                                            {...getMotionProps(reducedMotion)}
                                            initial={{ width: 0 }}
                                            animate={{ width: `${file.progress}%` }}
                                            className="rss-entries-modal__progress-fill"
                                        />
                                    </div>
                                )}
                            </div>
                            <button
                                onClick={(e) => onDeleteFile(file.id, e)}
                                className="rss-entries-modal__delete-btn"
                                title={t('delete')}
                                aria-label={`${t('delete')} ${file.name}`}
                            >
                                <Trash2 size={14} />
                            </button>
                        </li>
                    ))}
                </ul>
            </div>
        </div>
    );
};
