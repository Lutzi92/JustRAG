import { useState, useCallback } from 'react';
import axios from 'axios';
import type { KnowledgeBase, GeneratedContent } from '../types';
import { API_BASE_URL } from '../api';
import { useTheme } from '../contexts/ThemeContext';
import { useModalContext } from '../contexts/ModalContext';
import { useToast } from '../contexts/ToastContext';

interface UseKnowledgeBasesParams {
  onKBSelected: () => void;
  handleGoHome: () => void;
  setCurrentKb: (kb: KnowledgeBase | null) => void;
  setIsPro: (isPro: boolean) => void;
  setKbView: (view: 'chat' | 'dashboard' | 'research' | 'studio') => void;
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
  const [kbs, setKbs] = useState<KnowledgeBase[]>([]);
  const [globalKbs, setGlobalKbs] = useState<KnowledgeBase[]>([]);

  const fetchKBs = useCallback(async () => {
    try {
      const [kbRes, globalRes] = await Promise.all([
        axios.get(`${API_BASE_URL}/api/kb`),
        axios.get(`${API_BASE_URL}/api/kb/global`),
      ]);
      setKbs(Array.isArray(kbRes.data) ? kbRes.data : (kbRes.data.items || []));
      setGlobalKbs(Array.isArray(globalRes.data) ? globalRes.data : []);
    } catch (err: unknown) {
      console.error('Failed to fetch KBs:', err);
      toast.error(t('kbFetchError'));
    }
  }, []);

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

  const handleDeleteKB = useCallback(async (id: string, e: React.MouseEvent) => {
    e.stopPropagation();
    if (!await showConfirm(t('confirmDeleteKB'))) return;
    let snapshot: KnowledgeBase[] = [];
    setKbs(prev => { snapshot = prev; return prev.filter(kb => kb.id !== id); });
    handleGoHome();
    try {
      await axios.delete(`${API_BASE_URL}/api/kb/${id}`);
    } catch {
      setKbs(snapshot);
      toast.error(t('deleteKBError'));
    }
  }, [showConfirm, t, handleGoHome]);

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
  }, [showConfirm, t, handleGoHome]);

  const handleOpenGlobalKbSettings = (kb: KnowledgeBase, e: React.MouseEvent) => {
    e.stopPropagation();
    setCurrentKb(kb);
    setView('global-kb-settings');
  };

  return {
    kbs, setKbs, globalKbs, setGlobalKbs,
    fetchKBs, handleCreateKB, handleSelectKB, handleDeleteKB,
    handleCreateGlobalKB, handleDeleteGlobalKB, handleOpenGlobalKbSettings,
  };
}
