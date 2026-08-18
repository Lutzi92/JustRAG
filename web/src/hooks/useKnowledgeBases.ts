import { useState, useCallback, useEffect, type Dispatch, type SetStateAction } from 'react';
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
  // React state setter (functional form needed by handleRenameKB).
  setCurrentKb: Dispatch<SetStateAction<KnowledgeBase | null>>;
  setIsPro: (isPro: boolean) => void;
  setKbView: (view: 'chat' | 'dashboard' | 'research' | 'studio' | 'mindmap') => void;
  setView: (view: 'home' | 'kb' | 'admin' | 'profile' | 'global-kb-settings' | 'kb-settings' | 'privacy' | 'accessibility') => void;
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

  // Opens a KB known only by id. The discovery panel's rows are a thin
  // catalog projection (id/name/description/subscribed), while handleSelectKB
  // needs the full row — and an unsubscribed public KB is absent from
  // globalKbs by design, so it cannot be looked up locally either. Hence the
  // round trip to GET /api/kb/{id}, which is view-gated server-side.
  const handleOpenKbById = useCallback(async (id: string) => {
    try {
      const res = await axios.get(`${API_BASE_URL}/api/kb/${id}`);
      handleSelectKB(res.data);
    } catch (err: unknown) {
      console.error('Failed to open KB:', err);
      toast.error(t('kbFetchError'));
    }
  }, [handleSelectKB, toast, t]);

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

  // Rename via the same prompt dialog KB creation uses, prefilled with the
  // current name. Owner-only on private KBs, system-admin-only on public ones
  // — canRenameKb decides whether a trigger is shown, the server (kbaccess.
  // CanRename on PATCH /api/kb/{id}) enforces it. The response row replaces
  // the KB in whichever list holds it, and in currentKb only if that IS the
  // renamed KB (functional setter — a rename from the Home card of a KB that
  // is not open must not hijack the open one).
  const handleRenameKB = useCallback(async (kb: KnowledgeBase, e?: React.MouseEvent) => {
    e?.stopPropagation();
    const answer = await showPrompt(t('renameKbPrompt'), kb.name);
    const name = (answer ?? '').trim();
    if (!name || name === kb.name) return;
    try {
      const res = await axios.patch(`${API_BASE_URL}/api/kb/${kb.id}`, { name });
      const updated: KnowledgeBase = res.data;
      setKbs(prev => prev.map(k => k.id === updated.id ? updated : k));
      setGlobalKbs(prev => prev.map(k => k.id === updated.id ? updated : k));
      setCurrentKb(prev => (prev && prev.id === updated.id ? updated : prev));
    } catch (err: unknown) {
      console.error('Failed to rename KB:', err);
      toast.error(t('kbRenameError'));
    }
  }, [showPrompt, t, toast, setCurrentKb]);

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
    // Splice locally first, from whichever list actually holds it — a
    // subscribed public KB lives in globalKbs, not kbs, so both are
    // filtered. This guarantees the card disappears even if the reconciling
    // fetchKBs() below fails: fetchKBs swallows its own errors (logs +
    // toasts, never rethrows), so without this the just-deleted/left/
    // unsubscribed KB would silently stay on screen after a successful
    // request whose refetch happened to fail.
    setKbs(prev => prev.filter(k => k.id !== kb.id));
    setGlobalKbs(prev => prev.filter(k => k.id !== kb.id));
    // Navigate before the refetch, not after. If the caller was viewing the KB
    // they just removed, awaiting first would park them on a view whose KB no
    // longer exists for the whole round trip — and fetchKBs never rejects (it
    // swallows into console.error + a toast), so a hanging network would strand
    // them there indefinitely. The splice above already happened, so leaving is
    // safe immediately.
    handleGoHome();
    // Then still reconcile with the server — an unsubscribe (or auto_subscribe)
    // can change what the public list returns in ways local state can't
    // infer; same reload path KbCatalogPanel's onSubscriptionChange already
    // uses (Task 9). A failed refetch here degrades to "correct but slightly
    // stale" rather than the KB reappearing or staying stuck.
    await fetchKBs();
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

  // Per-KB RAG settings / evals / workflow (KbSettingsPanel). Same shape as
  // handleOpenGlobalKbSettings above — the panel's endpoints are all
  // kbAdminChain, so the caller side gates on myRole admin|owner.
  const handleOpenKbSettings = (kb: KnowledgeBase, e: React.MouseEvent) => {
    e.stopPropagation();
    setCurrentKb(kb);
    setView('kb-settings');
  };

  return {
    kbs, setKbs, globalKbs, setGlobalKbs,
    fetchKBs, handleCreateKB, handleSelectKB, handleOpenKbById, handleRenameKB, handleDeleteKB, removingKb,
    handleCreateGlobalKB, handleDeleteGlobalKB, handleOpenGlobalKbSettings, handleOpenKbSettings,
  };
}
