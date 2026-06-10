import React, { memo, useState, useCallback } from 'react';
import { Loader2, Plus, Layout, X } from 'lucide-react';
import { motion } from 'framer-motion';
import { useTheme } from '../../contexts/ThemeContext';
import { useReducedMotion, getMotionProps } from '../../hooks/useReducedMotion';
import { SourceModal } from './SourceModal';

type SearchMode = 'fast' | 'deep';

interface WebSearchModalProps {
    show: boolean;
    onClose: () => void;
    toolInput: string;
    setToolInput: (val: string) => void;
    toolLoading: boolean;
    onToolSubmit: () => void;
    searchResultsCount: number;
    setSearchResultsCount: (n: number) => void;
    searchResults: { title: string; url: string; snippet: string }[];
    onOpenWorkspace: () => void;
    setToolTab: (tab: 'websearch' | 'crawl' | 'research' | 'rss') => void;
    webResearchRunning: boolean;
    webResearchStatus: string | null;
    webResearchProgress: { step: number; total: number };
    onCancelWebResearch: () => void;
}

const WebSearchModalComp: React.FC<WebSearchModalProps> = ({
    show, onClose, toolInput, setToolInput, toolLoading, onToolSubmit,
    searchResultsCount, setSearchResultsCount, searchResults, onOpenWorkspace,
    setToolTab,
    webResearchRunning, webResearchStatus, webResearchProgress, onCancelWebResearch,
}) => {
    const { t } = useTheme();
    const reducedMotion = useReducedMotion();
    const [mode, setMode] = useState<SearchMode>('fast');

    const handleModeChange = useCallback((m: SearchMode) => {
        setMode(m);
        setToolTab(m === 'fast' ? 'websearch' : 'research');
    }, [setToolTab]);

    const handleSubmit = useCallback(() => {
        setToolTab(mode === 'fast' ? 'websearch' : 'research');
        onToolSubmit();
    }, [mode, setToolTab, onToolSubmit]);

    const handleKeyDown = useCallback((e: React.KeyboardEvent) => {
        if (e.key === 'Enter' && toolInput.trim() && !toolLoading && !webResearchRunning) {
            if (mode === 'fast') handleSubmit();
        }
    }, [toolInput, toolLoading, webResearchRunning, mode, handleSubmit]);

    return (
        <SourceModal title={t('websearch')} show={show} onClose={onClose}>
            <div className="source-modal__mode-toggle">
                <button
                    className={`source-modal__mode-btn ${mode === 'fast' ? 'source-modal__mode-btn--active' : ''}`}
                    onClick={() => handleModeChange('fast')}
                >
                    {t('websearchFast')}
                </button>
                <button
                    className={`source-modal__mode-btn ${mode === 'deep' ? 'source-modal__mode-btn--active' : ''}`}
                    onClick={() => handleModeChange('deep')}
                >
                    {t('websearchDeep')}
                </button>
            </div>
            <p style={{ margin: 0, fontSize: '0.85rem', color: 'var(--text-secondary)' }}>
                {mode === 'fast' ? t('websearchFastDesc') : t('websearchDeepDesc')}
            </p>

            {mode === 'fast' ? (
                <input
                    type="text"
                    placeholder={t('enterSearchTerm')}
                    value={toolInput}
                    onChange={(e) => setToolInput(e.target.value)}
                    onKeyDown={handleKeyDown}
                    aria-label={t('enterSearchTerm')}
                    className="sidebar-left__tools-input"
                    // eslint-disable-next-line jsx-a11y/no-autofocus -- focus first field on dialog open (WAI-ARIA dialog pattern)
                    autoFocus
                />
            ) : (
                <textarea
                    placeholder={t('enterTopic')}
                    value={toolInput}
                    onChange={(e) => setToolInput(e.target.value)}
                    disabled={webResearchRunning}
                    aria-label={t('enterTopic')}
                    className="sidebar-left__tools-input sidebar-left__tools-textarea"
                    style={{ opacity: webResearchRunning ? 0.6 : 1 }}
                    // eslint-disable-next-line jsx-a11y/no-autofocus -- focus first field on dialog open (WAI-ARIA dialog pattern)
                    autoFocus
                />
            )}

            {mode === 'fast' && (
                <div className="sidebar-left__slider-row">
                    <label htmlFor="ws-modal-results-count">{t('results')}: {searchResultsCount}</label>
                    <input
                        id="ws-modal-results-count"
                        type="range" min="1" max="10" step="1"
                        value={searchResultsCount}
                        onChange={(e) => setSearchResultsCount(parseInt(e.target.value, 10))}
                        className="sidebar-left__slider"
                    />
                </div>
            )}

            {mode === 'deep' && (webResearchRunning || webResearchStatus) ? (
                <div className="sidebar-left__research-status-wrap">
                    <div className="sidebar-left__research-progress-track">
                        <motion.div
                            {...getMotionProps(reducedMotion)}
                            initial={{ width: 0 }}
                            animate={{ width: webResearchRunning ? `${Math.max(5, (webResearchProgress.step / (webResearchProgress.total || 1)) * 100)}%` : '100%' }}
                            transition={{ duration: 0.3 }}
                            style={{ height: '100%', background: webResearchRunning ? 'var(--accent-primary)' : 'var(--success-text, #2d8f4e)' }}
                        />
                    </div>
                    <div className="sidebar-left__research-status-row">
                        <span className="sidebar-left__research-status-text">
                            {webResearchStatus}
                            {webResearchRunning && webResearchProgress.step > 0 && ` (${webResearchProgress.step}/${webResearchProgress.total})`}
                        </span>
                        {webResearchRunning && (
                            <button onClick={onCancelWebResearch} className="secondary-button sidebar-left__research-cancel" title={t('cancelResearch')}>
                                <X size={12} /> {t('cancelResearch')}
                            </button>
                        )}
                    </div>
                </div>
            ) : (
                <button
                    onClick={handleSubmit}
                    disabled={toolLoading || !toolInput.trim() || webResearchRunning}
                    className="search-button"
                >
                    <span className="icon-swap" key={toolLoading ? 'loading' : 'icon'}>
                        {toolLoading ? <Loader2 className="animate-spin" size={16} /> : <Plus size={16} aria-hidden="true" />}
                    </span>
                    {mode === 'fast' ? t('startWebsearch') : t('startResearch')}
                </button>
            )}

            {mode === 'fast' && searchResults.length > 0 && (
                <div className="sidebar-left__workspace-card">
                    <div className="sidebar-left__workspace-header">
                        <span className="sidebar-left__workspace-count">{searchResults.length} {t('results')}</span>
                    </div>
                    <button onClick={onOpenWorkspace} className="sidebar-left__workspace-btn">
                        <Layout size={14} /> {t('openWorkspace')}
                    </button>
                </div>
            )}
        </SourceModal>
    );
};

export const WebSearchModal = memo(WebSearchModalComp);
