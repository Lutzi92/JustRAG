import React, { useState } from 'react';
import { motion } from 'framer-motion';
import { useReducedMotion, getMotionProps } from '../hooks/useReducedMotion';
import {
    X, Eye, Pencil, Plus,
    Save, Layout, Check, ArrowLeft
} from 'lucide-react';
import { MarkdownEditor } from './Studio/MarkdownEditor';
import { useTheme } from '../contexts/ThemeContext';
import { useIsMobileContext } from '../contexts/MobileContext';
import { openExternal } from '../utils/openExternal';

interface FoundPage {
    url: string;
    title: string;
    content: string;
    snippet?: string;
    isEdited?: boolean;
}

interface WebWorkspaceProps {
    type: 'websearch' | 'crawl';
    results: FoundPage[];
    selectedUrls: Set<string>;
    onToggleSelection: (url: string) => void;
    onToggleSelectAll: () => void;
    onAddSources: () => void;
    onClose: () => void;
    onUpdateResult: (url: string, newContent: string) => void;
    onStartEdit?: (url: string) => Promise<void>;
}

export const WebWorkspace: React.FC<WebWorkspaceProps> = ({
    type,
    results,
    selectedUrls,
    onToggleSelection,
    onToggleSelectAll,
    onAddSources,
    onClose,
    onUpdateResult,
    onStartEdit,
}) => {
    const { t } = useTheme();
    const isMobile = useIsMobileContext();
    const reducedMotion = useReducedMotion();
    const [mobileView, setMobileView] = useState<'list' | 'editor'>('list');
    const [activeResultUrl, setActiveResultUrl] = useState<string | null>(results[0]?.url ?? null);
    const activeResult = results.find(r => r.url === activeResultUrl);
    const [editContent, setEditContent] = useState(results[0]?.content || results[0]?.snippet || '');

    const handleEdit = async (result: FoundPage) => {
        setActiveResultUrl(result.url);
        setEditContent(result.content || result.snippet || '');
        if (isMobile) setMobileView('editor');
        // If content is missing, trigger fetch in parent
        if (!result.content) {
            await onStartEdit?.(result.url);
        }
    };

    // Auto-select first result when results change (new search/crawl)
    React.useEffect(() => {
        if (results.length > 0 && !results.find(r => r.url === activeResultUrl)) {
            setActiveResultUrl(results[0].url);
            setEditContent(results[0].content || results[0].snippet || '');
        }
    }, [results, activeResultUrl]);

    // Refresh edit content when the active result's content arrives/changes
    // (e.g. after a fetch). lastSyncedContent gates the sync to actual content
    // transitions so re-runs caused by the user typing (editContent changes)
    // never clobber an in-progress edit.
    const lastSyncedContentRef = React.useRef(activeResult?.content);
    React.useEffect(() => {
        const content = activeResult?.content;
        if (content === lastSyncedContentRef.current) return;
        lastSyncedContentRef.current = content;
        if (activeResult && !activeResult.isEdited && content !== editContent) {
            if (content !== undefined && content !== null) {
                setEditContent(content);
            }
        }
    }, [activeResult, editContent]);

    const handleSaveEdit = () => {
        if (activeResultUrl) {
            onUpdateResult(activeResultUrl, editContent);
            // We don't necessarily close the editor here,
            // but we update the underlying results in App.tsx
        }
    };

    return (
        <motion.div
            className="web-workspace"
            {...getMotionProps(reducedMotion)}
            initial={{ opacity: 0, scale: 0.98 }}
            animate={{ opacity: 1, scale: 1 }}
            exit={{ opacity: 0, scale: 0.98 }}
            transition={{ duration: 0.2, ease: "easeOut" }}
        >
            {/* Header */}
            <header className="web-workspace-header">
                <div className="header-left">
                    <div className="header-icon">
                        <Layout size={20} />
                    </div>
                    <div>
                        <h2>{type === 'websearch' ? t('websearch') : t('crawl')} - Workspace</h2>
                        <p className="header-subtitle">Vorschau und Bearbeitung der Ergebnisse</p>
                    </div>
                </div>
                <button onClick={onClose} className="web-workspace-close" title={t('close')} aria-label={t('close')}>
                    <X size={24} />
                </button>
            </header>

            <div className="web-workspace-body" style={isMobile ? { flexDirection: 'column' } : undefined}>
                {/* Left Sidebar: Results List */}
                <aside className="web-workspace-sidebar" style={isMobile ? { width: '100%', borderRight: 'none', display: isMobile && mobileView !== 'list' ? 'none' : 'flex' } : undefined}>
                    <div className="sidebar-header">
                        <h3>Ergebnisse ({results.length})</h3>
                        <button onClick={onToggleSelectAll} className="text-link">
                            {selectedUrls.size === results.length ? t('deselectAll') : t('selectAll')}
                        </button>
                    </div>

                    <div className="results-list">
                        {results.map((result, index) => (
                            <div
                                key={index}
                                className={`result-card ${activeResultUrl === result.url ? 'active' : ''}`}
                                role="button"
                                tabIndex={0}
                                onClick={() => handleEdit(result)}
                                onKeyDown={(e) => {
                                    // Only react to keys on the card itself, not on the
                                    // nested checkbox/buttons (their keys bubble up here).
                                    if (e.target !== e.currentTarget) return;
                                    if (e.key === 'Enter' || e.key === ' ') {
                                        e.preventDefault();
                                        handleEdit(result);
                                    }
                                }}
                            >
                                <div className="result-card-top">
                                    <input
                                        type="checkbox"
                                        checked={selectedUrls.has(result.url)}
                                        onChange={(e) => {
                                            e.stopPropagation();
                                            onToggleSelection(result.url);
                                        }}
                                        onClick={(e) => e.stopPropagation()}
                                    />
                                    <span className="result-title" title={result.title}>{result.title}</span>
                                </div>
                                <div className="result-url" title={result.url}>{result.url}</div>
                                <div className="result-actions">
                                    <button
                                        onClick={(e) => {
                                            e.stopPropagation();
                                            openExternal(result.url);
                                        }}
                                        className="action-btn"
                                        title={t('openOriginal')}
                                        aria-label={t('openOriginal')}
                                    >
                                        <Eye size={14} />
                                    </button>
                                    <button
                                        onClick={(e) => {
                                            e.stopPropagation();
                                            handleEdit(result);
                                        }}
                                        className="action-btn"
                                        title={t('editContent')}
                                        aria-label={t('editContent')}
                                    >
                                        <Pencil size={14} />
                                    </button>
                                    {result.isEdited && (
                                        <span className="edited-badge" title={t('edited')}>
                                            <Check size={10} />
                                        </span>
                                    )}
                                </div>
                            </div>
                        ))}
                    </div>

                    <div className="sidebar-footer">
                        <button
                            className="workspace-add-btn"
                            disabled={selectedUrls.size === 0}
                            onClick={onAddSources}
                        >
                            <Plus size={18} />
                            {t('addToSources')} ({selectedUrls.size})
                        </button>
                    </div>
                </aside>

                {/* Main Content: Editor */}
                <main className="web-workspace-main" style={isMobile && mobileView !== 'editor' ? { display: 'none' } : undefined}>
                    {activeResult ? (
                        <div className="editor-container">
                            <div className="editor-header">
                                {isMobile && (
                                    <button
                                        onClick={() => setMobileView('list')}
                                        style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--accent-primary)', display: 'flex', alignItems: 'center', gap: '4px', padding: '4px', marginRight: '8px' }}
                                    >
                                        <ArrowLeft size={16} /> {t('backToOverview')}
                                    </button>
                                )}
                                <div className="active-title">
                                    <strong>Bearbeite:</strong> {activeResult.title}
                                </div>
                                <button
                                    className="save-edit-btn"
                                    onClick={handleSaveEdit}
                                >
                                    <Save size={16} />
                                    Änderungen übernehmen
                                </button>
                            </div>
                            <div className="editor-wrapper">
                                <MarkdownEditor
                                    value={editContent}
                                    onChange={setEditContent}
                                    isAnalyzing={false}
                                />
                            </div>
                        </div>
                    ) : (
                        <div className="empty-editor">
                            <div className="empty-content">
                                <Layout size={48} opacity={0.2} />
                                <h3>Wähle ein Ergebnis zum Bearbeiten aus</h3>
                                <p>Klicke auf ein Ergebnis in der Liste links, um dessen Inhalt anzupassen, bevor du es zu deinen Quellen hinzufügst.</p>
                            </div>
                        </div>
                    )}
                </main>
            </div>

            <style>{`
                .web-workspace {
                    position: fixed;
                    top: 0;
                    left: 0;
                    right: 0;
                    bottom: 0;
                    background: var(--bg-primary);
                    z-index: 2000;
                    display: flex;
                    flex-direction: column;
                }

                .web-workspace-header {
                    padding: 1rem 2rem;
                    background: var(--bg-secondary);
                    border-bottom: 1px solid var(--border-color);
                    display: flex;
                    justify-content: space-between;
                    align-items: center;
                }

                .header-left {
                    display: flex;
                    align-items: center;
                    gap: 1rem;
                }

                .header-icon {
                    width: 40px;
                    height: 40px;
                    background: var(--tag-bg);
                    color: var(--accent-primary);
                    border-radius: 10px;
                    display: flex;
                    align-items: center;
                    justify-content: center;
                }

                .web-workspace-header h2 {
                    margin: 0;
                    font-size: 1.25rem;
                }

                .header-subtitle {
                    margin: 0;
                    font-size: 0.85rem;
                    color: var(--text-secondary);
                }

                .web-workspace-close {
                    background: none;
                    border: none;
                    cursor: pointer;
                    color: var(--text-secondary);
                    padding: 8px;
                    border-radius: 50%;
                    transition: background 0.2s, color 0.2s;
                }

                .web-workspace-close:hover {
                    background: var(--bg-hover);
                    color: var(--text-primary);
                }

                .web-workspace-body {
                    flex: 1;
                    display: flex;
                    overflow: hidden;
                }

                .web-workspace-sidebar {
                    width: 320px;
                    background: var(--bg-secondary);
                    border-right: 1px solid var(--border-color);
                    display: flex;
                    flex-direction: column;
                }

                .sidebar-header {
                    padding: 1.5rem;
                    display: flex;
                    justify-content: space-between;
                    align-items: center;
                    border-bottom: 1px solid var(--border-color);
                }

                .sidebar-header h3 {
                    margin: 0;
                    font-size: 0.9rem;
                    text-transform: uppercase;
                    letter-spacing: 0.05em;
                    color: var(--text-secondary);
                }

                .text-link {
                    background: none;
                    border: none;
                    color: var(--accent-primary);
                    font-size: 0.8rem;
                    cursor: pointer;
                    font-weight: 500;
                }

                .results-list {
                    flex: 1;
                    overflow-y: auto;
                    padding: 1rem;
                    display: flex;
                    flex-direction: column;
                    gap: 0.75rem;
                }

                .result-card {
                    padding: 0.85rem;
                    background: var(--bg-primary);
                    border: 1px solid var(--border-color);
                    border-radius: 10px;
                    cursor: pointer;
                    transition: border-color 0.2s, background 0.2s, box-shadow 0.2s;
                }

                .result-card:hover {
                    border-color: var(--accent-primary);
                    background: var(--bg-hover);
                }

                .result-card.active {
                    border-color: var(--accent-primary);
                    background: var(--source-card-active-bg);
                    box-shadow: 0 2px 8px rgba(var(--accent-primary-rgb), 0.1);
                }

                .result-card-top {
                    display: flex;
                    align-items: center;
                    gap: 0.5rem;
                    margin-bottom: 0.25rem;
                }

                .result-title {
                    font-size: 0.85rem;
                    font-weight: 600;
                    white-space: nowrap;
                    overflow: hidden;
                    text-overflow: ellipsis;
                }

                .result-url {
                    font-size: 0.75rem;
                    color: var(--text-secondary);
                    white-space: nowrap;
                    overflow: hidden;
                    text-overflow: ellipsis;
                    margin-bottom: 0.5rem;
                    padding-left: 1.5rem;
                }

                .result-actions {
                    display: flex;
                    gap: 0.5rem;
                    padding-left: 1.5rem;
                    align-items: center;
                }

                .action-btn {
                    background: var(--bg-secondary);
                    border: 1px solid var(--border-color);
                    color: var(--text-secondary);
                    padding: 4px;
                    border-radius: 4px;
                    cursor: pointer;
                    transition: border-color 0.2s, color 0.2s;
                }

                .action-btn:hover {
                    border-color: var(--accent-primary);
                    color: var(--accent-primary);
                }

                .edited-badge {
                    background: var(--success-text);
                    color: white;
                    width: 16px;
                    height: 16px;
                    border-radius: 50%;
                    display: flex;
                    align-items: center;
                    justify-content: center;
                }

                .sidebar-footer {
                    padding: 1.5rem;
                    border-top: 1px solid var(--border-color);
                }

                .workspace-add-btn {
                    width: 100%;
                    padding: 0.85rem;
                    background: var(--accent-primary);
                    color: white;
                    border: none;
                    border-radius: 10px;
                    font-weight: 600;
                    cursor: pointer;
                    display: flex;
                    align-items: center;
                    justify-content: center;
                    gap: 0.5rem;
                    transition: background 0.2s, transform 0.2s, box-shadow 0.2s;
                }

                .workspace-add-btn:hover:not(:disabled) {
                    background: var(--accent-secondary);
                    transform: translateY(-1px);
                    box-shadow: 0 4px 12px rgba(var(--accent-primary-rgb), 0.3);
                }

                .workspace-add-btn:disabled {
                    opacity: 0.5;
                    cursor: not-allowed;
                }

                .web-workspace-main {
                    flex: 1;
                    background: var(--bg-primary);
                    position: relative;
                    overflow: hidden;
                }

                .editor-container {
                    height: 100%;
                    display: flex;
                    flex-direction: column;
                }

                .editor-header {
                    padding: 0.75rem 2rem;
                    background: var(--bg-secondary);
                    border-bottom: 1px solid var(--border-color);
                    display: flex;
                    justify-content: space-between;
                    align-items: center;
                }

                .active-title {
                    font-size: 0.9rem;
                    max-width: 60%;
                    overflow: hidden;
                    text-overflow: ellipsis;
                    white-space: nowrap;
                }

                .save-edit-btn {
                    display: flex;
                    align-items: center;
                    gap: 8px;
                    padding: 0.5rem 1rem;
                    background: var(--bg-primary);
                    border: 1px solid var(--border-color);
                    border-radius: 6px;
                    font-size: 0.85rem;
                    font-weight: 500;
                    cursor: pointer;
                    transition: border-color 0.2s, color 0.2s;
                }

                .save-edit-btn:hover {
                    border-color: var(--accent-primary);
                    color: var(--accent-primary);
                }

                .editor-wrapper {
                    flex: 1;
                    padding: 1.5rem;
                    overflow: hidden;
                }

                .empty-editor {
                    height: 100%;
                    display: flex;
                    align-items: center;
                    justify-content: center;
                    text-align: center;
                    padding: 3rem;
                }

                .empty-content h3 {
                    margin: 1.5rem 0 0.5rem;
                }

                .empty-content p {
                    color: var(--text-secondary);
                    max-width: 400px;
                }
            `}</style>
        </motion.div>
    );
};
