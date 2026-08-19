import { useState, useCallback, useMemo } from 'react';
import { type MobileTab } from '../components/MobileTabBar';
import { useSwipeGesture } from './useSwipeGesture';
import { deriveActiveMobileTab } from '../utils/activeMobileTab';

type ViewType = 'home' | 'kb' | 'admin' | 'profile' | 'global-kb-settings' | 'kb-settings' | 'terms' | 'privacy' | 'accessibility' | 'agents';
type KbViewType = 'chat' | 'dashboard' | 'research' | 'academic_research' | 'workspace' | 'mindmap';

interface UseViewStateParams {
  setView: React.Dispatch<React.SetStateAction<ViewType>>;
  kbView: KbViewType;
  setKbView: React.Dispatch<React.SetStateAction<KbViewType>>;
  setShowSettings: (val: boolean) => void;
}

export type { ViewType, KbViewType };

export function useViewState({ setView, kbView, setKbView, setShowSettings }: UseViewStateParams) {
  const [mobileTab, setMobileTab] = useState<MobileTab>('chat');

  const TAB_ORDER: MobileTab[] = useMemo(() => ['history', 'chat', 'workspace', 'files'], []);

  // 'chat' und 'workspace' rendern dieselbe ChatView und unterscheiden sich
  // nur über kbView (siehe handleMobileTabChange in KbWorkspaceLayout, das
  // dieselbe Regel für Tab-Klicks anwendet). setMobileTab wird hier von den
  // Swipe-Handlern direkt aufgerufen statt über KbWorkspaceLayout zu gehen —
  // ohne diesen Abgleich würde ein Swipe auf den Workspace-Reiter den zuletzt
  // gesetzten kbView zeigen (z.B. 'research') statt den Workspace.
  const applyTab = useCallback((tab: MobileTab) => {
    if (tab === 'workspace') setKbView('workspace');
    if (tab === 'chat') setKbView('chat');
    setMobileTab(tab);
  }, [setKbView]);

  // The swipe's starting point is the *displayed* tab (same derivation
  // KbWorkspaceLayout uses to decide what to render), not the raw
  // `mobileTab` state — those two can drift, e.g. ChatView's own Workspace
  // tab (icon-only on mobile) calls setKbView('workspace') directly without
  // going through applyTab/setMobileTab. Indexing TAB_ORDER by the stale
  // `mobileTab` in that state left a left-swipe dead and a right-swipe
  // skipping Chat.
  const activeTab = deriveActiveMobileTab(mobileTab, kbView);

  const swipeLeft = useCallback(() => {
    const i = TAB_ORDER.indexOf(activeTab);
    const next = i < TAB_ORDER.length - 1 ? TAB_ORDER[i + 1] : activeTab;
    if (next !== activeTab) applyTab(next);
  }, [TAB_ORDER, activeTab, applyTab]);
  const swipeRight = useCallback(() => {
    const i = TAB_ORDER.indexOf(activeTab);
    const next = i > 0 ? TAB_ORDER[i - 1] : activeTab;
    if (next !== activeTab) applyTab(next);
  }, [TAB_ORDER, activeTab, applyTab]);
  const swipeHandlers = useSwipeGesture(swipeLeft, swipeRight);

  const handleGoHome = useCallback(() => {
    setShowSettings(false);
    setKbView('chat');
    setView('home');
  }, [setShowSettings, setKbView, setView]);

  const handleViewHome = useCallback(() => setView('home'), [setView]);

  return {
    mobileTab, setMobileTab,
    swipeHandlers,
    handleGoHome,
    handleViewHome,
  };
}
