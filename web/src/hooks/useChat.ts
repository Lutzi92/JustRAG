import { useState, useEffect, useRef, useMemo, useCallback } from 'react';
import axios from 'axios';
import type { Message, MessageSource, MessageVerification, KnowledgeBase, FileEntry, ChatEntry } from '../types';
import { API_BASE_URL } from '../api';
import { useTheme } from '../contexts/ThemeContext';

import { useModalContext } from '../contexts/ModalContext';
import { useToast } from '../contexts/ToastContext';
import { buildMessageMap, findDefaultLeaf } from '../utils/messageTree';
import { useMessageTree } from './useMessageTree';
import { useChatStream } from './useChatStream';

interface RawChatMessage {
  id: string;
  parentMessageId?: string | null;
  role: 'user' | 'ai';
  content: string;
  sources?: string | MessageSource[];
  isEnhanced?: boolean;
  reasoning?: string;
  feedback?: 'positive' | 'negative' | null;
  verification?: MessageVerification | null;
  traceId?: string | null;
}

interface UseChatParams {
  currentKb: KnowledgeBase | null;
  files: FileEntry[];
  enhance: 'rewrite' | 'expand' | 'spell' | null;
  reasoningEnabled: boolean;
  reasoningLevel: 'low' | 'medium' | 'high';
  onResearchLoaded?: () => void;
  onAcademicResearchLoaded?: () => void;
}

export function useChat({
  currentKb, files, enhance,
  reasoningEnabled, reasoningLevel, onResearchLoaded, onAcademicResearchLoaded,
}: UseChatParams) {
  const { language, t } = useTheme();

  const { showConfirm } = useModalContext();
  const toast = useToast();

  // Compose: message tree state and navigation
  const tree = useMessageTree();
  const {
    messageTree, setMessageTree, messageTreeRef,
    activeLeafId, setActiveLeafId, messages,
    comparisonMode, setComparisonMode,
    comparisonLeafId, setComparisonLeafId,
    handleSwitchBranch, handleStartComparison,
  } = tree;

  // Editing state
  const [editingMessageId, setEditingMessageId] = useState<string | null>(null);

  // Fork indicator
  const [forkPointId, setForkPointId] = useState<string | null>(null);

  // Input state
  const [userMessageInput, setUserMessageInput] = useState('');

  // Chat history state
  const [chats, setChats] = useState<ChatEntry[]>([]);
  const [activeChatId, setActiveChatIdState] = useState<string | null>(null);
  const activeChatIdRef = useRef<string | null>(null);
  const setActiveChatId = (id: string | null) => {
    activeChatIdRef.current = id;
    setActiveChatIdState(id);
  };

  // Research session loaded from history
  const [loadedResearchSession, setLoadedResearchSession] = useState<{
    id: string; goal: string; report: string; findings: { content: string; sources: { fileName: string; fileId: string; pages?: number[] }[]; relevanceScore: number }[];
  } | null>(null);

  // Academic research session loaded from history
  const [loadedAcademicSession, setLoadedAcademicSession] = useState<{
    id: string; goal: string; report: string;
    findings: { content: string; papers: { id: string; title: string; authors: string[]; year: number; url: string; harvardCitation: string }[]; relevanceScore: number }[];
    papers?: { id: string; title: string; authors: string[]; year: number; journal?: string; volume?: string; issue?: string; pages?: string; abstract?: string; doi?: string; url: string; pdfUrl?: string; citationCount?: number; harvardCitation: string }[];
  } | null>(null);

  const fetchChats = useCallback(async (kbId: string, signal?: AbortSignal) => {
    try {
      const res = await axios.get(`${API_BASE_URL}/api/kb/${kbId}/chats`, { signal });
      setChats(res.data);
    } catch (err: unknown) {
      if (axios.isCancel(err)) return;
      console.error('Failed to fetch chats:', err);
      toast.error(t('chatListLoadError'));
    }
  }, [toast, t]);

  // Refs
  const chatEndRef = useRef<HTMLDivElement>(null);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const messagesContainerRef = useRef<HTMLDivElement>(null);
  const [isAtBottom, setIsAtBottom] = useState(true);
  const pendingEditRef = useRef<string | null | undefined>(undefined);
  const pendingFollowUpRef = useRef<string | null>(null);
  const fetchChatsTimerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const forkFocusTimerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const chatSwitchingRef = useRef(false);

  // Compose: SSE streaming
  const stream = useChatStream({
    currentKb, files, enhance,
    reasoningEnabled, reasoningLevel, language,
    setMessageTree, setActiveLeafId,
    activeChatIdRef, activeChatId, setActiveChatId, setChats,
    fetchChats, fetchChatsTimerRef,
    t,
  });
  const { loading, chatAbortRef } = stream;

  // Cleanup timers and abort in-flight SSE on unmount
  useEffect(() => {
    return () => {
      clearTimeout(fetchChatsTimerRef.current);
      clearTimeout(forkFocusTimerRef.current);
      chatAbortRef.current?.abort();
    };
  }, []);

  // Auto-scroll to bottom
  useEffect(() => {
    if (isAtBottom) {
      chatEndRef.current?.scrollIntoView({ behavior: 'smooth' });
    }
  }, [messages, isAtBottom]);

  const handleScroll = useCallback(() => {
    if (messagesContainerRef.current) {
      const { scrollTop, scrollHeight, clientHeight } = messagesContainerRef.current;
      const atBottom = scrollHeight - scrollTop - clientHeight < 100;
      setIsAtBottom(atBottom);
    }
  }, []);

  // Auto-resize textarea
  useEffect(() => {
    if (textareaRef.current) {
      textareaRef.current.style.height = 'auto';
      textareaRef.current.style.height = `${textareaRef.current.scrollHeight}px`;
    }
  }, [userMessageInput]);

  const handleSelectChat = useCallback(async (chat: ChatEntry) => {
    if (chat.type === 'research') {
      try {
        const res = await axios.get(`${API_BASE_URL}/api/chats/${chat.id}/messages`);
        const msgs = Array.isArray(res.data) ? res.data : [];
        const userMsg = msgs.find((m: RawChatMessage) => m.role === 'user');
        const aiMsg = msgs.find((m: RawChatMessage) => m.role === 'ai');
        const goal = userMsg?.content || '';
        const report = aiMsg?.content || '';
        type ResearchFinding = { content: string; sources: { fileName: string; fileId: string; pages?: number[] }[]; relevanceScore: number };
        let findings: ResearchFinding[] = [];
        try {
          findings = aiMsg?.sources ? (typeof aiMsg.sources === 'string' ? JSON.parse(aiMsg.sources) : aiMsg.sources) as ResearchFinding[] : [];
        } catch { findings = []; }
        setLoadedResearchSession({ id: chat.id, goal, report, findings });
        onResearchLoaded?.();
      } catch (err: unknown) {
        console.error('Failed to load research session:', err);
        toast.error(t('researchLoadError'));
      }
      return;
    }

    if (chat.type === 'academic_research') {
      try {
        const res = await axios.get(`${API_BASE_URL}/api/chats/${chat.id}/messages`);
        const msgs = Array.isArray(res.data) ? res.data : [];
        const userMsg = msgs.find((m: RawChatMessage) => m.role === 'user');
        const aiMsg = msgs.find((m: RawChatMessage) => m.role === 'ai');
        const goal = userMsg?.content || '';
        const report = aiMsg?.content || '';
        type SavedFinding = { content: string; sources: { fileName: string; fileId: string; url?: string }[]; relevanceScore: number };
        type AcademicPaperSaved = { id: string; title: string; authors: string[]; year: number; journal?: string; volume?: string; issue?: string; pages?: string; abstract?: string; doi?: string; url: string; pdfUrl?: string; citationCount?: number; harvardCitation: string };
        let savedFindings: SavedFinding[] = [];
        let savedPapers: AcademicPaperSaved[] = [];
        try {
          savedFindings = aiMsg?.sources ? (typeof aiMsg.sources === 'string' ? JSON.parse(aiMsg.sources) : aiMsg.sources) as SavedFinding[] : [];
        } catch { savedFindings = []; }
        // Papers are stored in the reasoning column as JSON
        try {
          savedPapers = aiMsg?.reasoning ? JSON.parse(aiMsg.reasoning) as AcademicPaperSaved[] : [];
        } catch { savedPapers = []; }
        // Convert saved Finding[] format back to AcademicFinding[] format
        const findings = savedFindings.map(f => ({
          content: f.content,
          relevanceScore: f.relevanceScore,
          papers: (f.sources || []).map(s => ({
            id: s.fileId,
            title: s.fileName,
            authors: [] as string[],
            year: 0,
            url: s.url || '',
            harvardCitation: s.fileName,
          })),
        }));
        setLoadedAcademicSession({ id: chat.id, goal, report, findings, papers: savedPapers });
        onAcademicResearchLoaded?.();
      } catch (err: unknown) {
        console.error('Failed to load academic research session:', err);
        toast.error(t('researchLoadError'));
      }
      return;
    }

    setActiveChatId(chat.id);
    chatSwitchingRef.current = true;
    try {
      const res = await axios.get(`${API_BASE_URL}/api/chats/${chat.id}/messages`);
      const rawMessages: Message[] = (res.data as RawChatMessage[]).map((m) => ({
        id: m.id,
        parentMessageId: m.parentMessageId || undefined,
        role: m.role,
        content: m.content,
        sources: typeof m.sources === 'string' ? (() => { try { return JSON.parse(m.sources as string); } catch { return []; } })() : m.sources,
        isEnhanced: m.isEnhanced,
        reasoning: m.reasoning,
        feedback: m.feedback,
        verification: m.verification ?? null,
        traceId: m.traceId ?? null,
      }));
      const newTree = buildMessageMap(rawMessages);
      setMessageTree(newTree);
      setActiveLeafId(findDefaultLeaf(newTree));
      setComparisonMode(false);
      setComparisonLeafId(null);
    } catch (err: unknown) {
      console.error('Failed to load chat messages:', err);
      toast.error(t('chatLoadError'));
    } finally {
      chatSwitchingRef.current = false;
    }
  }, [onResearchLoaded, onAcademicResearchLoaded, setMessageTree, setActiveLeafId, setComparisonMode, setComparisonLeafId, toast, t]);

  const handleNewChat = useCallback(() => {
    setActiveChatId(null);
    setMessageTree(new Map());
    setActiveLeafId(null);
    setComparisonMode(false);
    setComparisonLeafId(null);
    setEditingMessageId(null);
    setForkPointId(null);
    setLoadedResearchSession(null);
    setLoadedAcademicSession(null);
  }, []);

  const handleDeleteChat = useCallback(async (id: string, e: React.MouseEvent) => {
    e.stopPropagation();
    if (!await showConfirm(t('confirmDeleteChat'))) return;
    let snapshot: ChatEntry[] = [];
    setChats(prev => { snapshot = prev; return prev.filter(c => c.id !== id); });
    if (activeChatIdRef.current === id) {
      handleNewChat();
    }
    try {
      await axios.delete(`${API_BASE_URL}/api/chats/${id}`);
    } catch {
      setChats(snapshot);
      toast.error(t('deleteChatError'));
    }
  }, [handleNewChat, showConfirm, t]);

  // Orchestrating send: clears input, resets UI state, delegates to stream hook
  const handleSendMessage = useCallback(async (e: React.FormEvent | React.KeyboardEvent, editParentId?: string) => {
    e.preventDefault();
    setIsAtBottom(true);
    if (!userMessageInput.trim() || !currentKb || loading || chatSwitchingRef.current) return;

    // Guard: require at least one selected file before clearing input
    const selectedFiles = files.filter(f => f.selected !== false);
    if (selectedFiles.length === 0) return;

    const userMessage = userMessageInput.trim();
    setUserMessageInput('');
    setEditingMessageId(null);
    setForkPointId(null);
    if (textareaRef.current) {
      textareaRef.current.style.height = 'auto';
    }

    // Delegate actual streaming to useChatStream
    await stream.handleSendMessage(e, userMessage, activeLeafId, editParentId);
  }, [userMessageInput, currentKb, loading, files, activeLeafId, stream]);

  const handleStartEdit = useCallback((messageId: string) => {
    setEditingMessageId(messageId);
  }, []);

  const handleEditSubmit = useCallback((messageId: string, newContent: string) => {
    const msg = messageTreeRef.current.get(messageId);
    if (!msg) return;
    const parentId = msg.parentMessageId;
    setEditingMessageId(null);
    setUserMessageInput(newContent);
    pendingEditRef.current = parentId || null;
  }, []);

  const handleForkFromMessage = useCallback((messageId: string) => {
    setActiveLeafId(messageId);
    setForkPointId(messageId);
    forkFocusTimerRef.current = setTimeout(() => {
      textareaRef.current?.focus();
      textareaRef.current?.scrollIntoView({ behavior: 'smooth', block: 'center' });
    }, 50);
  }, []);

  const handleRegenerate = useCallback((aiMessageId: string) => {
    const aiMsg = messageTreeRef.current.get(aiMessageId);
    if (!aiMsg || aiMsg.role !== 'ai' || !aiMsg.parentMessageId) return;

    const userMsg = messageTreeRef.current.get(aiMsg.parentMessageId);
    if (!userMsg || userMsg.role !== 'user') return;

    const parentOfUser = userMsg.parentMessageId;
    setUserMessageInput(userMsg.content);
    pendingEditRef.current = parentOfUser || null;
  }, []);

  const handleFollowUpClick = useCallback((question: string) => {
    pendingFollowUpRef.current = question;
    setUserMessageInput(question);
  }, []);

  const handleFeedback = useCallback(async (messageId: string, feedback: 'positive' | 'negative' | null, comment?: string) => {
    if (!currentKb || !activeChatIdRef.current) return;
    try {
      await axios.post(`${API_BASE_URL}/api/kb/${currentKb.id}/chats/${activeChatIdRef.current}/messages/${messageId}/feedback`,
        { feedback, ...(comment !== undefined ? { comment } : {}) }
      );
      // Update local message tree
      setMessageTree(prev => {
        const updated = new Map(prev);
        const msg = updated.get(messageId);
        if (msg) {
          updated.set(messageId, { ...msg, feedback, feedbackComment: comment });
        }
        return updated;
      });
    } catch (error: unknown) {
      console.error('Failed to submit feedback', error);
      toast.error(t('feedbackError'));
    }
  }, [currentKb]);

  // Auto-trigger for pending edits and follow-ups
  useEffect(() => {
    if (pendingFollowUpRef.current && userMessageInput === pendingFollowUpRef.current) {
      pendingFollowUpRef.current = null;
      const syntheticEvent = { preventDefault: () => { } } as React.FormEvent;
      handleSendMessage(syntheticEvent);
    } else if (pendingEditRef.current !== undefined && userMessageInput) {
      const parentId = pendingEditRef.current;
      pendingEditRef.current = undefined;
      const syntheticEvent = { preventDefault: () => { } } as React.FormEvent;
      handleSendMessage(syntheticEvent, parentId || undefined);
    }
  }, [userMessageInput, handleSendMessage]);

  return useMemo(() => ({
    // Message tree
    messageTree,
    setMessageTree,
    activeLeafId,
    setActiveLeafId,
    messages,
    // Comparison
    comparisonMode,
    setComparisonMode,
    comparisonLeafId,
    setComparisonLeafId,
    // Editing
    editingMessageId,
    setEditingMessageId,
    // Fork
    forkPointId,
    setForkPointId,
    // Input
    userMessageInput,
    setUserMessageInput,
    loading,
    // Chat history
    chats,
    setChats,
    activeChatId,
    setActiveChatId,
    // Research
    loadedResearchSession,
    setLoadedResearchSession,
    // Academic Research
    loadedAcademicSession,
    setLoadedAcademicSession,
    // Refs
    chatEndRef,
    textareaRef,
    messagesContainerRef,
    isAtBottom,
    // Handlers
    handleScroll,
    handleSelectChat,
    handleNewChat,
    handleDeleteChat,
    handleSendMessage,
    handleSwitchBranch,
    handleStartEdit,
    handleEditSubmit,
    handleForkFromMessage,
    handleStartComparison,
    handleRegenerate,
    handleFollowUpClick,
    handleFeedback,
    fetchChats,
  }), [
    messageTree,
    activeLeafId,
    messages,
    comparisonMode,
    comparisonLeafId,
    editingMessageId,
    forkPointId,
    userMessageInput,
    loading,
    chats,
    activeChatId,
    loadedResearchSession,
    loadedAcademicSession,
    isAtBottom,
    handleScroll,
    handleSelectChat,
    handleNewChat,
    handleDeleteChat,
    handleSendMessage,
    handleSwitchBranch,
    handleStartEdit,
    handleEditSubmit,
    handleForkFromMessage,
    handleStartComparison,
    handleRegenerate,
    handleFollowUpClick,
    handleFeedback,
    fetchChats,
  ]);
}
