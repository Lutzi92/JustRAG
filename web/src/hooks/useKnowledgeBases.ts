import { useState, useCallback, useEffect } from 'react';
import axios from 'axios';
import type { KnowledgeBase, GeneratedContent } from '../types';
import { API_BASE_URL } from '../api';
import { useTheme } from '../contexts/ThemeContext';
import { useModalContext } from '../contexts/ModalContext';
import { useToast } from '../contexts/ToastContext';
import { useKbRemoval } from './useKbRemoval';

interface UseKnowledgeBasesParams {
  onKBSelected: () => void;
  handleGoHome: () => void;
  setCurrentKb: (kb: KnowledgeBase | null) => void;
  setIsPro: (isPro: boolean) => void;
  setKbView: (view: 'chat' | 'dashboard' | 'research' | 'studio' | 'mindmap') => void;
  setView: (view: 'home' | 'kb' | 'admin' | 'profile' | 'global-kb-settings' | 'privacy' | 'accessibility') => void;
  setSelectedContent: (content: GeneratedContent | null) => void;
  setGeneratedContent: (content: GeneratedContent[]) => void;
}

export function useKnowledgeBases({
  onKBSelected, handleGoHome,
  setCurrentKb, setIsPro, setKbView, setView, setSelectedContent, setGeneratedContent,
}: UseKnowledgeBasesParams) {
  const { t } = useTheme();
  const { showConfirm, showPrompt } = useModalContext();
  const toast = useToast();
  const { removeKb, removing: removingKb } = useKbRemoval();
  const [kbs, setKbs] = useState<KnowledgeBase[]>([]);
  const [globalKbs, setGlobalKbs] = useState<KnowledgeBase[]>([]);

  const fetchKBs = useCallback(async (opts?: { silent?: boolean }) => {
    try {
      const [kbRes, globalRes] = await Promise.all([
        axios.get(`${API_BASE_URL}/api/kb`),
        axios.get(`${API_BASE_URL}/api/kb/global`),
      ]);
      setKbs(Array.isArray(kbRes.data) ? kbRes.data : (kbRes.data.items || []));
      setGlobalKbs(Array.isArray(globalRes.data) ? globalRes.data : []);
    } catch (err: unknown) {
      console.error('Failed to fetch KBs:', err);
      // Suppress the error toast for background polling so a transient network
      // blip while files are processing doesn't spam the user.
      if (!opts?.silent) toast.error(t('kbFetchError'));
    }
  }, [t, toast]);

  // Live processing count: while any KB still has files being ingested
  // (status pending/processing), poll the KB lists so the per-card
  // "processing" chip updates without a manual refresh. Polling stops on its
  // own once nothing is processing, so there is no idle traffic.
  const anyProcessing = [...kbs, ...globalKbs].some(
    (kb) => (kb.processingFileCount ?? 0) > 0,
  );
  useEffect(() => {
    if (!anyProcessing) return;
    const id = setInterval(() => { void fetchKBs({ silent: true }); }, 4000);
    return () => clearInterval(id);
  }, [anyProcessing, fetchKBs]);

  const handleSelectKB = useCallback((kb: KnowledgeBase) => {
    setCurrentKb(kb);
    setIsPro(kb.isPro);
    setKbView('chat');
    setView('kb');
    setSelectedContent(null);
    setGeneratedContent([]);
    onKBSelected();
  }, [onKBSelected, setCurrentKb, setIsPro, setKbView, setView, setSelectedContent, setGeneratedContent]);

  const handleCreateKB = async () => {
    const name = await showPrompt(t('enterKBName'));
    if (!name) return;
    try {
      const res = await axios.post(`${API_BASE_URL}/api/kb`, { name });
      setKbs(prev => [res.data, ...prev]);
      handleSelectKB(res.data);
    } catch (err: unknown) {
      console.error('Failed to create KB:', err);
      toast.error(t('kbCreateError'));
    }
  };

  // Thin caller: removeKb (useKbRemoval) owns the delete-vs-leave decision,
  // the confirmation dialog(s), and the request. This only reacts to the
  // outcome — updating the local list and navigating home on success,
  // rolling nothing back (nothing was mutated yet) and toasting on failure.
  const handleDeleteKB = useCallback(async (kb: KnowledgeBase, e: React.MouseEvent) => {
    e.stopPropagation();
    let outcome: Awaited<ReturnType<typeof removeKb>>;
    try {
      outcome = await removeKb(kb);
    } catch (err: unknown) {
      console.error('Failed to remove KB:', err);
      toast.error(t('deleteKBError'));
      return;
    }
    if (outcome === 'cancelled') return;
    // A deletion/leave/unsubscribe can affect either list — a subscribed
    // public KB lives in globalKbs, not kbs — so refetch both instead of
    // guessing which array to splice; same reload path KbCatalogModal's
    // onSubscriptionChange already uses (Task 9).
    await fetchKBs();
    handleGoHome();
  }, [removeKb, handleGoHome, toast, t, fetchKBs]);

  const handleCreateGlobalKB = async () => {
    const name = await showPrompt(t('globalKbNamePrompt'));
    if (!name) return;
    try {
      const res = await axios.post(`${API_BASE_URL}/api/admin/global-kbs`, { name });
      setGlobalKbs(prev => [res.data, ...prev]);
    } catch (err: unknown) {
      console.error('Failed to create global KB:', err);
      toast.error(t('kbCreateError'));
    }
  };

  const handleDeleteGlobalKB = useCallback(async (id: string, e: React.MouseEvent) => {
    e.stopPropagation();
    if (!await showConfirm(t('confirmDeleteGlobalKb'))) return;
    let snapshot: KnowledgeBase[] = [];
    setGlobalKbs(prev => { snapshot = prev; return prev.filter(kb => kb.id !== id); });
    handleGoHome();
    try {
      await axios.delete(`${API_BASE_URL}/api/admin/global-kbs/${id}`);
    } catch {
      setGlobalKbs(snapshot);
      toast.error(t('deleteKBError'));
    }
  }, [showConfirm, t, handleGoHome, toast]);

  const handleOpenGlobalKbSettings = (kb: KnowledgeBase, e: React.MouseEvent) => {
    e.stopPropagation();
    setCurrentKb(kb);
    setView('global-kb-settings');
  };

  return {
    kbs, setKbs, globalKbs, setGlobalKbs,
    fetchKBs, handleCreateKB, handleSelectKB, handleDeleteKB, removingKb,
    handleCreateGlobalKB, handleDeleteGlobalKB, handleOpenGlobalKbSettings,
  };
}
