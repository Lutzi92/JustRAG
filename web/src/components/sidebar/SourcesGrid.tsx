import React, { memo, useMemo, useState, useCallback } from 'react';
import { Search, Globe, Rss, Upload, GraduationCap, BookOpen, Loader2, X, ArrowRight, GitBranch } from 'lucide-react';
import { useTheme } from '../../contexts/ThemeContext';
import { useAuth } from '../../contexts/AuthContext';
import './SourcesGrid.css';

export type SourceType = 'websearch' | 'crawl' | 'research' | 'rss' | 'upload' | 'academic' | 'confluence' | 'gitrepo';

interface WebSearchProps {
    toolInput: string;
    setToolInput: (val: string) => void;
    toolLoading: boolean;
    onSubmit: (tabOverride?: 'websearch' | 'crawl' | 'research' | 'rss') => void;
    setToolTab: (tab: 'websearch' | 'crawl' | 'research' | 'rss') => void;
    webResearchRunning: boolean;
    webResearchStatus: string | null;
    webResearchProgress: { step: number; total: number };
    onCancelWebResearch: () => void;
    hasResults?: boolean;
    onOpenWorkspace?: () => void;
}

interface SourcesGridProps {
    onSelect: (type: SourceType) => void;
    webSearch?: WebSearchProps;
}

type SearchMode = 'fast' | 'deep';

const gridItems: { type: SourceType; icon: typeof Search; labelKey: string }[] = [
    { type: 'crawl', icon: Globe, labelKey: 'crawl' },
    { type: 'rss', icon: Rss, labelKey: 'rss' },
    { type: 'upload', icon: Upload, labelKey: 'uploadFile' },
    { type: 'confluence', icon: BookOpen, labelKey: 'confluence' },
    { type: 'gitrepo', icon: GitBranch, labelKey: 'gitRepo' },
    { type: 'academic', icon: GraduationCap, labelKey: 'justFind' },
];

const SourcesGridComp: React.FC<SourcesGridProps> = ({ onSelect, webSearch }) => {
    const { t } = useTheme();
    const { siteConfigs } = useAuth();
    const [mode, setMode] = useState<SearchMode>('fast');

    const items = useMemo(() => {
        const confluenceEnabled = siteConfigs?.confluence_enabled === 'true';
        const academicEnabled = siteConfigs?.academic_search_enabled === 'true';
        const gitRepoEnabled = siteConfigs?.git_repo_enabled === 'true';
        return gridItems.filter(i => {
            if (i.type === 'confluence' && !confluenceEnabled) return false;
            if (i.type === 'academic' && !academicEnabled) return false;
            if (i.type === 'gitrepo' && !gitRepoEnabled) return false;
            return true;
        });
    }, [siteConfigs?.confluence_enabled, siteConfigs?.academic_search_enabled, siteConfigs?.git_repo_enabled]);

    const webSearchEnabled = siteConfigs?.web_search_enabled === 'true';

    const handleModeChange = useCallback((m: SearchMode) => {
        setMode(m);
        webSearch?.setToolTab(m === 'fast' ? 'websearch' : 'research');
    }, [webSearch]);

    const handleSubmit = useCallback(() => {
        if (!webSearch || !webSearch.toolInput.trim()) return;
        const tab = mode === 'fast' ? 'websearch' : 'research';
        webSearch.setToolTab(tab);
        webSearch.onSubmit(tab);
    }, [mode, webSearch]);

    const handleKeyDown = useCallback((e: React.KeyboardEvent) => {
        if (e.key === 'Enter' && webSearch?.toolInput.trim() && !webSearch.toolLoading && !webSearch.webResearchRunning) {
            handleSubmit();
        }
    }, [webSearch, handleSubmit]);

    return (
        <div className="sources-grid">
            <h2 className="sources-grid__title">{t('addSources')}</h2>

            {webSearchEnabled && webSearch && (
                <>
                    <div className="sources-grid__search-box">
                        <div className="sources-grid__search-header">
                            <span className="sources-grid__search-label">
                                <Search size={13} aria-hidden="true" />
                                {t('websearch')}
                            </span>
                            <select
                                value={mode}
                                onChange={e => handleModeChange(e.target.value as SearchMode)}
                                disabled={webSearch.toolLoading || webSearch.webResearchRunning}
                                className="sources-grid__search-mode"
                                aria-label={t('searchMode')}
                            >
                                <option value="fast">{t('websearchFast')}</option>
                                <option value="deep">{t('websearchDeep')}</option>
                            </select>
                        </div>
                        <div className="sources-grid__search-input-row">
                            <input
                                type="text"
                                placeholder={mode === 'fast' ? t('enterSearchTerm') : t('enterTopic')}
                                value={webSearch.toolInput}
                                onChange={e => webSearch.setToolInput(e.target.value)}
                                onKeyDown={handleKeyDown}
                                disabled={webSearch.toolLoading || webSearch.webResearchRunning}
                                className="sources-grid__search-input"
                            />
                            {webSearch.toolLoading ? (
                                <Loader2 size={14} className="animate-spin sources-grid__search-spinner" />
                            ) : (
                                <button
                                    onClick={handleSubmit}
                                    disabled={!webSearch.toolInput.trim() || webSearch.webResearchRunning}
                                    className="sources-grid__search-submit"
                                    aria-label={t('websearch')}
                                >
                                    <ArrowRight size={14} />
                                </button>
                            )}
                        </div>
                        {webSearch.hasResults && webSearch.onOpenWorkspace && (
                            <button
                                onClick={webSearch.onOpenWorkspace}
                                className="sources-grid__search-results-link"
                            >
                                {t('openWorkspace')}
                            </button>
                        )}
                    </div>
                    {(webSearch.webResearchRunning || webSearch.webResearchStatus) && (
                        <div className="sources-grid__research-progress">
                            <div className="sources-grid__research-track">
                                <div
                                    className="sources-grid__research-fill"
                                    style={{ width: `${Math.max(5, (webSearch.webResearchProgress.step / (webSearch.webResearchProgress.total || 1)) * 100)}%` }}
                                />
                            </div>
                            <div className="sources-grid__research-row">
                                <span className="sources-grid__research-text">
                                    {webSearch.webResearchStatus}
                                    {webSearch.webResearchProgress.step > 0 && ` (${webSearch.webResearchProgress.step}/${webSearch.webResearchProgress.total})`}
                                </span>
                                {webSearch.webResearchRunning && (
                                    <button onClick={webSearch.onCancelWebResearch} className="sources-grid__research-cancel" title={t('cancelResearch')}>
                                        <X size={12} />
                                    </button>
                                )}
                            </div>
                        </div>
                    )}
                </>
            )}

            <div className="sources-grid__grid">
                {items.map(({ type, icon: Icon, labelKey }) => (
                    <button
                        key={type}
                        className="sources-grid__item"
                        onClick={() => onSelect(type)}
                    >
                        <Icon size={18} aria-hidden="true" />
                        <span className="sources-grid__label">{t(labelKey)}</span>
                    </button>
                ))}
            </div>
        </div>
    );
};

export const SourcesGrid = memo(SourcesGridComp);
