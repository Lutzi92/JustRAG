import React, { memo, useState, useCallback } from 'react';
import {
    BookOpen, Link, Globe, Bot, FileText, Search, ChevronLeft,
    Rss
} from 'lucide-react';
import type { RssFeed } from '../../types';
import { API_BASE_URL } from '../../api';
import { useTheme } from '../../contexts/ThemeContext';
import { useAuth } from '../../contexts/AuthContext';
import { useIsMobileContext } from '../../contexts/MobileContext';
import { useKbCore } from '../../contexts/KbCoreContext';
import { useKbData } from '../../contexts/KbDataContext';
import { useKbLayout } from '../../contexts/KbLayoutContext';
import { SidebarShell } from '../sidebar-shell/SidebarShell';
import '../sidebar-primitives.css';
import { RssFeedEntriesModal } from '../RssFeedEntriesModal';
import { SourcesSection } from '../sidebar/SourcesSection';
import { SourcesGrid } from '../sidebar/SourcesGrid';
import type { SourceType } from '../sidebar/SourcesGrid';
import { CrawlModal } from '../sidebar/CrawlModal';
import { RssModal } from '../sidebar/RssModal';
import { FileUploadModal } from '../sidebar/FileUploadModal';
import { ConfluenceModal } from '../sidebar/ConfluenceModal';
import { GitRepoModal } from '../sidebar/GitRepoModal';
import { ACCEPTED_FILE_TYPES } from '../../constants';
import './SourcesPanel.css';

const SourcesPanelComp: React.FC = () => {
    const { t } = useTheme();
    const { siteConfigs } = useAuth();
    const isMobile = useIsMobileContext();
    const { currentKb, setKbView, handleGoHome, handleViewHome } = useKbCore();
    const {
        fileMgmt, webTools,
        rssFeeds, rssLoading, addRssFeed, updateRssFeed, deleteRssFeed, pollFeedNow,
        confluenceSources, updateConfluenceSource, deleteConfluenceSource, syncConfluenceNow,
        confluenceConnection, confluenceLoading,
        gitRepoSources, gitRepoLoading, addGitRepoSource, updateGitRepoSource, deleteGitRepoSource, syncGitRepoNow,
        saveConfluenceConnection, addConfluenceSource,
        fetchConfluenceSpaces, fetchConfluenceSpacePages, fetchConfluencePageChildren, fetchConfluenceAllSpacePages,
    } = useKbData();
    const { sidebar } = useKbLayout();

    const {
        files, fileInputRef,
        handleToggleFileSelection, handleToggleFilesSelection,
        handleDownloadFile, handleDeleteFile, handleFileUpload,
        retryFile, retryAllFailed,
        isDragging, textSourceTitle, setTextSourceTitle,
        textSourceContent, setTextSourceContent,
        handleDragOver, handleDragEnter, handleDragLeave, handleDrop, handleTextSourceAdd,
        showUploadModal, setShowUploadModal,
    } = fileMgmt;

    const {
        setToolTab, toolInput, setToolInput,
        crawlMaxPages, setCrawlMaxPages,
        toolLoading, handleToolSubmit, crawlResults, searchResults,
        handlePreviewSource,
        webResearchRunning, webResearchStatus, webResearchProgress, handleCancelWebResearch,
        setShowWebWorkspace, sourcesAddedCount,
    } = webTools;

    const isGlobal = currentKb?.isGlobal;
    const handleOpenWorkspace = useCallback(() => setShowWebWorkspace(true), [setShowWebWorkspace]);

    const [activeSourceModal, setActiveSourceModal] = useState<SourceType | null>(null);
    const [viewingFeed, setViewingFeed] = useState<RssFeed | null>(null);
    const nonRssFiles = files.filter(f => f.origin !== 'rss');
    const rssFeedFiles = (feedId: string) => files.filter(f => f.rssFeedId === feedId);

    const handleSourceSelect = useCallback((type: SourceType) => {
        if (type === 'academic') {
            setKbView('academic_research');
            return;
        }
        if (type === 'upload') {
            setShowUploadModal(true);
            return;
        }
        if (type === 'confluence') {
            setActiveSourceModal('confluence');
            return;
        }
        if (type === 'gitrepo') {
            setActiveSourceModal('gitrepo');
            return;
        }
        if (type === 'crawl') {
            setToolTab(type);
        }
        setActiveSourceModal(type);
    }, [setKbView, setShowUploadModal, setToolTab]);

    const closeSourceModal = useCallback(() => setActiveSourceModal(null), []);

    // Close modal when sources are successfully added.
    // Adjust-state-during-render pattern (react.dev "You Might Not Need an Effect"):
    // compare against the previous value instead of reacting in an effect.
    const [prevSourcesAddedCount, setPrevSourcesAddedCount] = useState(sourcesAddedCount);
    if (sourcesAddedCount !== prevSourcesAddedCount) {
        setPrevSourcesAddedCount(sourcesAddedCount);
        if (sourcesAddedCount > 0) setActiveSourceModal(null);
    }

    return (
        <SidebarShell
            side="right"
            isOpen={sidebar.isRightSidebarOpen}
            width={sidebar.rightSidebarWidth}
            onExpand={() => sidebar.setIsRightSidebarOpen(true)}
            onCollapse={() => sidebar.setIsRightSidebarOpen(false)}
            expandLabel={t('expandSourcesSidebar')}
            collapseLabel={t('collapseSourcesSidebar')}
            collapsedPreview={
                <>
                    <div className="sidebar-ui__collapsed-divider" />
                    <BookOpen size={20} color="var(--accent-primary)" />
                    {nonRssFiles.slice(0, 5).map(f => (
                        <div key={f.id} title={f.name} className="sidebar-left__collapsed-file-icon">
                            {f.origin === 'websearch' ? <Link size={16} aria-hidden="true" />
                                : f.origin === 'crawl' ? <Globe size={16} aria-hidden="true" />
                                : f.origin === 'research' ? <Bot size={16} aria-hidden="true" />
                                : f.origin === 'rss' ? <Rss size={16} aria-hidden="true" />
                                : <FileText size={16} aria-hidden="true" />}
                        </div>
                    ))}
                    <div className="sidebar-left__spacer" />
                    {!isGlobal && <Search size={20} color="var(--text-secondary)" />}
                </>
            }
        >
            <div
                className="sidebar-left__sources"
                style={{
                    height: isMobile ? undefined : '100%',
                    flex: isMobile ? '1 1 auto' : undefined,
                    overflow: isMobile ? 'auto' : undefined,
                }}
            >
                <div className="sidebar-left__header-wrap">
                    <div className="sidebar-left__header-row">
                        <div className="sidebar-left__brand-row">
                            <button
                                onClick={handleGoHome}
                                className="sidebar-left__brand-button"
                                aria-label={t('goToHome')}
                            >
                                {siteConfigs.logo_path ? (
                                    <img src={`${API_BASE_URL}${siteConfigs.logo_path}`} alt="" className="sidebar-left__brand-logo" />
                                ) : (
                                    <div className="sidebar-left__brand-fallback">
                                        <BookOpen size={24} aria-hidden="true" />
                                    </div>
                                )}
                            </button>
                            <button
                                onClick={handleViewHome}
                                className="secondary-button sidebar-left__overview-btn sidebar-left__overview-btn--icon-only"
                                title={t('backToOverview')}
                                aria-label={t('backToOverview')}
                            >
                                <ChevronLeft size={20} aria-hidden="true" />
                            </button>
                        </div>
                    </div>
                </div>

                {!isGlobal && (
                    <SourcesGrid
                        onSelect={handleSourceSelect}
                        webSearch={{
                            toolInput,
                            setToolInput,
                            toolLoading,
                            onSubmit: handleToolSubmit,
                            setToolTab,
                            webResearchRunning,
                            webResearchStatus,
                            webResearchProgress,
                            onCancelWebResearch: handleCancelWebResearch,
                            hasResults: searchResults.length > 0 || crawlResults.length > 0,
                            onOpenWorkspace: handleOpenWorkspace,
                        }}
                    />
                )}

                <SourcesSection
                    files={files}
                    onPreviewSource={handlePreviewSource}
                    onToggleFileSelection={handleToggleFileSelection}
                    onToggleFilesSelection={handleToggleFilesSelection}
                    onDownloadFile={handleDownloadFile}
                    onDeleteFile={handleDeleteFile}
                    rssFeeds={rssFeeds}
                    onUpdateRssFeed={updateRssFeed}
                    onDeleteRssFeed={deleteRssFeed}
                    onPollFeedNow={pollFeedNow}
                    onViewFeed={setViewingFeed}
                    confluenceSources={confluenceSources}
                    onUpdateConfluenceSource={updateConfluenceSource}
                    onDeleteConfluenceSource={deleteConfluenceSource}
                    onSyncConfluenceNow={syncConfluenceNow}
                    onRetryFile={retryFile}
                    onRetryAllFailed={retryAllFailed}
                    gitRepoSources={gitRepoSources}
                    onUpdateGitRepoSource={updateGitRepoSource}
                    onDeleteGitRepoSource={deleteGitRepoSource}
                    onSyncGitRepoNow={syncGitRepoNow}
                />
            </div>

            <input
                type="file"
                ref={fileInputRef}
                className="sidebar-left__hidden-file-input"
                onChange={handleFileUpload}
                accept={ACCEPTED_FILE_TYPES}
                multiple
            />

            {viewingFeed && (
                <RssFeedEntriesModal
                    feed={viewingFeed}
                    files={rssFeedFiles(viewingFeed.id)}
                    onDeleteFile={handleDeleteFile}
                    onClose={() => setViewingFeed(null)}
                />
            )}

            <CrawlModal
                show={activeSourceModal === 'crawl'}
                onClose={closeSourceModal}
                toolLoading={toolLoading}
                onToolSubmit={(url: string) => handleToolSubmit('crawl', url)}
                crawlMaxPages={crawlMaxPages}
                setCrawlMaxPages={setCrawlMaxPages}
                crawlResults={crawlResults}
                onOpenWorkspace={handleOpenWorkspace}
            />
            <RssModal
                show={activeSourceModal === 'rss'}
                onClose={closeSourceModal}
                rssLoading={rssLoading}
                onAddRssFeed={addRssFeed}
            />
            <ConfluenceModal
                show={activeSourceModal === 'confluence'}
                onClose={closeSourceModal}
                confluenceConnection={confluenceConnection}
                confluenceLoading={confluenceLoading}
                onSaveConnection={saveConfluenceConnection}
                onAddSource={(data) => {
                    if (confluenceConnection?.connection?.id) {
                        addConfluenceSource({ ...data, connectionId: confluenceConnection.connection.id });
                    }
                }}
                fetchSpaces={fetchConfluenceSpaces}
                fetchSpacePages={fetchConfluenceSpacePages}
                fetchPageChildren={fetchConfluencePageChildren}
                fetchAllSpacePages={fetchConfluenceAllSpacePages}
            />
            <GitRepoModal
                show={activeSourceModal === 'gitrepo'}
                onClose={closeSourceModal}
                loading={gitRepoLoading}
                onAdd={addGitRepoSource}
            />
            <FileUploadModal
                show={showUploadModal}
                onClose={() => setShowUploadModal(false)}
                fileInputRef={fileInputRef}
                onTextSourceAdd={handleTextSourceAdd}
                textSourceTitle={textSourceTitle}
                setTextSourceTitle={setTextSourceTitle}
                textSourceContent={textSourceContent}
                setTextSourceContent={setTextSourceContent}
                onDragOver={handleDragOver}
                onDragEnter={handleDragEnter}
                onDragLeave={handleDragLeave}
                onDrop={handleDrop}
                isDragging={isDragging}
            />
        </SidebarShell>
    );
};

export const SourcesPanel = memo(SourcesPanelComp);
