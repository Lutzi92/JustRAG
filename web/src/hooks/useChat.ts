import { useState, useEffect, useRef, useMemo, useCallback } from 'react';
import axios from 'axios';
import type { Message, MessageSource, MessageVerification, StructuredTable, KnowledgeBase, FileEntry, ChatEntry } from '../types';
import { API_BASE_URL } from '../api';
import { useTheme } from '../contexts/ThemeContext';

import { useModalContext } from '../contexts/ModalContext';
import { useToast } from '../contexts/ToastContext';
import { buildMessageMap, findDefaultLeaf } from '../utils/messageTree';
import { useMessageTree } from './useMessageTree';
import { useChatStream } from './useChatStream';
import { useChatAttachment, type ChatAttachment } from './useChatAttachment';
import type { AgentSelection } from './useKbSettings';

// Kept local (not imported from the Workspace comparison dialog): this hook
// sits below the dialog layer and must not import its union type (see
// `startComparison`'s `modes: string[]` parameter below).
type ComparisonMode = 'contradiction' | 'formal' | 'completeness';

/**
 * Pure helper for `startComparison`: turns an uploaded attachment + chosen
 * modes + free-text instruction into the (message, send-options) pair
 * `useChatStream.handleSendMessage` expects. Extracted so it is testable
 * without a `useChat` render harness (none exists yet for this hook).
 */
export function buildComparisonSend(
  attachment: ChatAttachment,
  modes: string[],
  instruction: string,
  fallbackMessage: string,
): { message: string; opts: { attachmentId: string; comparisonModes: string[] } } {
  return {
    message: instruction.trim() || fallbackMessage,
    opts: { attachmentId: attachment.attachmentId, comparisonModes: modes },
  };
}

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
  structured_table?: StructuredTable | null;
  teamId?: string | null;
  agentId?: string | null;
}

interface UseChatParams {
  currentKb: KnowledgeBase | null;
  files: FileEntry[];
  enhance: 'rewrite' | 'expand' | 'spell' | null;
  reasoningEnabled: boolean;
  reasoningLevel: 'low' | 'medium' | 'high';
  agentSelection: AgentSelection;
  setAgentSelection: (val: AgentSelection) => void;
  onResearchLoaded?: () => void;
  onAcademicResearchLoaded?: () => void;
}

export function useChat({
  currentKb, files, enhance,
  reasoningEnabled, reasoningLevel, agentSelection, setAgentSelection,
  onResearchLoaded, onAcademicResearchLoaded,
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

  // In-chat document comparison: attachment lifecycle + selected modes.
  // The attachment persists across the chat session (so follow-up turns keep
  // sending its id), while selectedModes are cleared after a comparison send
  // so a follow-up is a normal question rather than a re-comparison.
  const attachmentState = useChatAttachment(currentKb?.id ?? '', t);
  const [selectedModes, setSelectedModes] = useState<ComparisonMode[]>([]);

  // Chat history state
  const [chats, setChats] = useState<ChatEntry[]>([]);
  const [activeChatId, setActiveChatIdState] = useState<string | null>(null);
  const activeChatIdRef = useRef<string | null>(null);
  const setActiveChatId = useCallback((id: string | null) => {
    activeChatIdRef.current = id;
    setActiveChatIdState(id);
  }, []);

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
    reasoningEnabled, reasoningLevel, agentSelection, language,
    setMessageTree, messageTreeRef, setActiveLeafId,
    activeChatIdRef, activeChatId, setActiveChatId, setChats,
    fetchChats, fetchChatsTimerRef,
    t,
  });
  const { loading, chatAbortRef } = stream;

  // Cleanup timers and abort in-flight SSE on unmount. The ref *objects* are
  // captured locally so the cleanup reads their latest .current at unmount
  // (the timers/abort controller are assigned after mount).
  useEffect(() => {
    const fetchChatsTimer = fetchChatsTimerRef;
    const forkFocusTimer = forkFocusTimerRef;
    const chatAbort = chatAbortRef;
    return () => {
      clearTimeout(fetchChatsTimer.current);
      clearTimeout(forkFocusTimer.current);
      chatAbort.current?.abort();
    };
  }, [chatAbortRef]);

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
    setAgentSelection({ teamId: chat.teamId ?? undefined, agentId: chat.agentId ?? undefined });
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
        structured_table: m.structured_table ?? null,
        teamId: m.teamId ?? null,
        agentId: m.agentId ?? null,
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
  }, [onResearchLoaded, onAcademicResearchLoaded, setActiveChatId, setAgentSelection, setMessageTree, setActiveLeafId, setComparisonMode, setComparisonLeafId, toast, t]);

  // Clears the comparison attachment + selected modes (e.g. on new chat or the
  // user pressing the chip's remove button).
  const clearComparison = useCallback(() => {
    attachmentState.clear();
    setSelectedModes([]);
  }, [attachmentState]);

  const handleNewChat = useCallback(() => {
    setActiveChatId(null);
    // Clear the sticky selection so a fresh chat doesn't inherit the
    // previous chat's team/agent; ChatView's default-selection effect
    // re-applies the KB's isDefault team/agent once options are loaded.
    setAgentSelection({});
    setMessageTree(new Map());
    setActiveLeafId(null);
    setComparisonMode(false);
    setComparisonLeafId(null);
    setEditingMessageId(null);
    setForkPointId(null);
    setLoadedResearchSession(null);
    setLoadedAcademicSession(null);
    clearComparison();
  }, [setActiveChatId, setAgentSelection, setMessageTree, setActiveLeafId, setComparisonMode, setComparisonLeafId, clearComparison]);

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
  }, [handleNewChat, showConfirm, t, toast]);

  // Orchestrating send: clears input, resets UI state, delegates to stream hook
  const handleSendMessage = useCallback(async (e: React.FormEvent | React.KeyboardEvent, editParentId?: string) => {
    e.preventDefault();
    setIsAtBottom(true);
    if (!currentKb || loading || chatSwitchingRef.current) return;

    // A comparison send (attachment + ≥1 mode) is allowed with an empty input —
    // we substitute a localized default message because the backend rejects an
    // empty message with 400 before running the comparison. Don't start a
    // comparison while the attachment is still uploading.
    const isComparison = !!attachmentState.attachment && selectedModes.length > 0;
    if (isComparison && attachmentState.uploading) return;
    if (!userMessageInput.trim() && !isComparison) return;

    // Guard: require at least one selected file before clearing input
    const selectedFiles = files.filter(f => f.selected !== false);
    if (selectedFiles.length === 0) return;

    const userMessage = userMessageInput.trim() || (isComparison ? t('comparisonDefaultMessage') : '');
    if (!userMessage) return;

    const sendOpts = attachmentState.attachment
      ? { attachmentId: attachmentState.attachment.attachmentId, comparisonModes: selectedModes }
      : undefined;

    setUserMessageInput('');
    setEditingMessageId(null);
    setForkPointId(null);
    // Clear modes after a comparison send so a follow-up turn is a normal
    // question; keep the attachment until the user removes it.
    if (selectedModes.length > 0) setSelectedModes([]);
    if (textareaRef.current) {
      textareaRef.current.style.height = 'auto';
    }

    // Delegate actual streaming to useChatStream
    await stream.handleSendMessage(e, userMessage, activeLeafId, editParentId, sendOpts);
  }, [userMessageInput, currentKb, loading, files, activeLeafId, stream, attachmentState.attachment, attachmentState.uploading, selectedModes, t]);

  /**
   * Starts a document comparison from the Workspace tile.
   *
   * The comparison stays a real chat turn — only its entry point moved 2026-08
   * from the composer into a Workspace tile. That keeps follow-up questions
   * about the uploaded document working unchanged in the resulting chat
   * (backend: buildFollowUpContext).
   */
  const startComparison = useCallback(async (input: {
    file: File;
    modes: string[];
    instruction: string;
    agentSelection: AgentSelection;
  }) => {
    if (!currentKb) return;
    const uploaded = await attachmentState.upload(input.file);
    if (!uploaded) return; // upload() already reported the failure via attachmentState.error

    handleNewChat();
    // Apply the dialog's agent/team choice before sending — handleNewChat()
    // just reset the selection to {}, and useChatStream sends teamId/agentId
    // from this state, not from the send options.
    setAgentSelection(input.agentSelection);

    const { message, opts } = buildComparisonSend(uploaded, input.modes, input.instruction, t('comparisonDefaultMessage'));
    await stream.handleSendMessage(
      { preventDefault: () => {} } as React.FormEvent,
      message,
      null,
      undefined,
      opts,
    );
  }, [currentKb, attachmentState, handleNewChat, setAgentSelection, stream, t]);

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
  }, [messageTreeRef]);

  const handleForkFromMessage = useCallback((messageId: string) => {
    setActiveLeafId(messageId);
    setForkPointId(messageId);
    forkFocusTimerRef.current = setTimeout(() => {
      textareaRef.current?.focus();
      textareaRef.current?.scrollIntoView({ behavior: 'smooth', block: 'center' });
    }, 50);
  }, [setActiveLeafId]);

  const handleRegenerate = useCallback((aiMessageId: string) => {
    const aiMsg = messageTreeRef.current.get(aiMessageId);
    if (!aiMsg || aiMsg.role !== 'ai' || !aiMsg.parentMessageId) return;

    const userMsg = messageTreeRef.current.get(aiMsg.parentMessageId);
    if (!userMsg || userMsg.role !== 'user') return;

    const parentOfUser = userMsg.parentMessageId;
    setUserMessageInput(userMsg.content);
    pendingEditRef.current = parentOfUser || null;
  }, [messageTreeRef]);

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
  }, [currentKb, setMessageTree, t, toast]);

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
    // Document comparison
    attachment: attachmentState.attachment,
    attachmentUploading: attachmentState.uploading,
    attachmentError: attachmentState.error,
    uploadAttachment: attachmentState.upload,
    selectedComparisonModes: selectedModes,
    setSelectedComparisonModes: setSelectedModes,
    clearComparison,
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
    startComparison,
    handleRegenerate,
    handleFollowUpClick,
    handleFeedback,
    fetchChats,
  }), [
    messageTree,
    setMessageTree,
    activeLeafId,
    setActiveLeafId,
    messages,
    setComparisonMode,
    setComparisonLeafId,
    setActiveChatId,
    comparisonMode,
    comparisonLeafId,
    editingMessageId,
    forkPointId,
    userMessageInput,
    loading,
    attachmentState,
    selectedModes,
    clearComparison,
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
    startComparison,
    handleRegenerate,
    handleFollowUpClick,
    handleFeedback,
    fetchChats,
  ]);
}
