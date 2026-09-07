import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type { ReactNode } from 'react';
import { ChatView } from './ChatView';
import type { Message, KnowledgeBase } from '../types';

// Fix round 1 (review finding): `ComponentView`-level coverage isn't enough
// — the review found that `ChatView.tsx` (the MAIN chat surface, not just
// `ComparisonView`) mounts `MessageBubble` without wiring `conflictsOpen`,
// so the badge rendered but its click was a permanent no-op there. This test
// renders the real `ChatView` (not a stub) with a message carrying one
// conflict, clicks the real badge, and asserts the real details panel opens
// — the only way to catch "the prop exists but a caller forgot to pass it"
// class of bug, which a `MessageBubble`-only unit test structurally cannot.
//
// `ChatView` is context-driven (no props) and pulls in four Kb*Context
// hooks, `useKbAgents`, and `react-virtuoso`. Everything below is mocked to
// the minimum needed to reach the message list without crashing;
// `useMessageSections` (the real caller-owned open-state store) and
// `MessageBubble` itself are left REAL, since the whole point is to exercise
// the actual wiring between them.
vi.mock('../contexts/ThemeContext', () => ({
  useTheme: () => ({
    t: (key: string) => key,
    language: 'en' as const,
    theme: 'light' as const,
    toggleTheme: vi.fn(),
    setLanguage: vi.fn(),
  }),
}));
vi.mock('../contexts/AuthContext', () => ({
  useAuth: () => ({ user: { id: 'u1', role: 'user' }, siteConfigs: {} }),
}));
vi.mock('../contexts/ToastContext', () => ({
  useToast: () => ({ success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() }),
}));
vi.mock('../contexts/MobileContext', () => ({
  // isMobile=true collapses most of ChatView's desktop-only chrome (theme/
  // language toggles, BranchTreeNav, …) that this test has no reason to
  // exercise or mock further.
  useIsMobileContext: () => true,
}));
vi.mock('../hooks/useReducedMotion', () => ({
  useReducedMotion: () => false,
  getMotionProps: () => ({}),
}));
vi.mock('../hooks/useKbAgents', () => ({
  useKbAgents: () => ({ agents: [], teams: [] }),
}));

const currentKb: KnowledgeBase = {
  id: 'kb1', name: 'Handbuch', description: null, userId: 'someone-else',
  createdAt: '2026-01-01', isPro: false, isGlobal: false,
  aiConfigId: null, chatModel: null, embeddingModel: null, rerankModel: null, ttsModel: null,
};

vi.mock('../contexts/KbCoreContext', () => ({
  useKbCore: () => ({
    currentKb,
    availableConfigs: [],
    kbView: 'chat' as const,
    setKbView: vi.fn(),
    handleGoHome: vi.fn(),
    handleUpdateKBSettings: vi.fn(),
    onViewAgents: vi.fn(),
    scopedMindmapMessageId: undefined,
    onViewGraphForMessage: vi.fn(),
    onCloseMindmap: vi.fn(),
    onShowWholeKb: vi.fn(),
    kbMgmt: { handleRenameKB: vi.fn() },
  }),
}));

const conflictMessage: Message = {
  id: 'ai1',
  role: 'ai',
  content: 'Die Quellen widersprechen sich.',
  conflicts: [
    { claim: 'Beitragshöhe', sourceA: 1, sourceB: 2, kind: 'contradiction', newer: 'a', fileA: 'alt.md', fileB: 'neu.md' },
  ],
};

vi.mock('../contexts/KbChatContext', () => ({
  useKbChat: () => ({
    chat: {
      activeChatId: 'c1',
      messageTree: new Map(),
      messages: [conflictMessage],
      activeLeafId: null,
      comparisonMode: false,
      comparisonLeafId: null,
      setComparisonMode: vi.fn(),
      setComparisonLeafId: vi.fn(),
      setActiveLeafId: vi.fn(),
      editingMessageId: null,
      setEditingMessageId: vi.fn(),
      forkPointId: null,
      setForkPointId: vi.fn(),
      userMessageInput: '',
      setUserMessageInput: vi.fn(),
      loading: false,
      messagesContainerRef: { current: null },
      textareaRef: { current: null },
      handleScroll: vi.fn(),
      handleFollowUpClick: vi.fn(),
      handleSwitchBranch: vi.fn(),
      handleStartEdit: vi.fn(),
      handleEditSubmit: vi.fn(),
      handleForkFromMessage: vi.fn(),
      handleStartComparison: vi.fn(),
      handleRegenerate: vi.fn(),
      handleFeedback: vi.fn(),
      handleSendMessage: (e: { preventDefault: () => void }) => e.preventDefault(),
      fetchChats: vi.fn(),
      loadedResearchSession: null,
      setLoadedResearchSession: vi.fn(),
      loadedAcademicSession: null,
      setLoadedAcademicSession: vi.fn(),
      startComparison: vi.fn(),
    },
    enhance: null,
    setEnhance: vi.fn(),
    reasoningEnabled: false,
    setReasoningEnabled: vi.fn(),
    researchRunning: false,
    academicResearchRunning: false,
    setResearchRunning: vi.fn(),
    setAcademicResearchRunning: vi.fn(),
    agentSelection: {},
    setAgentSelection: vi.fn(),
  }),
}));

vi.mock('../contexts/KbDataContext', () => ({
  useKbData: () => ({
    fileMgmt: {
      hasFiles: true, selectedFileCount: 1, fileInputRef: { current: null },
      isDragging: false, handleDragOver: vi.fn(), handleDragEnter: vi.fn(),
      handleDragLeave: vi.fn(), handleDrop: vi.fn(), files: [],
    },
    webTools: {
      handlePreviewSource: vi.fn(), handlePdfSourceOpen: vi.fn(), setToolTab: vi.fn(),
      toolLoading: false, webResearchRunning: false,
    },
    content: { handleGenerate: vi.fn(), selectedContent: null, fetchGeneratedContent: vi.fn() },
    sharing: { handleOpenShare: vi.fn() },
    handleSelectContent: vi.fn(),
    rssLoading: false,
    confluenceLoading: false,
    confluenceSources: [],
  }),
}));

vi.mock('../contexts/KbLayoutContext', () => ({
  useKbLayout: () => ({
    sidebar: { setIsRightSidebarOpen: vi.fn(), setIsLeftSidebarOpen: vi.fn() },
  }),
}));

// Bypasses react-virtuoso's scroll/measurement machinery (which needs real
// layout jsdom doesn't provide) and just renders `itemContent` for every
// row — exactly what ChatView's `data`/`itemContent` props drive in
// production, minus the virtualization.
vi.mock('react-virtuoso', () => ({
  Virtuoso: (props: { data: unknown[]; itemContent: (index: number, item: unknown) => ReactNode }) => (
    <div data-testid="virtuoso-stub">
      {props.data.map((item, index) => (
        <div key={index}>{props.itemContent(index, item)}</div>
      ))}
    </div>
  ),
}));

describe('ChatView — conflicting-sources badge (main chat surface)', () => {
  it('opens the conflicts details panel from the real ChatView, not just ComparisonView', async () => {
    const user = userEvent.setup();
    render(<ChatView />);

    const badge = screen.getByLabelText('conflictsBadge');
    expect(badge).toHaveAttribute('aria-expanded', 'false');

    await user.click(badge);

    expect(badge).toHaveAttribute('aria-expanded', 'true');
    expect(screen.getByText('alt.md vs. neu.md')).toBeInTheDocument();
  });
});
