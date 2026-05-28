import { useState, lazy, Suspense, useCallback } from 'react';
import axios from 'axios';
import { motion } from 'framer-motion';
import { Loader2, HelpCircle } from 'lucide-react';
import type { KnowledgeBase, GeneratedContent } from './types';
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
import { useViewState, type ViewType, type KbViewType } from './hooks/useViewState';
import { useKbSettings } from './hooks/useKbSettings';
import { useKbLifecycle } from './hooks/useKbLifecycle';

// Components
import { HomeView } from './components/HomeView';
import { KbWorkspaceLayout } from './components/KbWorkspaceLayout';
import { KbWorkspaceModals } from './components/KbWorkspaceModals';

// Lazy load heavy components
const GlobalKbSettings = lazy(() => import('./components/GlobalKbSettings').then(module => ({ default: module.GlobalKbSettings })));

import { OnboardingTour } from './components/OnboardingTour';
import { Footer } from './components/Footer';
import { LegalPage } from './components/LegalPage';
import { viewportHeight } from './utils/viewport';

// Lazy Loaded Components
const AdminUI = lazy(() => import('./AdminUI'));
const Profile = lazy(() => import('./Profile'));

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

  // Cross-hook handlers
  const handleExpandStudio = useCallback(() => {
    setKbView('studio');
    sidebar.setIsRightSidebarOpen(false);
  }, [sidebar.setIsRightSidebarOpen]);

  const handleSelectContent = useCallback((item: GeneratedContent) => {
    content.setSelectedContent(item);
    if (item.type === 'analysis' || item.type === 'abstract') {
      setKbView('studio');
      sidebar.setIsRightSidebarOpen(false);
    } else {
      if (item.type === 'flashcards') {
        content.setCurrentCardIndex(0);
        content.setIsAnswerVisible(false);
      }
      content.setShowContentModal(true);
    }
  }, [content.setSelectedContent, content.setCurrentCardIndex, content.setIsAnswerVisible, content.setShowContentModal, sidebar.setIsRightSidebarOpen]);

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

  if (view === 'privacy' || view === 'accessibility') {
    return <LegalPage page={view} onBack={viewState.handleGoHome} />;
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
          onCreateKB={kbMgmt.handleCreateKB}
          onSelectKB={kbMgmt.handleSelectKB}
          onDeleteKB={kbMgmt.handleDeleteKB}
          onCreateGlobalKB={kbMgmt.handleCreateGlobalKB}
          onDeleteGlobalKB={kbMgmt.handleDeleteGlobalKB}
          onOpenGlobalKbSettings={kbMgmt.handleOpenGlobalKbSettings}
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
    kbMgmt,
    handleGoHome: viewState.handleGoHome,
    handleViewHome: viewState.handleViewHome,
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
