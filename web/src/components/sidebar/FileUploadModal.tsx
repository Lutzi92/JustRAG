import React, { memo, useState } from 'react';
import { Upload, Type } from 'lucide-react';
import { useTheme } from '../../contexts/ThemeContext';
import { useFormValidation } from '../../hooks/useFormValidation';
import { SourceModal } from './SourceModal';

interface FileUploadModalProps {
    show: boolean;
    onClose: () => void;
    fileInputRef: React.RefObject<HTMLInputElement | null>;
    onTextSourceAdd: () => void;
    textSourceTitle: string;
    setTextSourceTitle: (v: string) => void;
    textSourceContent: string;
    setTextSourceContent: (v: string) => void;
    onDragOver: (e: React.DragEvent) => void;
    onDragEnter: (e: React.DragEvent) => void;
    onDragLeave: (e: React.DragEvent) => void;
    onDrop: (e: React.DragEvent) => void;
    isDragging: boolean;
}

const FileUploadModalComp: React.FC<FileUploadModalProps> = ({
    show, onClose, fileInputRef,
    onTextSourceAdd, textSourceTitle, setTextSourceTitle,
    textSourceContent, setTextSourceContent,
    onDragOver, onDragEnter, onDragLeave, onDrop, isDragging,
}) => {
    const { t } = useTheme();
    const { errors, validate, clearError } = useFormValidation({
        content: (v) => !v.trim() && t('fieldRequired'),
    });
    const [mode, setMode] = useState<'dropzone' | 'text'>('dropzone');

    return (
        <SourceModal title={mode === 'dropzone' ? t('uploadFile') : t('insertText')} show={show} onClose={onClose}>
            {mode === 'dropzone' ? (
                <>
                    <button
                        className={`dropzone ${isDragging ? 'dragging' : ''}`}
                        onDragOver={onDragOver}
                        onDragEnter={onDragEnter}
                        onDragLeave={onDragLeave}
                        onDrop={onDrop}
                        onClick={() => fileInputRef.current?.click()}
                        style={{ background: 'none', width: '100%', font: 'inherit' }}
                    >
                        <Upload className="dropzone-icon" size={48} aria-hidden="true" />
                        <div className="dropzone-text">{t('dropFilesHere')}</div>
                        <div className="dropzone-subtext">{t('orClickToUpload')}</div>
                    </button>
                    <div className="upload-modal-divider"><span>{t('or')}</span></div>
                    <button className="upload-modal-text-btn" onClick={() => setMode('text')}>
                        <Type size={18} aria-hidden="true" /> {t('insertText')}
                    </button>
                </>
            ) : (
                <div className="text-input-mode">
                    <label htmlFor="fu-modal-title" className="sr-only">{t('textTitle')}</label>
                    <input
                        id="fu-modal-title"
                        type="text"
                        placeholder={t('textTitle')}
                        value={textSourceTitle}
                        onChange={(e) => setTextSourceTitle(e.target.value)}
                    />
                    <label htmlFor="fu-modal-content" className="sr-only">{t('textContent')}</label>
                    <textarea
                        id="fu-modal-content"
                        placeholder={t('textContent')}
                        value={textSourceContent}
                        onChange={(e) => { setTextSourceContent(e.target.value); clearError('content'); }}
                        // eslint-disable-next-line jsx-a11y/no-autofocus -- focus first field on dialog open (WAI-ARIA dialog pattern)
                        autoFocus
                    />
                    {errors.content && <span className="field-error" role="alert">{errors.content}</span>}
                    <div className="text-input-actions">
                        <button className="text-input-back" onClick={() => setMode('dropzone')}>{t('backToUpload')}</button>
                        <button className="text-input-submit" onClick={() => { if (validate({ content: textSourceContent })) onTextSourceAdd(); }}>{t('addTextSource')}</button>
                    </div>
                </div>
            )}
        </SourceModal>
    );
};

export const FileUploadModal = memo(FileUploadModalComp);
