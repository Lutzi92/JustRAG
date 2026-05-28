import { useState, useRef, useEffect } from 'react';
import { Check, X } from 'lucide-react';
import { useTheme } from '../contexts/ThemeContext';

interface InlineMessageEditorProps {
    initialContent: string;
    onSave: (newContent: string) => void;
    onCancel: () => void;
}

export function InlineMessageEditor({ initialContent, onSave, onCancel }: InlineMessageEditorProps) {
    const { t } = useTheme();
    const [content, setContent] = useState(initialContent);
    const textareaRef = useRef<HTMLTextAreaElement>(null);

    useEffect(() => {
        if (textareaRef.current) {
            textareaRef.current.focus();
            textareaRef.current.style.height = 'auto';
            textareaRef.current.style.height = `${textareaRef.current.scrollHeight}px`;
        }
    }, []);

    const handleKeyDown = (e: React.KeyboardEvent) => {
        if (e.key === 'Enter' && !e.shiftKey) {
            e.preventDefault();
            if (content.trim()) onSave(content.trim());
        }
        if (e.key === 'Escape') {
            onCancel();
        }
    };

    return (
        <div style={{
            background: 'var(--bg-secondary)',
            border: '1px solid var(--accent-primary)',
            borderRadius: '12px',
            padding: '0.75rem',
        }}>
            <textarea
                ref={textareaRef}
                value={content}
                onChange={(e) => {
                    setContent(e.target.value);
                    e.target.style.height = 'auto';
                    e.target.style.height = `${e.target.scrollHeight}px`;
                }}
                onKeyDown={handleKeyDown}
                style={{
                    width: '100%',
                    border: 'none',
                    background: 'transparent',
                    color: 'var(--text-primary)',
                    fontSize: '0.95rem',
                    lineHeight: '1.5',
                    resize: 'none',
                    outline: 'none',
                    fontFamily: 'inherit',
                    minHeight: '60px',
                }}
            />
            <div style={{
                display: 'flex',
                justifyContent: 'flex-end',
                gap: '8px',
                marginTop: '8px',
            }}>
                <button
                    onClick={onCancel}
                    style={{
                        background: 'var(--tag-bg)',
                        border: '1px solid var(--border-color)',
                        borderRadius: '6px',
                        padding: '4px 12px',
                        cursor: 'pointer',
                        color: 'var(--text-secondary)',
                        fontSize: '0.85rem',
                        display: 'flex',
                        alignItems: 'center',
                        gap: '4px',
                    }}
                >
                    <X size={14} /> {t('cancel')}
                </button>
                <button
                    onClick={() => content.trim() && onSave(content.trim())}
                    disabled={!content.trim() || content.trim() === initialContent}
                    style={{
                        background: 'var(--accent-primary)',
                        border: 'none',
                        borderRadius: '6px',
                        padding: '4px 12px',
                        cursor: content.trim() && content.trim() !== initialContent ? 'pointer' : 'default',
                        color: 'white',
                        fontSize: '0.85rem',
                        display: 'flex',
                        alignItems: 'center',
                        gap: '4px',
                        opacity: content.trim() && content.trim() !== initialContent ? 1 : 0.5,
                    }}
                >
                    <Check size={14} /> {t('saveAndSubmit')}
                </button>
            </div>
        </div>
    );
}
