import { useState, lazy, Suspense, useCallback, useEffect } from 'react';
import axios from 'axios';
import { motion } from 'framer-motion';
import { Loader2, HelpCircle, ArrowLeft } from 'lucide-react';
import type { KnowledgeBase, GeneratedContent } from './types';
import { isMarkdownArtifact } from './utils/artifactTypes';
import { API_BASE_URL } from './api';
import { useTheme } from './contexts/ThemeContext';
import { useAuth } from './contexts/AuthContext';
import { ModalProvider, useModalContext } from './contexts/ModalContext';
import { ToastProvider, useToast } from './contexts/ToastContext';
import { ToastContainer } from './components/ToastContainer';
import { KbCoreProvider, type KbCoreContextValue } from './contexts/KbCoreContext';
import { KbChatProvider, type KbChatContextValue } from './contexts/KbChatContext';
import { KbDataProvider, type KbDataContextValue } from './contexts/KbDataContext';
import { KbLayoutProvider, type KbLayoutContextValue } from './contexts/KbLayoutContext';
import { useReducedMotion, getMotionProps } from './hooks/useReducedMotion';

// Hooks
import { useSidebarResize } from './hooks/useSidebarResize';
import { useChat } from './hooks/useChat';
import { useFileManagement } from './hooks/useFileManagement';
import { useWebTools } from './hooks/useWebTools';
import { useGeneratedContent } from './hooks/useGeneratedContent';
import { useKnowledgeBases } from './hooks/useKnowledgeBases';
import { useSharing } from './hooks/useSharing';
import { useRssFeeds } from './hooks/useRssFeeds';
import { useConfluenceSources } from './hooks/useConfluenceSources';
import { useGitRepoSources } from './hooks/useGitRepoSources';
import { useViewState, type ViewType, type KbViewType } from './hooks/useViewState';
import { useKbSettings } from './hooks/useKbSettings';
import { useKbLifecycle } from './hooks/useKbLifecycle';

// Components
import { HomeView } from './components/HomeView';
import { KbWorkspaceLayout } from './components/KbWorkspaceLayout';
import { KbWorkspaceModals } from './components/KbWorkspaceModals';

// Lazy load heavy components
const GlobalKbSettings = lazy(() => import('./components/GlobalKbSettings').then(module => ({ default: module.GlobalKbSettings })));
const KbSettingsPanel = lazy(() => import('./components/kb-settings/KbSettingsPanel').then(module => ({ default: module.KbSettingsPanel })));

import { OnboardingTour } from './components/OnboardingTour';
import { Footer } from './components/Footer';
import { LegalPage } from './components/LegalPage';
import { viewportHeight } from './utils/viewport';

// Lazy Loaded Components
const AdminUI = lazy(() => import('./AdminUI'));
const Profile = lazy(() => import('./Profile'));
const AgentsView = lazy(() => import('./components/agents/AgentsView'));

const LoadingFallback = () => (
  <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', height: '100%', width: '100%' }}>
    <Loader2 className="animate-spin" size={32} color="var(--accent-primary)" />
  </div>
);

function OnboardingHelpButton({ onClick }: { onClick: () => void }) {
  const { t } = useTheme();
  return (
    <button
      onClick={onClick}
      title={t('onboardingReopenTour')}
      aria-label={t('onboardingReopenTour')}
      style={{
        position: 'fixed',
        bottom: '24px',
        left: '24px',
        width: '44px',
        height: '44px',
        borderRadius: '50%',
        border: '1px solid var(--border-color)',
        background: 'var(--bg-secondary)',
        color: 'var(--text-secondary)',
        cursor: 'pointer',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        zIndex: 100,
        boxShadow: '0 2px 8px rgba(0,0,0,0.1)',
      }}
    >
      <HelpCircle size={20} />
    </button>
  );
}

export default function AuthenticatedApp() {
  return (
    <ModalProvider>
      <ToastProvider>
        <AuthenticatedAppInner />
        <ToastContainer />
      </ToastProvider>
    </ModalProvider>
  );
}

function AuthenticatedAppInner() {
  const { theme, t } = useTheme();
  const { user, token, logout: onLogout, updateUser: onUpdateUser } = useAuth();
  const { showConfirm } = useModalContext();
  const toast = useToast();
  const reducedMotion = useReducedMotion();

  // Local state kept here to avoid circular deps with useKnowledgeBases
  const [view, setView] = useState<ViewType>('home');
  const [kbView, setKbView] = useState<KbViewType>('chat');
  const [currentKb, setCurrentKb] = useState<KnowledgeBase | null>(null);

  // Query-scoped mindmap: which answer's subgraph the mindmap view is scoped to.
  const [scopedMindmapMessageId, setScopedMindmapMessageId] = useState<string | null>(null);

  const handleViewGraphForMessage = useCallback((messageId: string) => {
    setScopedMindmapMessageId(messageId);
    setKbView('mindmap');
  }, [setKbView]);

  const onCloseMindmap = useCallback(() => { setScopedMindmapMessageId(null); setKbView('chat'); }, [setKbView]);
  const onShowWholeKb = useCallback(() => { setScopedMindmapMessageId(null); }, []); // stays in mindmap; messageId=null reloads full graph

  // New extracted hooks
  const kbSettings = useKbSettings();
  const viewState = useViewState({ setView, setKbView, setShowSettings: kbSettings.setShowSettings });

  // Onboarding Tour
  const [showOnboarding, setShowOnboarding] = useState(() => !localStorage.getItem('onboardingCompleted'));
  const handleCloseOnboarding = useCallback(() => { setShowOnboarding(false); localStorage.setItem('onboardingCompleted', 'true'); }, []);


  // Sidebar resize
  const sidebar = useSidebarResize();

  // Existing hooks
  const fileMgmt = useFileManagement({ currentKb });

  const chat = useChat({
    currentKb, files: fileMgmt.files, enhance: kbSettings.enhance,
    reasoningEnabled: kbSettings.reasoningEnabled, reasoningLevel: kbSettings.reasoningLevel,
    agentSelection: kbSettings.agentSelection, setAgentSelection: kbSettings.setAgentSelection,
    onResearchLoaded: () => setKbView('research'),
    onAcademicResearchLoaded: () => setKbView('academic_research'),
  });

  const webTools = useWebTools({
    currentKb,
    fetchFiles: fileMgmt.fetchFiles,
  });

  const { rssFeeds, rssLoading, fetchRssFeeds, addRssFeed, updateRssFeed, deleteRssFeed, pollFeedNow } = useRssFeeds({
    currentKb,
    fetchFiles: fileMgmt.fetchFiles,
  });

  const confluenceHook = useConfluenceSources({
    currentKb,
    fetchFiles: fileMgmt.fetchFiles,
  });

  const gitRepoHook = useGitRepoSources({
    currentKb,
    fetchFiles: fileMgmt.fetchFiles,
  });

  const content = useGeneratedContent({
    currentKb, files: fileMgmt.files,
  });

  const kbMgmt = useKnowledgeBases({
    onKBSelected: chat.handleNewChat,
    handleGoHome: viewState.handleGoHome,
    setCurrentKb, setIsPro: kbSettings.setIsPro, setKbView, setView,
    setSelectedContent: content.setSelectedContent,
    setGeneratedContent: content.setGeneratedContent,
  });

  const sharing = useSharing({ username: user?.username });

  // Lifecycle effects
  useKbLifecycle({
    token, currentKb,
    fetchKBs: kbMgmt.fetchKBs,
    fetchAvailableConfigs: kbSettings.fetchAvailableConfigs,
    fetchFiles: fileMgmt.fetchFiles,
    fetchChats: chat.fetchChats,
    fetchGeneratedContent: content.fetchGeneratedContent,
    fetchRssFeeds,
    fetchConfluenceSources: confluenceHook.fetchSources,
    fetchConfluenceConnectionInfo: confluenceHook.fetchConnectionInfo,
    showShareModal: sharing.showShareModal,
    setShowShareModal: sharing.setShowShareModal,
    showSettings: kbSettings.showSettings,
    setShowSettings: kbSettings.setShowSettings,
  });

  const { fetchGitRepoSources } = gitRepoHook;
  useEffect(() => {
    if (currentKb) {
      fetchGitRepoSources(currentKb.id);
    }
  }, [currentKb, fetchGitRepoSources]);

  // Refetch the overview whenever the user lands back on it. useKbLifecycle
  // fetches once per token, so anything that changed a KB's visibility
  // elsewhere in the session — publishing one from the admin UI is the case
  // this exists for — only reached the overview after a full page reload.
  const { fetchKBs } = kbMgmt;
  useEffect(() => {
    if (view === 'home') {
      void fetchKBs({ silent: true });
    }
  }, [view, fetchKBs]);

  // Cross-hook handlers. The setters are destructured into stable locals so
  // the useCallback dep arrays don't have to depend on the whole hook objects
  // (which are recreated each render and would defeat the memoization).
  const { setIsRightSidebarOpen } = sidebar;
  const { setSelectedContent, setCurrentCardIndex, setIsAnswerVisible, setShowContentModal } = content;

  const handleExpandStudio = useCallback(() => {
    setKbView('studio');
    setIsRightSidebarOpen(false);
  }, [setIsRightSidebarOpen]);

  const handleSelectContent = useCallback((item: GeneratedContent) => {
    setSelectedContent(item);
    if (isMarkdownArtifact(item.type)) {
      setKbView('studio');
      setIsRightSidebarOpen(false);
    } else {
      if (item.type === 'flashcards') {
        setCurrentCardIndex(0);
        setIsAnswerVisible(false);
      }
      setShowContentModal(true);
    }
  }, [setSelectedContent, setCurrentCardIndex, setIsAnswerVisible, setShowContentModal, setIsRightSidebarOpen]);

  const handleUpdateKBSettings = async (data: Record<string, unknown>) => {
    if (!currentKb) return;

    if (data.embeddingModel && data.embeddingModel !== currentKb.embeddingModel && currentKb.embeddingModel !== null) {
      if (!await showConfirm(t('embeddingModelChangeConfirm'))) {
        return;
      }
    }

    try {
      const res = await axios.patch(`${API_BASE_URL}/api/kb/${currentKb.id}`, data);
      setCurrentKb(res.data);
      kbMgmt.setKbs(prev => prev.map(kb => kb.id === res.data.id ? res.data : kb));
      if (typeof data.isPro === 'boolean') kbSettings.setIsPro(data.isPro);
    } catch (err: unknown) {
      console.error('Failed to update KB settings:', err);
      toast.error(t('settingsUpdateError'));
    }
  };

  // Early returns for non-KB views
  if (view === 'admin') {
    return (
      <Suspense fallback={<LoadingFallback />}>
        <AdminUI onBack={viewState.handleGoHome} user={user} onEditGlobalKb={(kb) => kbMgmt.handleOpenGlobalKbSettings(kb as KnowledgeBase, { stopPropagation: () => { } } as React.MouseEvent)} />
      </Suspense>
    );
  }

  if (view === 'global-kb-settings' && currentKb?.isGlobal) {
    return (
      <Suspense fallback={<div className="loading-spinner"><Loader2 className="animate-spin" /></div>}>
        <GlobalKbSettings
          kb={currentKb}
          onBack={() => { kbMgmt.fetchKBs(); viewState.handleGoHome(); }}
          onOpenKb={() => { kbMgmt.handleSelectKB(currentKb); }}
          onUpdate={(updated) => {
            setCurrentKb(updated);
            kbMgmt.setGlobalKbs(prev => prev.map(k => k.id === updated.id ? updated : k));
          }}
        />
      </Suspense>
    );
  }

  // Per-KB RAG settings / evals / workflow. Mirrors the 'global-kb-settings'
  // branch above (that one is the system-admin editor for public KBs; this one
  // is the KB-admin tuning surface, and every endpoint it calls is
  // kbAdminChain). Entry point: the sliders icon on a KB card in HomeView,
  // shown only for myRole admin|owner.
  if (view === 'kb-settings' && currentKb) {
    return (
      <div className="app-container" data-theme={theme}>
        <div style={{ display: 'flex', flexDirection: 'column', height: viewportHeight('100dvh', '100vh'), width: '100%', overflow: 'auto' }}>
          <div style={{ padding: '1rem 1.5rem 0' }}>
            <button
              type="button"
              onClick={() => { kbMgmt.fetchKBs(); viewState.handleGoHome(); }}
              style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', background: 'transparent', border: '1px solid var(--border-color)', color: 'var(--text-primary)', padding: '0.45rem 0.75rem', borderRadius: 8, cursor: 'pointer', fontSize: '0.85rem' }}
            >
              <ArrowLeft size={16} aria-hidden="true" /> {currentKb.name}
            </button>
          </div>
          <div style={{ padding: '1.25rem 1.5rem 2rem' }}>
            <Suspense fallback={<LoadingFallback />}>
              <KbSettingsPanel kbId={currentKb.id} onCreateAgent={() => setView('agents')} />
            </Suspense>
          </div>
        </div>
      </div>
    );
  }

  if (view === 'profile' && user) {
    return (
      <div className="app-container" data-theme={theme}>
        <div style={{ display: 'flex', height: viewportHeight('100dvh', '100vh'), width: '100%', overflow: 'auto' }}>
          <Suspense fallback={<LoadingFallback />}>
            <Profile user={user} onBack={viewState.handleGoHome} onUpdateUser={onUpdateUser} />
          </Suspense>
        </div>
      </div>
    );
  }

  if (view === 'terms' || view === 'privacy' || view === 'accessibility') {
    return <LegalPage page={view} onBack={viewState.handleGoHome} />;
  }

  if (view === 'agents') {
    return (
      <div className="app-container" data-theme={theme}>
        <div style={{ display: 'flex', height: viewportHeight('100dvh', '100vh'), width: '100%', overflow: 'auto' }}>
          <Suspense fallback={<LoadingFallback />}>
            <AgentsView
              onBack={viewState.handleGoHome}
              availableModels={[...new Set(kbSettings.availableConfigs.flatMap(c => c.chat_models))]}
            />
          </Suspense>
        </div>
      </div>
    );
  }

  if (view === 'home') {
    return (
      <motion.div
        key="home"
        {...getMotionProps(reducedMotion)}
        initial={{ opacity: 0 }}
        animate={{ opacity: 1 }}
        transition={{ duration: 0.2 }}
      >
        <HomeView
          kbs={kbMgmt.kbs}
          globalKbs={kbMgmt.globalKbs}
          currentKb={currentKb}
          availableConfigs={kbSettings.availableConfigs}
          copySuccess={sharing.copySuccess}
          onCopyUserId={sharing.copyUserId}
          onLogout={onLogout}
          onViewProfile={() => setView('profile')}
          onViewAdmin={() => setView('admin')}
          onViewAgents={() => setView('agents')}
          onCreateKB={kbMgmt.handleCreateKB}
          onSelectKB={kbMgmt.handleSelectKB}
          onDeleteKB={kbMgmt.handleDeleteKB}
          removingKb={kbMgmt.removingKb}
          onCreateGlobalKB={kbMgmt.handleCreateGlobalKB}
          onSubscriptionChange={() => { kbMgmt.fetchKBs(); }}
          onOpenKbById={kbMgmt.handleOpenKbById}
          onDeleteGlobalKB={kbMgmt.handleDeleteGlobalKB}
          onOpenGlobalKbSettings={kbMgmt.handleOpenGlobalKbSettings}
          onOpenKbSettings={kbMgmt.handleOpenKbSettings}
          onRenameKB={kbMgmt.handleRenameKB}
          onOpenShare={sharing.handleOpenShare}
          onUpdateKBSettings={handleUpdateKBSettings}
          showShareModal={sharing.showShareModal}
          setShowShareModal={sharing.setShowShareModal}
          sharingKb={sharing.sharingKb}
          shareUserId={sharing.shareUserId}
          setShareUserId={sharing.setShareUserId}
          shareTargetUser={sharing.shareTargetUser}
          shareLoading={sharing.shareLoading}
          sharePermission={sharing.sharePermission}
          setSharePermission={sharing.setSharePermission}
          onLookupUser={sharing.lookupUser}
          onConfirmShare={sharing.confirmShare}
          notFoundUsername={sharing.notFoundUsername}
          onPendingInvited={sharing.clearNotFound}
          showSettings={kbSettings.showSettings}
          setShowSettings={kbSettings.setShowSettings}
        />
        <Footer onNavigate={(page) => setView(page)} />
        <OnboardingHelpButton onClick={() => setShowOnboarding(true)} />
        <OnboardingTour show={showOnboarding} onClose={handleCloseOnboarding} />
      </motion.div>
    );
  }

  if (!currentKb) {
    return (
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', height: '100%', width: '100%' }}>
        <Loader2 className="animate-spin" size={32} color="var(--accent-primary)" />
      </div>
    );
  }

  const coreValue: KbCoreContextValue = {
    currentKb,
    isPro: kbSettings.isPro,
    availableConfigs: kbSettings.availableConfigs,
    kbView,
    setKbView,
    scopedMindmapMessageId,
    onViewGraphForMessage: handleViewGraphForMessage,
    onCloseMindmap,
    onShowWholeKb,
    kbMgmt,
    handleGoHome: viewState.handleGoHome,
    handleViewHome: viewState.handleViewHome,
    onViewAgents: () => setView('agents'),
    handleUpdateKBSettings,
  };

  const chatValue: KbChatContextValue = {
    chat,
    enhance: kbSettings.enhance,
    setEnhance: kbSettings.setEnhance,
    reasoningEnabled: kbSettings.reasoningEnabled,
    setReasoningEnabled: kbSettings.setReasoningEnabled,
    reasoningLevel: kbSettings.reasoningLevel,
    setReasoningLevel: kbSettings.setReasoningLevel,
    showSettings: kbSettings.showSettings,
    setShowSettings: kbSettings.setShowSettings,
    researchRunning: kbSettings.researchRunning,
    academicResearchRunning: kbSettings.academicResearchRunning,
    setResearchRunning: kbSettings.setResearchRunning,
    setAcademicResearchRunning: kbSettings.setAcademicResearchRunning,
    agentSelection: kbSettings.agentSelection,
    setAgentSelection: kbSettings.setAgentSelection,
  };

  const dataValue: KbDataContextValue = {
    fileMgmt,
    webTools,
    content,
    sharing,
    rssFeeds,
    rssLoading,
    fetchRssFeeds,
    addRssFeed,
    updateRssFeed,
    deleteRssFeed,
    pollFeedNow,
    confluenceSources: confluenceHook.confluenceSources,
    confluenceConnection: confluenceHook.confluenceConnection,
    confluenceLoading: confluenceHook.confluenceLoading,
    fetchConfluenceSources: confluenceHook.fetchSources,
    fetchConfluenceConnectionInfo: confluenceHook.fetchConnectionInfo,
    saveConfluenceConnection: confluenceHook.saveConnection,
    addConfluenceSource: confluenceHook.addSource,
    updateConfluenceSource: confluenceHook.updateSource,
    deleteConfluenceSource: confluenceHook.deleteSource,
    syncConfluenceNow: confluenceHook.syncNow,
    fetchConfluenceSpaces: confluenceHook.fetchSpaces,
    fetchConfluenceSpacePages: confluenceHook.fetchSpacePages,
    fetchConfluencePageChildren: confluenceHook.fetchPageChildren,
    fetchConfluenceAllSpacePages: confluenceHook.fetchAllSpacePages,
    gitRepoSources: gitRepoHook.gitRepoSources,
    gitRepoLoading: gitRepoHook.gitRepoLoading,
    fetchGitRepoSources: gitRepoHook.fetchGitRepoSources,
    addGitRepoSource: gitRepoHook.addGitRepoSource,
    updateGitRepoSource: gitRepoHook.updateGitRepoSource,
    deleteGitRepoSource: gitRepoHook.deleteGitRepoSource,
    syncGitRepoNow: gitRepoHook.syncGitRepoNow,
    handleSelectContent,
    handleExpandStudio,
  };

  const layoutValue: KbLayoutContextValue = {
    sidebar,
  };

  return (
    <KbCoreProvider value={coreValue}>
      <KbChatProvider value={chatValue}>
        <KbDataProvider value={dataValue}>
          <KbLayoutProvider value={layoutValue}>
            <div>
              <a href="#chat-message-input" className="skip-link">{t('skipToChat')}</a>
              <KbWorkspaceLayout
                mobileTab={viewState.mobileTab}
                setMobileTab={viewState.setMobileTab}
                swipeHandlers={viewState.swipeHandlers}
              />
              <KbWorkspaceModals />
              <OnboardingTour show={showOnboarding} onClose={handleCloseOnboarding} />
            </div>
          </KbLayoutProvider>
        </KbDataProvider>
      </KbChatProvider>
    </KbCoreProvider>
  );
}
