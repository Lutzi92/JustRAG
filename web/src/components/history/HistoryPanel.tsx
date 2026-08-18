import React, { memo, useCallback, useMemo } from 'react';
import {
    MessageSquare, Search, GraduationCap, FileText, Loader2, Plus, Trash2, ArrowLeft,
} from 'lucide-react';
import type { ChatEntry, GeneratedContent } from '../../types';
import { artifactTypeLabel } from '../../utils/artifactTypes';
import { useTheme } from '../../contexts/ThemeContext';
import { useIsMobileContext } from '../../contexts/MobileContext';
import { useKbCore } from '../../contexts/KbCoreContext';
import { useKbChat } from '../../contexts/KbChatContext';
import { useKbData } from '../../contexts/KbDataContext';
import { useKbLayout } from '../../contexts/KbLayoutContext';
import { SidebarShell } from '../sidebar-shell/SidebarShell';
import { ContentRowSkeleton } from '../Skeleton';
import { buildHistoryItems, type HistoryItem, type HistoryKind } from './historyItems';
import '../sidebar-primitives.css';
import './HistoryPanel.css';

const KIND_ICON: Record<HistoryKind, typeof MessageSquare> = {
    chat: MessageSquare,
    artifact: FileText,
    research: Search,
    academic: GraduationCap,
};

const HistoryPanelComp: React.FC = () => {
    const { t } = useTheme();
    const isMobile = useIsMobileContext();
    const { currentKb, setKbView, handleGoHome } = useKbCore();
    const { chat } = useKbChat();
    const { content, handleSelectContent } = useKbData();
    const { sidebar } = useKbLayout();

    const { chats, activeChatId, handleSelectChat, handleDeleteChat, handleNewChat } = chat;
    const { generatedContent, generating, handleDeleteGeneratedContent, podcastProgress } = content;

    const items = useMemo(
        () => buildHistoryItems({ chats, generatedContent }),
        [chats, generatedContent],
    );

    // Ein Klick öffnet immer den Reiter, in dem der Eintrag lebt. Artefakte
    // gehen in den Workspace, Recherchen in den Bericht-Reiter — die Liste ist
    // vereint, die Zielansichten sind es nicht.
    const openItem = useCallback((item: HistoryItem) => {
        if (item.kind === 'artifact') {
            handleSelectContent(item.source as GeneratedContent);
            setKbView('workspace');
            return;
        }
        handleSelectChat(item.source as ChatEntry);
        setKbView(item.kind === 'research' ? 'research'
            : item.kind === 'academic' ? 'academic_research'
            : 'chat');
    }, [handleSelectContent, handleSelectChat, setKbView]);

    const deleteItem = useCallback((item: HistoryItem, e: React.MouseEvent) => {
        if (item.kind === 'artifact') handleDeleteGeneratedContent(item.id, e);
        else handleDeleteChat(item.id, e);
    }, [handleDeleteGeneratedContent, handleDeleteChat]);

    const label = (item: HistoryItem) =>
        item.kind === 'artifact' && item.artifactType
            ? artifactTypeLabel(item.artifactType, t)
            : item.kind === 'research' ? t('research')
            : item.kind === 'academic' ? t('academicResearch')
            : t('chat');

    return (
        <SidebarShell
            side="left"
            isOpen={sidebar.isLeftSidebarOpen}
            width={sidebar.leftSidebarWidth}
            onExpand={() => sidebar.setIsLeftSidebarOpen(true)}
            onCollapse={() => sidebar.setIsLeftSidebarOpen(false)}
            expandLabel={t('expandSidebar')}
            collapseLabel={t('collapseSidebar')}
            collapsedPreview={
                <>
                    <div className="sidebar-ui__collapsed-divider" />
                    <MessageSquare size={20} color="var(--accent-primary)" />
                </>
            }
        >
            {isMobile && (
                <div className="history-panel__mobile-header">
                    <button onClick={handleGoHome} className="history-panel__back" aria-label={t('back')}>
                        <ArrowLeft size={20} />
                    </button>
                    <span className="history-panel__kb-name">{currentKb?.name}</span>
                </div>
            )}

            <div className="history-panel">
                <div className="sidebar-ui__section-header">
                    <h2 className="sidebar-ui__section-title">{t('history')}</h2>
                    <button
                        className="send-button history-panel__new-chat-btn"
                        onClick={handleNewChat}
                        title={t('newChat')}
                        aria-label={t('newChat')}
                    >
                        <Plus size={16} />
                    </button>
                </div>

                {generating && (
                    <>
                        <div className="source-card history-panel__generating-card sidebar-ui__item-card">
                            <div className="history-panel__generating-row">
                                <Loader2 className="animate-spin" size={16} />
                                <span>{podcastProgress ? podcastProgress.message : t('generatingContent')}</span>
                            </div>
                        </div>
                        <ContentRowSkeleton />
                    </>
                )}

                <ul className="sidebar-ui__list sidebar-ui__list--stack">
                    {items.map(item => {
                        const Icon = KIND_ICON[item.kind];
                        const isActive = item.kind !== 'artifact' && item.id === activeChatId;
                        return (
                            <li
                                key={`${item.kind}-${item.id}`}
                                className={`source-card sidebar-ui__item-card${isActive ? ' history-panel__item--active' : ''}`}
                            >
                                <div className="sidebar-ui__item-row">
                                    <div className="sidebar-ui__item-main">
                                        <button
                                            data-testid="history-item-title"
                                            onClick={() => openItem(item)}
                                            className="text-button source-title sidebar-ui__item-title history-panel__item-title"
                                        >
                                            <Icon size={14} className="flex-shrink-0" aria-hidden="true" />
                                            {item.title}
                                        </button>
                                        <div className="source-meta sidebar-ui__item-meta">
                                            {label(item)} • {new Date(item.createdAt).toLocaleDateString()}
                                        </div>
                                    </div>
                                    <button
                                        onClick={(e) => deleteItem(item, e)}
                                        className="settings-toggle sidebar-ui__item-delete"
                                        aria-label={`${t('deleteItem')} ${item.title}`}
                                    >
                                        <Trash2 size={14} />
                                    </button>
                                </div>
                            </li>
                        );
                    })}

                    {items.length === 0 && !generating && (
                        <li className="sidebar-ui__empty">{t('noHistory')}</li>
                    )}
                </ul>
            </div>
        </SidebarShell>
    );
};

export const HistoryPanel = memo(HistoryPanelComp);
