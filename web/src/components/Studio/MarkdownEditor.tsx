import React, { useState, useRef } from 'react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import {
    Bold, Italic, List, Heading1, Heading2,
    Sparkles, Columns, Maximize2, Save,
    Check, Loader, FileText, Eye, Pencil
} from 'lucide-react';
import { useIsMobileContext } from '../../contexts/MobileContext';
import { useTheme } from '../../contexts/ThemeContext';

interface MarkdownEditorProps {
    value: string;
    onChange: (value: string) => void;
    onAnalyze?: () => void;
    onSave?: () => void;
    isAnalyzing?: boolean;
    saveStatus?: 'idle' | 'saving' | 'saved' | 'error';
    onCopy?: () => void;
    onExportDocx?: () => void;
    onDownloadPdf?: () => void;
    isCopied?: boolean;
    isGeneratingPdf?: boolean;
}

export const MarkdownEditor: React.FC<MarkdownEditorProps> = ({
    value,
    onChange,
    onAnalyze,
    onSave,
    isAnalyzing = false,
    saveStatus = 'idle',
    onCopy,
    onExportDocx,
    onDownloadPdf,
    isCopied = false,
    isGeneratingPdf = false
}) => {

    const isMobile = useIsMobileContext();
    const { t } = useTheme();
    const [viewMode, setViewMode] = useState<'split' | 'edit' | 'preview'>(isMobile ? 'edit' : 'preview');
    const textareaRef = useRef<HTMLTextAreaElement>(null);
    const [splitRatio, setSplitRatio] = useState(0.5);
    const [isDragging, setIsDragging] = useState(false);
    const containerRef = useRef<HTMLDivElement>(null);

    const previewRef = useRef<HTMLDivElement>(null);
    const isScrollingRef = useRef<'editor' | 'preview' | null>(null);

    // Helper to insert markdown syntax
    const insertSyntax = (prefix: string, suffix: string = '') => {
        if (!textareaRef.current) return;

        const start = textareaRef.current.selectionStart;
        const end = textareaRef.current.selectionEnd;
        const text = textareaRef.current.value;
        const selected = text.substring(start, end);

        const newText = text.substring(0, start) + prefix + selected + suffix + text.substring(end);
        onChange(newText);

        // Restore focus and selection
        setTimeout(() => {
            if (textareaRef.current) {
                textareaRef.current.focus();
                textareaRef.current.setSelectionRange(start + prefix.length, end + prefix.length);
            }
        }, 0);
    };

    const handleMouseDown = (e: React.MouseEvent) => {
        setIsDragging(true);
        e.preventDefault();
    };

    React.useEffect(() => {
        const handleMouseMove = (e: MouseEvent) => {
            if (!isDragging || !containerRef.current) return;

            const containerRect = containerRef.current.getBoundingClientRect();
            const newRatio = (e.clientX - containerRect.left) / containerRect.width;

            // Limit ratio between 0.2 and 0.8 to prevent panels from disappearing
            const clampedRatio = Math.max(0.2, Math.min(0.8, newRatio));
            setSplitRatio(clampedRatio);
        };

        const handleMouseUp = () => {
            setIsDragging(false);
        };

        if (isDragging) {
            document.addEventListener('mousemove', handleMouseMove);
            document.addEventListener('mouseup', handleMouseUp);
        }

        return () => {
            document.removeEventListener('mousemove', handleMouseMove);
            document.removeEventListener('mouseup', handleMouseUp);
        };
    }, [isDragging]);

    // Synchronous scrolling
    const handleEditorScroll = () => {
        if (isScrollingRef.current === 'preview') return;

        const editor = textareaRef.current;
        const preview = previewRef.current;

        if (editor && preview) {
            isScrollingRef.current = 'editor';

            // Calculate percentage
            const percentage = editor.scrollTop / (editor.scrollHeight - editor.clientHeight);

            // Apply to preview
            preview.scrollTop = percentage * (preview.scrollHeight - preview.clientHeight);

            // Reset lock after a short delay
            setTimeout(() => {
                if (isScrollingRef.current === 'editor') {
                    isScrollingRef.current = null;
                }
            }, 50);
        }
    };

    const handlePreviewScroll = () => {
        if (isScrollingRef.current === 'editor') return;

        const editor = textareaRef.current;
        const preview = previewRef.current;

        if (editor && preview) {
            isScrollingRef.current = 'preview';

            // Calculate percentage
            const percentage = preview.scrollTop / (preview.scrollHeight - preview.clientHeight);

            // Apply to editor
            editor.scrollTop = percentage * (editor.scrollHeight - editor.clientHeight);

            // Reset lock after a short delay
            setTimeout(() => {
                if (isScrollingRef.current === 'preview') {
                    isScrollingRef.current = null;
                }
            }, 50);
        }
    };

    return (
        <div style={{
            display: 'flex',
            flexDirection: 'column',
            height: '100%',
            border: '1px solid var(--border-color)',
            borderRadius: '8px',
            overflow: 'hidden',
            background: 'var(--bg-primary)'
        }}>
            {/* Toolbar */}
            <div style={{
                padding: '0.5rem',
                borderBottom: '1px solid var(--border-color)',
                background: 'var(--bg-secondary)',
                display: 'flex',
                gap: '0.5rem',
                alignItems: 'center',
                flexWrap: 'wrap'
            }}>
                {viewMode !== 'preview' && (
                    <>
                        <div style={{ display: 'flex', gap: '0.25rem' }}>
                            <button className="editor-btn" onClick={() => insertSyntax('**', '**')} title={t('bold')} aria-label={t('bold')}>
                                <Bold size={16} />
                            </button>
                            <button className="editor-btn" onClick={() => insertSyntax('*', '*')} title={t('italic')} aria-label={t('italic')}>
                                <Italic size={16} />
                            </button>
                            <button className="editor-btn" onClick={() => insertSyntax('# ')} title={t('heading1')} aria-label={t('heading1')}>
                                <Heading1 size={16} />
                            </button>
                            <button className="editor-btn" onClick={() => insertSyntax('## ')} title={t('heading2')} aria-label={t('heading2')}>
                                <Heading2 size={16} />
                            </button>
                            <button className="editor-btn" onClick={() => insertSyntax('- ')} title={t('list')} aria-label={t('list')}>
                                <List size={16} />
                            </button>
                        </div>

                        <div style={{ width: '1px', background: 'var(--border-color)', height: '20px', margin: '0 0.5rem' }} />
                    </>
                )}

                {onCopy && (
                    <button
                        className="editor-btn"
                        onClick={onCopy}
                        title={t('copyToClipboard')}
                        aria-label={t('copyToClipboard')}
                        style={isCopied ? { color: 'var(--success-color, green)' } : undefined}
                    >
                        <span className="icon-swap" key={isCopied ? 'check' : 'file'}>{isCopied ? <Check size={16} /> : <FileText size={16} />}</span>
                    </button>
                )}

                {onExportDocx && (
                    <button
                        className="editor-btn"
                        onClick={onExportDocx}
                        title={t('exportDocx')}
                        aria-label={t('exportDocx')}
                    >
                        <FileText size={16} /> <span style={{ fontSize: '0.7em', marginLeft: -2 }}>DOC</span>
                    </button>
                )}

                {onDownloadPdf && (
                    <button
                        className="editor-btn"
                        onClick={onDownloadPdf}
                        title={t('downloadPdf')}
                        aria-label={t('downloadPdf')}
                        disabled={isGeneratingPdf}
                        style={{ opacity: isGeneratingPdf ? 0.5 : 1 }}
                    >
                        <FileText size={16} /> <span style={{ fontSize: '0.7em', marginLeft: -2 }}>PDF</span>
                    </button>
                )}

                {(onCopy || onExportDocx || onDownloadPdf) && (
                    <div style={{ width: '1px', background: 'var(--border-color)', height: '20px', margin: '0 0.5rem' }} />
                )}

                {onAnalyze && (
                    <button
                        className="editor-btn primary"
                        onClick={onAnalyze}
                        disabled={isAnalyzing}
                        title={t('analyzeWithAi')}
                        aria-label={t('analyzeWithAi')}
                        style={{
                            color: 'var(--accent-primary)',
                            background: 'color-mix(in srgb, var(--accent-primary) 10%, transparent)',
                            borderColor: 'var(--accent-primary)'
                        }}
                    >
                        <Sparkles size={16} className={isAnalyzing ? 'spin' : ''} />
                        <span style={{ fontSize: '0.85rem', fontWeight: 500 }}>
                            {isAnalyzing ? 'Analyzing...' : 'Analyze'}
                        </span>
                    </button>
                )}

                {onSave && (
                    <button
                        className={`editor-btn ${saveStatus === 'saved' ? 'success' : ''}`}
                        onClick={onSave}
                        title={t('saveChanges')}
                        aria-label={t('saveChanges')}
                        disabled={saveStatus === 'saving'}
                        style={saveStatus === 'saved' ? {
                            color: 'var(--success-text)',
                            borderColor: 'var(--success-text)',
                            background: 'rgba(24, 128, 56, 0.05)'
                        } : undefined}
                    >
                        {saveStatus === 'saving' ? (
                            <Loader size={16} className="spin" />
                        ) : saveStatus === 'saved' ? (
                            <Check size={16} />
                        ) : (
                            <Save size={16} />
                        )}
                        <span style={{ fontSize: '0.85rem', fontWeight: 500 }}>
                            {saveStatus === 'saving' ? 'Saving...' : saveStatus === 'saved' ? 'Saved' : 'Save'}
                        </span>
                    </button>
                )}
            </div>

            {/* Editor Area */}
            <div
                ref={containerRef}
                style={{ flex: 1, display: 'flex', overflow: 'hidden' }}
            >
                {/* Text Input */}
                {(viewMode === 'split' || viewMode === 'edit') && (
                    <div style={{
                        flex: viewMode === 'split' ? `0 0 ${splitRatio * 100}%` : 1,
                        display: 'flex',
                        flexDirection: 'column',
                        overflow: 'hidden',
                        minWidth: 0
                    }}>
                        <div style={{
                            display: 'flex',
                            justifyContent: 'flex-end',
                            padding: '4px 4px 0 4px',
                            flexShrink: 0
                        }}>
                            <button
                                className="overlay-btn"
                                onClick={() => setViewMode(isMobile ? 'preview' : (viewMode === 'split' ? 'edit' : 'split'))}
                                title={isMobile ? "Preview" : (viewMode === 'split' ? "Maximize Editor" : "Split View")}
                                style={{
                                    background: 'var(--bg-secondary)',
                                    border: '1px solid var(--border-color)',
                                    borderRadius: '4px',
                                    padding: '4px',
                                    cursor: 'pointer',
                                    color: 'var(--text-secondary)'
                                }}
                            >
                                {isMobile ? <Eye size={16} /> : (viewMode === 'split' ? <Maximize2 size={16} /> : <Columns size={16} />)}
                            </button>
                        </div>
                        <textarea
                            ref={textareaRef}
                            value={value}
                            onChange={(e) => onChange(e.target.value)}
                            onScroll={handleEditorScroll}
                            placeholder="Start writing your research notes here..."
                            style={{
                                flex: 1,
                                padding: '0 1.5rem 1.5rem 1.5rem',
                                border: 'none',
                                resize: 'none',
                                outline: 'none',
                                fontSize: '1rem',
                                lineHeight: '1.6',
                                fontFamily: "'JetBrains Mono', monospace",
                                background: 'var(--bg-primary)',
                                color: 'var(--text-primary)',
                            }}
                        />
                    </div>
                )}

                {/* Resizer Handle */}
                {viewMode === 'split' && !isMobile && (
                    <div
                        role="slider"
                        aria-label="Resize editor/preview split"
                        aria-valuemin={20}
                        aria-valuemax={80}
                        aria-valuenow={Math.round(splitRatio * 100)}
                        tabIndex={0}
                        onMouseDown={handleMouseDown}
                        onKeyDown={(e) => {
                            if (e.key === 'ArrowLeft') {
                                e.preventDefault();
                                setSplitRatio(r => Math.max(0.2, r - 0.05));
                            } else if (e.key === 'ArrowRight') {
                                e.preventDefault();
                                setSplitRatio(r => Math.min(0.8, r + 0.05));
                            }
                        }}
                        style={{
                            width: '4px',
                            background: isDragging ? 'var(--accent-primary)' : 'var(--border-color)',
                            cursor: 'col-resize',
                            zIndex: 10,
                            transition: 'background 0.2s',
                            flexShrink: 0
                        }}
                    />
                )}

                {/* Preview */}
                {(viewMode === 'split' || viewMode === 'preview') && (
                    <div style={{
                        flex: 1, // Takes remaining space in split mode
                        display: 'flex',
                        flexDirection: 'column',
                        background: 'var(--bg-primary)',
                        overflow: 'hidden',
                        minWidth: 0
                    }}>
                        <div style={{
                            display: 'flex',
                            justifyContent: 'flex-end',
                            padding: '4px 4px 0 4px',
                            flexShrink: 0
                        }}>
                            <button
                                className="overlay-btn"
                                onClick={() => setViewMode(isMobile ? 'edit' : (viewMode === 'split' ? 'preview' : 'split'))}
                                title={isMobile ? "Edit" : (viewMode === 'split' ? "Maximize Preview" : t('editContent') || "Edit")}
                                style={{
                                    background: 'var(--bg-secondary)',
                                    border: '1px solid var(--border-color)',
                                    borderRadius: '4px',
                                    padding: viewMode === 'preview' && !isMobile ? '4px 10px' : '4px',
                                    cursor: 'pointer',
                                    color: 'var(--text-secondary)',
                                    display: 'flex',
                                    alignItems: 'center',
                                    gap: '4px'
                                }}
                            >
                                {isMobile ? <Pencil size={16} /> : viewMode === 'split' ? <Maximize2 size={16} /> : (
                                    <>
                                        <Pencil size={14} />
                                        <span style={{ fontSize: '0.8rem', fontWeight: 500 }}>{t('editContent') || 'Edit'}</span>
                                    </>
                                )}
                            </button>
                        </div>
                        <div
                            className="markdown-preview"
                            ref={previewRef}
                            onScroll={handlePreviewScroll}
                            style={{
                                flex: 1,
                                padding: '0 1.5rem 1.5rem 1.5rem',
                                overflowY: 'auto',
                                color: 'var(--text-primary)'
                            }}>
                            <ReactMarkdown
                                remarkPlugins={[remarkGfm]}
                                components={{
                                    code({ node, inline, className, children, ...props }: React.HTMLAttributes<HTMLElement> & { inline?: boolean; node?: unknown }) {
                                        // `node` is react-markdown's AST node; destructured only so it
                                        // isn't spread onto the DOM element below.
                                        void node;
                                        return !inline ? (
                                            <pre style={{ background: 'var(--bg-primary)', padding: '1rem', borderRadius: '4px', overflowX: 'auto' }}>
                                                <code className={className} {...props}>
                                                    {children}
                                                </code>
                                            </pre>
                                        ) : (
                                            <code className={className} style={{ background: 'var(--bg-primary)', padding: '0.2rem 0.4rem', borderRadius: '3px' }} {...props}>
                                                {children}
                                            </code>
                                        )
                                    }
                                }}
                            >
                                {value || '*Preview will appear here*'}
                            </ReactMarkdown>
                        </div>
                    </div>
                )}
            </div>

            <style>{`
                .editor-btn {
                    display: flex;
                    align-items: center;
                    justify-content: center;
                    gap: 6px;
                    padding: 6px 10px;
                    background: transparent;
                    border: 1px solid transparent;
                    border-radius: 4px;
                    cursor: pointer;
                    color: var(--text-secondary);
                    transition: background 0.2s, color 0.2s, border-color 0.2s;
                }
                .editor-btn:hover {
                    background: var(--bg-hover);
                    color: var(--text-primary);
                }
                .editor-btn.active {
                    background: var(--bg-hover);
                    color: var(--accent-primary);
                }
                .editor-btn:disabled {
                    opacity: 0.5;
                    cursor: not-allowed;
                }
                .overlay-btn:hover {
                    opacity: 1;
                    background: var(--bg-hover);
                }
                .spin {
                    animation: spin 1s linear infinite;
                }
                @keyframes spin {
                    from { transform: rotate(0deg); }
                    to { transform: rotate(360deg); }
                }

                /* Markdown Preview Styles */
                .markdown-preview h1 { font-size: 1.8rem; margin-bottom: 1rem; border-bottom: 1px solid var(--border-color); padding-bottom: 0.5rem; }
                .markdown-preview h2 { font-size: 1.5rem; margin-top: 1.5rem; margin-bottom: 1rem; }
                .markdown-preview p { margin-bottom: 1rem; line-height: 1.6; }
                .markdown-preview ul, .markdown-preview ol { padding-left: 1.5rem; margin-bottom: 1rem; }
                .markdown-preview li { margin-bottom: 0.5rem; }
                .markdown-preview blockquote { border-left: 4px solid var(--accent-primary); padding-left: 1rem; color: var(--text-secondary); font-style: italic; }
                .markdown-preview table { width: 100%; border-collapse: collapse; margin-bottom: 1rem; }
                .markdown-preview th, .markdown-preview td { border: 1px solid var(--border-color); padding: 0.75rem; text-align: left; }
                .markdown-preview th { background: var(--bg-secondary); font-weight: 600; }
            `}</style>
        </div>
    );
};
