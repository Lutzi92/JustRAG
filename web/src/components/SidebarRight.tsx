import React, { memo, useMemo } from 'react';
import {
    PanelRightClose, Sparkles, FileText, MessageSquare, Loader2, Trash2, Plus, Search, GraduationCap, ArrowLeft,
} from 'lucide-react';
import type { ChatEntry, GeneratedContent } from '../types';
import { artifactTypeLabel } from '../utils/artifactTypes';
import { useTheme } from '../contexts/ThemeContext';
import { useIsMobileContext } from '../contexts/MobileContext';
import { useKbCore } from '../contexts/KbCoreContext';
import { useKbChat } from '../contexts/KbChatContext';
import { useKbData } from '../contexts/KbDataContext';
import { useKbLayout } from '../contexts/KbLayoutContext';
import { viewportHeight } from '../utils/viewport';
import { ContentRowSkeleton } from './Skeleton';
import './sidebar-primitives.css';
import './SidebarRight.css';

const SidebarRightComp: React.FC = () => {
    const { t } = useTheme();
    const isMobile = useIsMobileContext();
    const { currentKb, handleGoHome } = useKbCore();
    const { chat } = useKbChat();
    const { fileMgmt, content, handleSelectContent, handleExpandStudio } = useKbData();
    const { sidebar } = useKbLayout();

    const { generatedContent, generating, handleGenerate, handleDeleteGeneratedContent, podcastProgress } = content;
    const { chats, activeChatId, handleSelectChat, handleDeleteChat, handleNewChat } = chat;
    const { hasFiles } = fileMgmt;

    const isOpen = isMobile ? true : sidebar.isRightSidebarOpen;
    const width = isMobile ? 0 : sidebar.rightSidebarWidth;
    const split = sidebar.rightSidebarSplit;
    const isGlobal = currentKb?.isGlobal;
    const studioConfig = currentKb?.isGlobal ? (currentKb.studioConfig || undefined) : undefined;

    const sc = { cards: true, presentation: true, podcast: true, abstract: true, briefingDoc: true, faq: true, studyGuide: true, timeline: true, quiz: true, ...studioConfig };
    const hasAnyStudio = sc.cards || sc.presentation || sc.podcast || sc.abstract || sc.briefingDoc || sc.faq || sc.studyGuide || sc.timeline || sc.quiz;

    const researchChats = useMemo(() => chats.filter(c => c.type === 'research'), [chats]);
    const academicChats = useMemo(() => chats.filter(c => c.type === 'academic_research'), [chats]);
    const standardChats = useMemo(() => chats.filter(c => c.type !== 'research' && c.type !== 'academic_research'), [chats]);

    const studioItems = useMemo(() => {
        const combined = [
            ...generatedContent.map(i => ({ ...i, itemType: 'generated' as const })),
            ...researchChats.map(c => ({ ...c, itemType: 'research' as const })),
            ...academicChats.map(c => ({ ...c, itemType: 'academic_research' as const })),
        ];
        return combined.sort((a, b) => new Date(b.createdAt).getTime() - new Date(a.createdAt).getTime());
    }, [generatedContent, researchChats, academicChats]);

    const handleKeyDown = (e: React.KeyboardEvent) => {
        if (e.key === 'ArrowUp') {
            e.preventDefault();
            sidebar.setRightSidebarSplit(Math.max(20, split - 5));
        } else if (e.key === 'ArrowDown') {
            e.preventDefault();
            sidebar.setRightSidebarSplit(Math.min(80, split + 5));
        }
    };

    return (
        <aside
            className={`sidebar sidebar-right sidebar-ui__panel${isMobile ? ' sidebar-right--mobile' : ''}`}
            style={{
                width: isMobile ? '100%' : (isOpen ? `${width}px` : '60px'),
                height: isMobile ? viewportHeight('calc(100dvh - 60px)', 'calc(100vh - 60px)') : undefined,
                borderLeft: isMobile ? 'none' : undefined,
            }}
        >
            {isMobile && (
                <div style={{ display: 'flex', alignItems: 'center', gap: '8px', padding: '0.75rem', borderBottom: '1px solid var(--border-color)' }}>
                    <button
                        onClick={handleGoHome}
                        style={{ background: 'none', border: 'none', cursor: 'pointer', display: 'flex', alignItems: 'center', padding: '4px', color: 'var(--text-secondary)' }}
                        aria-label={t('back')}
                    >
                        <ArrowLeft size={20} />
                    </button>
                    <span style={{ fontWeight: 600, fontSize: '0.85rem' }}>{currentKb?.name}</span>
                </div>
            )}
            {!isMobile && !isOpen && (
                <button
                    onClick={() => sidebar.setIsRightSidebarOpen(true)}
                    className="settings-toggle sidebar-right__collapsed sidebar-ui__collapsed"
                    aria-label={t('expandRightSidebar')}
                    aria-expanded={false}
                >
                    <div className="sidebar-right__collapsed-icon-wrap sidebar-ui__collapsed-icon-wrap">
                        <PanelRightClose size={20} />
                    </div>
                    <div className="sidebar-right__collapsed-divider sidebar-ui__collapsed-divider" />
                    {!isGlobal && (
                        <>
                            <Sparkles size={20} color="var(--accent-primary)" />
                            {generatedContent.slice(0, 3).map(i => (
                                <div key={i.id} className="sidebar-right__collapsed-item-icon">
                                    <FileText size={16} aria-hidden="true" />
                                </div>
                            ))}
                            <div className="sidebar-right__collapsed-divider sidebar-ui__collapsed-divider" />
                        </>
                    )}
                    <MessageSquare size={20} color="var(--text-secondary)" />
                </button>
            )}

            {!isGlobal && (
                <div
                    className="sidebar-right__studio"
                    style={{
                        display: (isMobile || isOpen) ? 'flex' : 'none',
                        height: isMobile ? undefined : `${split}%`,
                        flex: isMobile ? '1 1 auto' : undefined,
                    }}
                >
                    <div className="sidebar-right__section-header sidebar-ui__section-header">
                        <h2 className="sidebar-right__section-title sidebar-ui__section-title">{t('studio')}</h2>
                        <div className="sidebar-right__header-actions">
                            <button
                                onClick={handleExpandStudio}
                                className="studio-gen-btn sidebar-right__workspace-btn"
                                disabled={!hasFiles}
                                title={hasFiles ? t('openWorkspace') : t('sourcesRequiredHint')}
                                aria-label={t('openWorkspace')}
                            >
                                Workspace
                            </button>
                            {!isMobile && (
                                <button
                                    onClick={() => sidebar.setIsRightSidebarOpen(false)}
                                    className="sidebar-right__collapse-btn"
                                    title={t('collapseSidebar')}
                                    aria-label={t('collapseSidebar')}
                                    aria-expanded={true}
                                >
                                    <PanelRightClose size={20} />
                                </button>
                            )}
                        </div>
                    </div>

                    {hasAnyStudio && (
                        <div
                            className="sidebar-right__studio-actions"
                            style={{ opacity: hasFiles ? 1 : 0.4 }}
                            title={!hasFiles ? t('sourcesRequiredHint') : undefined}
                        >
                            {sc.cards && <button onClick={() => handleGenerate('cards')} disabled={generating || !hasFiles} className="studio-gen-btn">{t('flashcards')}</button>}
                            {sc.presentation && <button onClick={() => handleGenerate('presentation')} disabled={generating || !hasFiles} className="studio-gen-btn">{t('slides')}</button>}
                            {sc.podcast && <button onClick={() => handleGenerate('podcast')} disabled={generating || !hasFiles} className="studio-gen-btn" title={t('podcastEnglishOnly')}>{t('podcastEnglishOnly')}</button>}

                            {sc.abstract && <button onClick={() => handleGenerate('abstract')} disabled={generating || !hasFiles} className="studio-gen-btn">{t('abstract')}</button>}
                            {sc.briefingDoc && <button onClick={() => handleGenerate('briefing_doc')} disabled={generating || !hasFiles} className="studio-gen-btn">{t('briefingDoc')}</button>}
                            {sc.faq && <button onClick={() => handleGenerate('faq')} disabled={generating || !hasFiles} className="studio-gen-btn">{t('faq')}</button>}
                            {sc.studyGuide && <button onClick={() => handleGenerate('study_guide')} disabled={generating || !hasFiles} className="studio-gen-btn">{t('studyGuide')}</button>}
                            {sc.timeline && <button onClick={() => handleGenerate('timeline')} disabled={generating || !hasFiles} className="studio-gen-btn">{t('timeline')}</button>}
                            {sc.quiz && <button onClick={() => handleGenerate('quiz')} disabled={generating || !hasFiles} className="studio-gen-btn">{t('quiz')}</button>}
                        </div>
                    )}

                    {generating && (
                        <>
                            <div className="source-card sidebar-right__generating-card sidebar-ui__item-card">
                                <div className="sidebar-right__generating-row">
                                    <Loader2 className="animate-spin" size={16} />
                                    <span className="sidebar-right__generating-text">
                                        {podcastProgress ? podcastProgress.message : t('generatingContent')}
                                    </span>
                                </div>
                                {podcastProgress?.step === 'audio' && podcastProgress.total && (
                                    <div style={{ marginTop: '6px', height: '3px', background: 'var(--border-color)', borderRadius: '2px', overflow: 'hidden' }}>
                                        <div style={{
                                            height: '100%',
                                            width: `${((podcastProgress.current || 0) / podcastProgress.total) * 100}%`,
                                            background: 'var(--accent-primary)',
                                            borderRadius: '2px',
                                            transition: 'width 0.3s ease',
                                        }} />
                                    </div>
                                )}
                            </div>
                            <ContentRowSkeleton />
                        </>
                    )}

                    <ul className="sidebar-right__studio-list sidebar-ui__list sidebar-ui__list--stack">
                        {studioItems.map(item => {
                            if (item.itemType === 'generated') {
                                return (
                                    <li key={item.id} className="source-card sidebar-right__item-card sidebar-ui__item-card">
                                        <div className="sidebar-right__item-row sidebar-ui__item-row">
                                            <div className="sidebar-right__item-main sidebar-ui__item-main">
                                                <button
                                                    onClick={() => handleSelectContent(item as GeneratedContent)}
                                                    className="text-button source-title sidebar-right__item-title sidebar-ui__item-title"
                                                >
                                                    {item.title}
                                                </button>
                                                <div className="source-meta sidebar-right__item-meta sidebar-ui__item-meta">{artifactTypeLabel((item as GeneratedContent).type, t)} • {new Date(item.createdAt).toLocaleDateString()}</div>
                                            </div>
                                            <button onClick={(e) => handleDeleteGeneratedContent(item.id, e)} className="settings-toggle sidebar-right__item-delete sidebar-ui__item-delete" aria-label={`${t('deleteItem')} ${item.title}`}>
                                                <Trash2 size={14} />
                                            </button>
                                        </div>
                                    </li>
                                );
                            }

                            const chatEntry = item as ChatEntry;
                            const isActive = chatEntry.id === activeChatId;
                            const isAcademic = item.itemType === 'academic_research';
                            return (
                                <li
                                    key={chatEntry.id}
                                    className="source-card sidebar-right__item-card sidebar-ui__item-card"
                                    style={isActive ? { borderColor: 'var(--accent-primary)', background: 'var(--accent-primary-translucent, rgba(99,102,241,0.08))' } : undefined}
                                >
                                    <div className="sidebar-right__item-row sidebar-ui__item-row">
                                        <div className="sidebar-right__item-main sidebar-ui__item-main">
                                            <button
                                                onClick={() => handleSelectChat(chatEntry)}
                                                className="text-button source-title sidebar-right__item-title sidebar-right__item-title--with-icon sidebar-ui__item-title"
                                            >
                                                {isAcademic ? <GraduationCap size={14} className="flex-shrink-0" /> : <Search size={14} className="flex-shrink-0" />}
                                                {chatEntry.title}
                                            </button>
                                            <div className="source-meta sidebar-right__item-meta sidebar-ui__item-meta">{isAcademic ? 'Academic' : 'Research'} • {new Date(item.createdAt).toLocaleDateString()}</div>
                                        </div>
                                        <button onClick={(e) => handleDeleteChat(chatEntry.id, e)} className="settings-toggle sidebar-right__item-delete sidebar-ui__item-delete" aria-label={`${t('deleteItem')} ${chatEntry.title}`}>
                                            <Trash2 size={14} />
                                        </button>
                                    </div>
                                </li>
                            );
                        })}

                        {studioItems.length === 0 && !generating && (
                            <li className="sidebar-right__empty-row sidebar-ui__empty">
                                {!hasFiles ? t('sourcesRequiredHint') : t('noContent')}
                            </li>
                        )}
                    </ul>
                </div>
            )}

            {!isMobile && isOpen && !isGlobal && (
                // eslint-disable-next-line jsx-a11y/no-noninteractive-element-interactions
                <div
                    onMouseDown={() => sidebar.setIsResizingRightVertical(true)}
                    role="separator"
                    aria-orientation="horizontal"
                    tabIndex={0} // eslint-disable-line jsx-a11y/no-noninteractive-tabindex
                    aria-label={t('resizeRightSidebar')}
                    aria-valuemin={20}
                    aria-valuemax={80}
                    aria-valuenow={split}
                    onKeyDown={handleKeyDown}
                    className="resize-handle sidebar-right__resize-handle sidebar-ui__resize-handle"
                />
            )}

            <div
                className="sidebar-right__history"
                style={{
                    display: (isMobile || isOpen) ? 'flex' : 'none',
                    height: isMobile ? undefined : (isGlobal ? '100%' : `${100 - split}%`),
                    flex: isMobile ? '1 1 auto' : undefined,
                }}
            >
                <div className="sidebar-right__section-header sidebar-ui__section-header">
                    <h2 className="sidebar-right__section-title sidebar-ui__section-title">{t('history')}</h2>
                    <button
                        className="send-button sidebar-right__new-chat-btn"
                        onClick={handleNewChat}
                        title={t('newChat')}
                        aria-label={t('newChat')}
                    >
                        <Plus size={16} />
                    </button>
                </div>

                <ul className="sidebar-right__history-list sidebar-ui__list sidebar-ui__list--stack">
                    {standardChats.map(chatEntry => {
                        const isActive = chatEntry.id === activeChatId;
                        return (
                            <li
                                key={chatEntry.id}
                                className="source-card sidebar-right__item-card sidebar-ui__item-card"
                                style={isActive ? { borderColor: 'var(--accent-primary)', background: 'var(--accent-primary-translucent, rgba(99,102,241,0.08))' } : undefined}
                            >
                                <div className="sidebar-right__item-row sidebar-ui__item-row">
                                    <div className="sidebar-right__history-main sidebar-ui__item-main">
                                        <button
                                            onClick={() => !isActive && handleSelectChat(chatEntry)}
                                            className="text-button source-title sidebar-right__history-title sidebar-ui__item-title"
                                            style={isActive ? { color: 'var(--accent-primary)', fontWeight: 600 } : undefined}
                                        >
                                            {chatEntry.title}
                                        </button>
                                        <div className="source-meta">{new Date(chatEntry.createdAt).toLocaleDateString()}</div>
                                    </div>
                                    <button
                                        onClick={(e) => handleDeleteChat(chatEntry.id, e)}
                                        className="settings-toggle sidebar-right__item-delete sidebar-ui__item-delete"
                                        aria-label={`${t('deleteItem')} ${chatEntry.title}`}
                                    >
                                        <Trash2 size={14} />
                                    </button>
                                </div>
                            </li>
                        );
                    })}

                    {standardChats.length === 0 && (
                        <li className="sidebar-right__history-empty sidebar-ui__empty">{t('noHistory')}</li>
                    )}
                </ul>
            </div>
        </aside>
    );
};

export const SidebarRight = memo(SidebarRightComp);
