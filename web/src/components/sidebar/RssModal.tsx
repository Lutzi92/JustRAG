import React, { memo, useState } from 'react';
import { Plus, Loader2 } from 'lucide-react';
import { useTheme } from '../../contexts/ThemeContext';
import { SourceModal } from './SourceModal';
import { SyncScheduleSelect } from './SyncScheduleSelect';
import type { SyncSchedule } from '../../types';

interface RssModalProps {
    show: boolean;
    onClose: () => void;
    rssLoading: boolean;
    onAddRssFeed: (url: string, syncSchedule: SyncSchedule, fetchFullText: boolean) => void;
}

const RssModalComp: React.FC<RssModalProps> = ({ show, onClose, rssLoading, onAddRssFeed }) => {
    const { t } = useTheme();
    const [rssUrl, setRssUrl] = useState('');
    // Defaults to 'daily' (not 'manual'): before this branch every RSS feed
    // polled automatically (60 min default), and migration 0068 backfills
    // every existing active feed to 'daily'. A newly created feed must keep
    // that "polls automatically" behavior, or it is fetched once and never
    // again. The backend's own default stays 'manual' (an omitted field must
    // stay conservative) — this is a UI-only default.
    const [syncSchedule, setSyncSchedule] = useState<SyncSchedule>('daily');
    const [rssFetchFullText, setRssFetchFullText] = useState(false);

    const handleSubmit = () => {
        if (rssUrl.trim()) {
            onAddRssFeed(rssUrl.trim(), syncSchedule, rssFetchFullText);
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
            <SyncScheduleSelect id="rss-modal-schedule" value={syncSchedule} onChange={setSyncSchedule} />
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
