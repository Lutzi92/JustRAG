import { useState, useCallback, useMemo } from 'react';
import { type MobileTab } from '../components/MobileTabBar';
import { useSwipeGesture } from './useSwipeGesture';

type ViewType = 'home' | 'kb' | 'admin' | 'profile' | 'global-kb-settings' | 'kb-settings' | 'privacy' | 'accessibility' | 'agents';
type KbViewType = 'chat' | 'dashboard' | 'research' | 'academic_research' | 'workspace' | 'mindmap';

interface UseViewStateParams {
  setView: React.Dispatch<React.SetStateAction<ViewType>>;
  setKbView: React.Dispatch<React.SetStateAction<KbViewType>>;
  setShowSettings: (val: boolean) => void;
}

export type { ViewType, KbViewType };

export function useViewState({ setView, setKbView, setShowSettings }: UseViewStateParams) {
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

  const swipeLeft = useCallback(() => {
    const i = TAB_ORDER.indexOf(mobileTab);
    const next = i < TAB_ORDER.length - 1 ? TAB_ORDER[i + 1] : mobileTab;
    if (next !== mobileTab) applyTab(next);
  }, [TAB_ORDER, mobileTab, applyTab]);
  const swipeRight = useCallback(() => {
    const i = TAB_ORDER.indexOf(mobileTab);
    const next = i > 0 ? TAB_ORDER[i - 1] : mobileTab;
    if (next !== mobileTab) applyTab(next);
  }, [TAB_ORDER, mobileTab, applyTab]);
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
