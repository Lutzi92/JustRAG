import React, { memo, useState, useEffect } from 'react';
import { Loader2, Search, Layout } from 'lucide-react';
import { useTheme } from '../../contexts/ThemeContext';
import { SourceModal } from './SourceModal';

interface CrawlModalProps {
    show: boolean;
    onClose: () => void;
    toolLoading: boolean;
    onToolSubmit: (urlOverride: string) => void;
    crawlMaxPages: number;
    setCrawlMaxPages: (n: number) => void;
    crawlResults: { title: string; content: string; url: string; isEdited?: boolean }[];
    onOpenWorkspace: () => void;
}

const CrawlModalComp: React.FC<CrawlModalProps> = ({
    show, onClose, toolLoading, onToolSubmit,
    crawlMaxPages, setCrawlMaxPages, crawlResults, onOpenWorkspace,
}) => {
    const { t } = useTheme();
    // Own local state — isolated from the web search input
    const [crawlUrl, setCrawlUrl] = useState('');

    // Reset when modal opens
    useEffect(() => {
        if (show) setCrawlUrl('');
    }, [show]);

    return (
        <SourceModal title={t('crawl')} show={show} onClose={onClose}>
            <p style={{ margin: 0, fontSize: '0.85rem', color: 'var(--text-secondary)' }}>{t('crawlDesc')}</p>
            <input
                type="text"
                placeholder="https://example.com"
                value={crawlUrl}
                onChange={(e) => setCrawlUrl(e.target.value)}
                onKeyDown={(e) => { if (e.key === 'Enter' && crawlUrl.trim() && !toolLoading) onToolSubmit(crawlUrl); }}
                aria-label={t('enterUrl')}
                className="sidebar-left__tools-input"
                autoFocus
            />
            <div className="sidebar-left__slider-row">
                <label htmlFor="crawl-modal-max-pages">{t('maxPages')}: {crawlMaxPages}</label>
                <input
                    id="crawl-modal-max-pages"
                    type="range" min="1" max="20" step="1"
                    value={crawlMaxPages}
                    onChange={(e) => setCrawlMaxPages(parseInt(e.target.value, 10))}
                    className="sidebar-left__slider"
                />
            </div>
            <button
                onClick={() => onToolSubmit(crawlUrl)}
                disabled={toolLoading || !crawlUrl.trim()}
                className="search-button"
            >
                <span className="icon-swap" key={toolLoading ? 'loading' : 'icon'}>
                    {toolLoading ? <Loader2 className="animate-spin" size={16} /> : <Search size={16} aria-hidden="true" />}
                </span>
                {t('startCrawl')}
            </button>
            {crawlResults.length > 0 && (
                <div className="sidebar-left__workspace-card">
                    <div className="sidebar-left__workspace-header">
                        <span className="sidebar-left__workspace-count">{crawlResults.length} {t('foundPages')}</span>
                    </div>
                    <button onClick={onOpenWorkspace} className="sidebar-left__workspace-btn">
                        <Layout size={14} /> {t('openWorkspace')}
                    </button>
                </div>
            )}
        </SourceModal>
    );
};

export const CrawlModal = memo(CrawlModalComp);
