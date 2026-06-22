import React, { memo, useState } from 'react';
import { Plus, Loader2 } from 'lucide-react';
import { useTheme } from '../../contexts/ThemeContext';
import { SourceModal } from './SourceModal';

interface RssModalProps {
    show: boolean;
    onClose: () => void;
    rssLoading: boolean;
    onAddRssFeed: (url: string, pollInterval: number, fetchFullText: boolean) => void;
}

const RssModalComp: React.FC<RssModalProps> = ({ show, onClose, rssLoading, onAddRssFeed }) => {
    const { t } = useTheme();
    const [rssUrl, setRssUrl] = useState('');
    const [rssPollInterval, setRssPollInterval] = useState(60);
    const [rssFetchFullText, setRssFetchFullText] = useState(false);

    const handleSubmit = () => {
        if (rssUrl.trim()) {
            onAddRssFeed(rssUrl.trim(), rssPollInterval, rssFetchFullText);
            setRssUrl('');
            setRssFetchFullText(false);
            onClose();
        }
    };

    return (
        <SourceModal title={t('rss')} show={show} onClose={onClose}>
            <p style={{ margin: 0, fontSize: '0.85rem', color: 'var(--text-secondary)' }}>{t('rssDesc')}</p>
            <input
                type="url"
                placeholder={t('enterFeedUrl')}
                value={rssUrl}
                onChange={(e) => setRssUrl(e.target.value)}
                onKeyDown={(e) => { if (e.key === 'Enter') handleSubmit(); }}
                className="sidebar-left__tools-input"
                // eslint-disable-next-line jsx-a11y/no-autofocus -- focus first field on dialog open (WAI-ARIA dialog pattern)
                autoFocus
            />
            <div className="sidebar-left__slider-row">
                <label htmlFor="rss-modal-interval">{t('pollInterval')}</label>
                <select
                    id="rss-modal-interval"
                    value={rssPollInterval}
                    onChange={(e) => setRssPollInterval(parseInt(e.target.value, 10))}
                    className="sidebar-left__tools-select"
                >
                    <option value="15">15 {t('minutes')}</option>
                    <option value="30">30 {t('minutes')}</option>
                    <option value="60">1 {t('hours')}</option>
                    <option value="360">6 {t('hours')}</option>
                    <option value="720">12 {t('hours')}</option>
                    <option value="1440">24 {t('hours')}</option>
                </select>
            </div>
            <label style={{ display: 'flex', alignItems: 'flex-start', gap: '0.5rem', fontSize: '0.85rem' }}>
                <input
                    type="checkbox"
                    checked={rssFetchFullText}
                    onChange={(e) => setRssFetchFullText(e.target.checked)}
                />
                <span>
                    {t('rssFetchFullText')}
                    <span style={{ display: 'block', fontSize: '0.75rem', color: 'var(--text-secondary)' }}>{t('rssFetchFullTextHint')}</span>
                </span>
            </label>
            <button
                onClick={handleSubmit}
                disabled={rssLoading || !rssUrl.trim()}
                className="search-button"
            >
                {rssLoading ? <Loader2 className="animate-spin" size={16} /> : <Plus size={16} />}
                {t('subscribe')}
            </button>
        </SourceModal>
    );
};

export const RssModal = memo(RssModalComp);
