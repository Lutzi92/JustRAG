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

  const TAB_ORDER: MobileTab[] = useMemo(() => ['files', 'chat', 'studio'], []);
  const swipeLeft = useCallback(() => {
    setMobileTab(prev => {
      const i = TAB_ORDER.indexOf(prev);
      return i < TAB_ORDER.length - 1 ? TAB_ORDER[i + 1] : prev;
    });
  }, [TAB_ORDER]);
  const swipeRight = useCallback(() => {
    setMobileTab(prev => {
      const i = TAB_ORDER.indexOf(prev);
      return i > 0 ? TAB_ORDER[i - 1] : prev;
    });
  }, [TAB_ORDER]);
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
