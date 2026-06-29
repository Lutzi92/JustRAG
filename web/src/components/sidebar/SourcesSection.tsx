import React, { memo } from 'react';
import {
    Link, Globe, Bot, FileText, Download, Trash2,
    Rss, RefreshCw, Pause, Play, Eye, BookOpen, Plus, GitBranch
} from 'lucide-react';
import type { FileEntry, RssFeed, ConfluenceSource, GitRepoSource } from '../../types';
import { useTheme } from '../../contexts/ThemeContext';
import { IngestStageIndicator } from './IngestStageIndicator';

interface SourcesSectionProps {
    files: FileEntry[];
    onPreviewSource: (file: FileEntry) => void;
    onToggleFileSelection: (id: string, e: React.SyntheticEvent) => void;
    onToggleFilesSelection: (fileIds: string[], selected: boolean) => void;
    onDownloadFile: (id: string) => void;
    onDeleteFile: (id: string, e: React.MouseEvent) => void;
    rssFeeds: RssFeed[];
    onUpdateRssFeed: (feedId: string, updates: { pollInterval?: number; status?: 'active' | 'paused' }) => void;
    onDeleteRssFeed: (feedId: string) => void;
    onPollFeedNow: (feedId: string) => void;
    onViewFeed: (feed: RssFeed) => void;
    confluenceSources: ConfluenceSource[];
    onUpdateConfluenceSource: (sourceId: string, updates: { includeAttachments?: boolean; syncInterval?: number | null; status?: 'active' | 'paused' }) => void;
    onDeleteConfluenceSource: (sourceId: string) => void;
    onSyncConfluenceNow: (sourceId: string) => void;
    gitRepoSources: GitRepoSource[];
    onUpdateGitRepoSource: (sourceId: string, updates: { status?: 'active' | 'paused' }) => void;
    onDeleteGitRepoSource: (sourceId: string) => void;
    onSyncGitRepoNow: (sourceId: string) => void;
    onRetryFile: (id: string) => void;
    onRetryAllFailed: () => void;
    onAddSource?: () => void;
}

// Maps files.error_stage values (backend vocabulary, see
// files.PGStore.MarkFileError) to translation keys. Unknown stages fall
// back to the raw errorMessage, then to fileErrorUnknown.
const ERROR_STAGE_KEYS: Record<string, string> = {
    unsupported_type: 'fileErrorUnsupportedType',
    parse: 'fileErrorParse',
    embedding: 'fileErrorEmbedding',
    canceled: 'fileErrorCanceled',
    processing: 'fileErrorProcessing',
    timeout: 'fileErrorTimeout',
    queue: 'fileErrorQueue',
};

const SourcesSectionComp: React.FC<SourcesSectionProps> = ({
    files, onPreviewSource, onToggleFileSelection, onToggleFilesSelection,
    onDownloadFile, onDeleteFile,
    rssFeeds, onUpdateRssFeed, onDeleteRssFeed, onPollFeedNow, onViewFeed,
    confluenceSources, onUpdateConfluenceSource, onDeleteConfluenceSource, onSyncConfluenceNow,
    gitRepoSources, onUpdateGitRepoSource, onDeleteGitRepoSource, onSyncGitRepoNow,
    onRetryFile, onRetryAllFailed, onAddSource
}) => {
    const { t } = useTheme();

    const nonRssFiles = files.filter(f => f.origin !== 'rss' && f.origin !== 'confluence' && f.origin !== 'git');
    const rssFeedFiles = (feedId: string) => files.filter(f => f.rssFeedId === feedId);

    const failedCount = files.filter(f => f.status === 'error').length;
    const errorLabel = (file: FileEntry) => {
        if (file.errorStage && ERROR_STAGE_KEYS[file.errorStage]) return t(ERROR_STAGE_KEYS[file.errorStage]);
        return file.errorMessage || t('fileErrorUnknown');
    };

    return (
        <div className="sidebar-left__files-section">
            {onAddSource && (
                <button
                    type="button"
                    onClick={onAddSource}
                    className="sidebar-left__add-source-btn"
                    style={{
                        display: 'flex', alignItems: 'center', justifyContent: 'center', gap: '0.4rem',
                        width: '100%', padding: '0.6rem 0.9rem', marginBottom: '0.75rem',
                        background: 'var(--accent-primary)', color: '#fff', border: 'none',
                        borderRadius: '8px', cursor: 'pointer', fontFamily: 'inherit',
                        fontSize: '0.875rem', fontWeight: 600, transition: 'background 0.15s, transform 0.15s',
                    }}
                    onMouseEnter={(e) => { e.currentTarget.style.transform = 'translateY(-2px)'; }}
                    onMouseLeave={(e) => { e.currentTarget.style.transform = 'translateY(0)'; }}
                >
                    <Plus size={16} aria-hidden="true" /> {t('addSourceCta')}
                </button>
            )}
            <div className="sidebar-left__files-header sidebar-ui__section-header">
                <h2 className="sidebar-left__files-title">{t('sources')}</h2>
                {failedCount > 0 && (
                    <button
                        onClick={onRetryAllFailed}
                        className="text-button sidebar-left__retry-all-failed"
                        title={t('retryAllFailed')}
                    >
                        <RefreshCw size={12} aria-hidden="true" /> {t('retryAllFailed')} ({failedCount})
                    </button>
                )}
            </div>

            <ul className="sidebar-left__files-list sidebar-ui__list">
                {nonRssFiles.map(file => (
                    <li key={file.id} className="source-card sidebar-left__file-card">
                        <div className="sidebar-left__file-row">
                            <input
                                type="checkbox"
                                checked={file.selected !== false}
                                onChange={(e) => onToggleFileSelection(file.id, e)}
                                className="sidebar-left__file-checkbox"
                                aria-label={`${t('selectSource')} ${file.name}`}
                            />
                            <div className="sidebar-left__file-origin-icon">
                                {file.origin === 'websearch' ? <Link size={18} aria-hidden="true" /> : file.origin === 'crawl' ? <Globe size={18} aria-hidden="true" /> : file.origin === 'research' ? <Bot size={18} aria-hidden="true" /> : file.origin === 'rss' ? <Rss size={18} aria-hidden="true" /> : <FileText size={18} aria-hidden="true" />}
                            </div>
                            <div className="sidebar-left__file-main sidebar-ui__item-main">
                                <div className="sidebar-left__file-top-row sidebar-ui__item-row">
                                    <button
                                        onClick={() => onPreviewSource(file)}
                                        className="text-button source-title sidebar-left__file-name sidebar-ui__item-title"
                                    >
                                        {file.name}
                                    </button>
                                    <div className="sidebar-left__file-actions">
                                        {file.status === 'error' && (
                                            <button
                                                onClick={(e) => { e.stopPropagation(); onRetryFile(file.id); }}
                                                className="settings-toggle sidebar-left__file-action sidebar-ui__item-delete"
                                                title={t('retrySource')}
                                                aria-label={`${t('retrySource')} ${file.name}`}
                                            >
                                                <RefreshCw size={14} />
                                            </button>
                                        )}
                                        <button
                                            onClick={(e) => { e.stopPropagation(); onDownloadFile(file.id); }}
                                            className="settings-toggle sidebar-left__file-action sidebar-ui__item-delete"
                                            title={t('download')}
                                            aria-label={`${t('download')} ${file.name}`}
                                        >
                                            <Download size={14} />
                                        </button>
                                        <button
                                            onClick={(e) => onDeleteFile(file.id, e)}
                                            className="settings-toggle sidebar-left__file-action sidebar-ui__item-delete"
                                            aria-label={`${t('delete')} ${file.name}`}
                                        >
                                            <Trash2 size={14} />
                                        </button>
                                    </div>
                                </div>
                                <div className="sidebar-left__file-meta-row">
                                    <div
                                        className="source-meta sidebar-left__file-meta sidebar-ui__item-meta"
                                        style={{ color: file.status === 'error' ? 'var(--error-text)' : 'var(--text-secondary)' }}
                                    >
                                        {file.status} • {file.origin}
                                    </div>
                                </div>
                                {file.status === 'error' && (
                                    <div
                                        className="sidebar-left__rss-feed-error"
                                        title={file.errorMessage || undefined}
                                    >
                                        {errorLabel(file)}
                                    </div>
                                )}
                                {file.currentStage && (
                                    <IngestStageIndicator
                                        stage={file.currentStage}
                                        index={file.stageIndex}
                                        total={file.stageTotal}
                                        fileName={file.name}
                                    />
                                )}
                            </div>
                        </div>
                    </li>
                ))}

                {rssFeeds.map(feed => (
                    <li key={`rss-${feed.id}`} className="source-card sidebar-left__file-card">
                        <div className="sidebar-left__file-row">
                            <input
                                type="checkbox"
                                checked={rssFeedFiles(feed.id).length > 0 && rssFeedFiles(feed.id).every(f => f.selected !== false)}
                                onChange={() => {
                                    const feedFileList = rssFeedFiles(feed.id);
                                    const allSelected = feedFileList.every(f => f.selected !== false);
                                    onToggleFilesSelection(feedFileList.map(f => f.id), !allSelected);
                                }}
                                className="sidebar-left__file-checkbox"
                                aria-label={`${t('selectSource')} ${feed.title || feed.url}`}
                            />
                            <div className="sidebar-left__file-origin-icon">
                                <Rss size={18} aria-hidden="true" />
                            </div>
                            <div className="sidebar-left__file-main sidebar-ui__item-main">
                                <div className="sidebar-left__file-top-row sidebar-ui__item-row">
                                    <span className="text-button source-title sidebar-left__file-name sidebar-ui__item-title">
                                        {feed.title || feed.url}
                                    </span>
                                    <span className={`sidebar-left__rss-feed-status sidebar-left__rss-feed-status--${feed.status}`}>
                                        {feed.status === 'active' ? t('active') : feed.status === 'paused' ? t('paused') : t('feedError')}
                                    </span>
                                </div>
                                <div className="sidebar-left__file-meta-row">
                                    <div className="source-meta sidebar-left__file-meta sidebar-ui__item-meta">
                                        {feed.itemCount > 0 && <span>{feed.itemCount} {t('items')}</span>}
                                        {feed.lastPolledAt && <span>{t('lastPolled')}: {new Date(feed.lastPolledAt).toLocaleString()}</span>}
                                    </div>
                                </div>
                                {feed.status === 'error' && feed.errorMessage && (
                                    <div className="sidebar-left__rss-feed-error">{feed.errorMessage}</div>
                                )}
                                <div className="sidebar-left__rss-feed-actions">
                                    <button onClick={() => onPollFeedNow(feed.id)} title={t('pollNow')} aria-label={t('pollNow')}>
                                        <RefreshCw size={14} />
                                    </button>
                                    <button onClick={() => onUpdateRssFeed(feed.id, {
                                        status: feed.status === 'active' ? 'paused' : 'active'
                                    })} title={feed.status === 'active' ? t('pause') : t('resume')} aria-label={feed.status === 'active' ? t('pause') : t('resume')}>
                                        {feed.status === 'active' ? <Pause size={14} /> : <Play size={14} />}
                                    </button>
                                    <button onClick={() => onViewFeed(feed)} title={t('rssViewEntries')} aria-label={t('rssViewEntries')}>
                                        <Eye size={14} />
                                    </button>
                                    <button onClick={() => onDeleteRssFeed(feed.id)} title={t('delete')} aria-label={`${t('delete')} ${feed.title || feed.url}`}>
                                        <Trash2 size={14} />
                                    </button>
                                </div>
                            </div>
                        </div>
                    </li>
                ))}

                {confluenceSources.map(source => (
                    <li key={`confluence-${source.id}`} className="source-card sidebar-left__file-card">
                        <div className="sidebar-left__file-row">
                            <div className="sidebar-left__file-origin-icon">
                                <BookOpen size={18} aria-hidden="true" />
                            </div>
                            <div className="sidebar-left__file-main sidebar-ui__item-main">
                                <div className="sidebar-left__file-top-row sidebar-ui__item-row">
                                    <span className="text-button source-title sidebar-left__file-name sidebar-ui__item-title">
                                        {source.spaceKey}{source.rootPageTitle ? ` / ${source.rootPageTitle}` : ''}
                                    </span>
                                    <span className={`sidebar-left__rss-feed-status sidebar-left__rss-feed-status--${source.status}`}>
                                        {source.status === 'active' ? t('active') : source.status === 'syncing' ? t('syncing') : source.status === 'paused' ? t('paused') : t('feedError')}
                                    </span>
                                </div>
                                <div className="sidebar-left__file-meta-row">
                                    <div className="source-meta sidebar-left__file-meta sidebar-ui__item-meta">
                                        {source.pageCount > 0 && <span>{source.pageCount} {t('pages')}</span>}
                                        {source.lastSyncedAt && <span>{t('lastSynced')}: {new Date(source.lastSyncedAt).toLocaleString()}</span>}
                                    </div>
                                </div>
                                {source.status === 'syncing' && source.syncTotal > 0 && (
                                    <div style={{ marginTop: 4 }}>
                                        <div style={{
                                            height: 6, borderRadius: 3, background: 'var(--bg-tertiary)', overflow: 'hidden',
                                        }}>
                                            <div style={{
                                                height: '100%', borderRadius: 3, background: 'var(--accent-primary)',
                                                width: `${Math.round((source.syncProgress / source.syncTotal) * 100)}%`,
                                                transition: 'width 0.3s ease',
                                            }} />
                                        </div>
                                        <span style={{ fontSize: '0.75rem', color: 'var(--text-secondary)' }}>
                                            {source.syncProgress}/{source.syncTotal} {t('filesProcessed')}
                                        </span>
                                    </div>
                                )}
                                {source.status === 'error' && source.errorMessage && (
                                    <div className="sidebar-left__rss-feed-error">{source.errorMessage}</div>
                                )}
                                <div className="sidebar-left__rss-feed-actions">
                                    <button onClick={() => onSyncConfluenceNow(source.id)} title={t('pollNow')} aria-label={t('pollNow')}>
                                        <RefreshCw size={14} />
                                    </button>
                                    <button onClick={() => onUpdateConfluenceSource(source.id, {
                                        status: source.status === 'active' ? 'paused' : 'active'
                                    })} title={source.status === 'active' ? t('pause') : t('resume')} aria-label={source.status === 'active' ? t('pause') : t('resume')}>
                                        {source.status === 'active' ? <Pause size={14} /> : <Play size={14} />}
                                    </button>
                                    <button onClick={() => onDeleteConfluenceSource(source.id)} title={t('delete')} aria-label={`${t('delete')} ${source.spaceKey}`}>
                                        <Trash2 size={14} />
                                    </button>
                                </div>
                            </div>
                        </div>
                    </li>
                ))}

                {gitRepoSources.map(source => {
                    const repoName = source.repoUrl.replace(/\.git$/, '').split('/').slice(-2).join('/');
                    return (
                        <li key={`gitrepo-${source.id}`} className="source-card sidebar-left__file-card">
                            <div className="sidebar-left__file-row">
                                <div className="sidebar-left__file-origin-icon">
                                    <GitBranch size={18} aria-hidden="true" />
                                </div>
                                <div className="sidebar-left__file-main sidebar-ui__item-main">
                                    <div className="sidebar-left__file-top-row sidebar-ui__item-row">
                                        <span className="text-button source-title sidebar-left__file-name sidebar-ui__item-title">
                                            {repoName}{source.branch ? ` @ ${source.branch}` : ''}
                                        </span>
                                        <span className={`sidebar-left__rss-feed-status sidebar-left__rss-feed-status--${source.status}`}>
                                            {source.status === 'active' ? t('active') : source.status === 'syncing' ? t('syncing') : source.status === 'paused' ? t('paused') : t('feedError')}
                                        </span>
                                    </div>
                                    <div className="sidebar-left__file-meta-row">
                                        <div className="source-meta sidebar-left__file-meta sidebar-ui__item-meta">
                                            {source.fileCount > 0 && <span>{source.fileCount} {t('gitFiles')}</span>}
                                            {source.lastSyncedAt && <span>{t('lastSynced')}: {new Date(source.lastSyncedAt).toLocaleString()}</span>}
                                        </div>
                                    </div>
                                    {source.status === 'syncing' && source.syncTotal > 0 && (
                                        <div style={{ marginTop: 4 }}>
                                            <div style={{ height: 6, borderRadius: 3, background: 'var(--bg-tertiary)', overflow: 'hidden' }}>
                                                <div style={{ height: '100%', borderRadius: 3, background: 'var(--accent-primary)', width: `${Math.round((source.syncProgress / source.syncTotal) * 100)}%`, transition: 'width 0.3s ease' }} />
                                            </div>
                                            <span style={{ fontSize: '0.75rem', color: 'var(--text-secondary)' }}>
                                                {source.syncProgress}/{source.syncTotal} {t('filesProcessed')}
                                            </span>
                                        </div>
                                    )}
                                    {source.status === 'error' && source.errorMessage && (
                                        <div className="sidebar-left__rss-feed-error">{source.errorMessage}</div>
                                    )}
                                    <div className="sidebar-left__rss-feed-actions">
                                        <button onClick={() => onSyncGitRepoNow(source.id)} title={t('pollNow')} aria-label={t('pollNow')}>
                                            <RefreshCw size={14} />
                                        </button>
                                        <button onClick={() => onUpdateGitRepoSource(source.id, { status: source.status === 'active' ? 'paused' : 'active' })} disabled={source.status === 'syncing'} title={source.status === 'active' ? t('pause') : t('resume')} aria-label={source.status === 'active' ? t('pause') : t('resume')}>
                                            {source.status === 'active' ? <Pause size={14} /> : <Play size={14} />}
                                        </button>
                                        <button onClick={() => onDeleteGitRepoSource(source.id)} title={t('delete')} aria-label={`${t('delete')} ${repoName}`}>
                                            <Trash2 size={14} />
                                        </button>
                                    </div>
                                </div>
                            </div>
                        </li>
                    );
                })}
            </ul>

            {nonRssFiles.length === 0 && rssFeeds.length === 0 && confluenceSources.length === 0 && gitRepoSources.length === 0 && (
                <div className="sidebar-left__empty sidebar-ui__empty">{t('noSources')}</div>
            )}
        </div>
    );
};

export const SourcesSection = memo(SourcesSectionComp);
