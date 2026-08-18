import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { HistoryPanel } from './HistoryPanel';

const setKbView = vi.fn();
const handleSelectChat = vi.fn();
const handleSelectContent = vi.fn();
const handleNewChat = vi.fn();

vi.mock('../sidebar-shell/SidebarShell', () => ({
  SidebarShell: ({ children }: { children: React.ReactNode }) => <aside>{children}</aside>,
}));
vi.mock('../../contexts/MobileContext', () => ({ useIsMobileContext: () => false }));
vi.mock('../../contexts/ThemeContext', () => ({ useTheme: () => ({ t: (k: string) => k }) }));
vi.mock('../../contexts/KbCoreContext', () => ({
  useKbCore: () => ({ currentKb: { id: 'kb1' }, setKbView, handleGoHome: vi.fn() }),
}));
vi.mock('../../contexts/KbLayoutContext', () => ({
  useKbLayout: () => ({ sidebar: { isLeftSidebarOpen: true, leftSidebarWidth: 320, setIsLeftSidebarOpen: vi.fn() } }),
}));
vi.mock('../../contexts/KbChatContext', () => ({
  useKbChat: () => ({
    chat: {
      chats: [
        { id: 'c1', title: 'Budget?', createdAt: '2026-08-10T00:00:00Z' },
        { id: 'r1', title: 'Zero-Trust', createdAt: '2026-08-14T00:00:00Z', type: 'research' },
        { id: 'a1', title: 'Paper', createdAt: '2026-08-12T00:00:00Z', type: 'academic_research' },
      ],
      activeChatId: null, handleSelectChat, handleDeleteChat: vi.fn(), handleNewChat,
    },
  }),
}));
vi.mock('../../contexts/KbDataContext', () => ({
  useKbData: () => ({
    content: {
      generatedContent: [{ id: 'g1', title: 'Analyse: Budget', createdAt: '2026-08-16T00:00:00Z', type: 'analysis', content: { text: '' } }],
      generating: false, handleDeleteGeneratedContent: vi.fn(), podcastProgress: null,
    },
    handleSelectContent,
  }),
}));

beforeEach(() => vi.clearAllMocks());

describe('HistoryPanel', () => {
  it('zeigt alle vier Arten in einer Liste, chronologisch', () => {
    render(<HistoryPanel />);
    const titles = screen.getAllByTestId('history-item-title').map(e => e.textContent);
    expect(titles).toEqual(['Analyse: Budget', 'Zero-Trust', 'Paper', 'Budget?']);
  });

  it('öffnet ein Artefakt im Workspace-Reiter', async () => {
    render(<HistoryPanel />);
    await userEvent.click(screen.getByText('Analyse: Budget'));
    expect(handleSelectContent).toHaveBeenCalledWith(expect.objectContaining({ id: 'g1' }));
    expect(setKbView).toHaveBeenCalledWith('workspace');
  });

  it('öffnet eine Recherche im Bericht-Reiter, nicht im Workspace', async () => {
    render(<HistoryPanel />);
    await userEvent.click(screen.getByText('Zero-Trust'));
    expect(handleSelectChat).toHaveBeenCalledWith(expect.objectContaining({ id: 'r1' }));
    expect(setKbView).toHaveBeenCalledWith('research');
    expect(setKbView).not.toHaveBeenCalledWith('workspace');
  });

  it('öffnet eine Academic-Session im Academic-Reiter', async () => {
    render(<HistoryPanel />);
    await userEvent.click(screen.getByText('Paper'));
    expect(setKbView).toHaveBeenCalledWith('academic_research');
  });

  it('öffnet einen Chat im Chat-Reiter', async () => {
    render(<HistoryPanel />);
    await userEvent.click(screen.getByText('Budget?'));
    expect(handleSelectChat).toHaveBeenCalledWith(expect.objectContaining({ id: 'c1' }));
    expect(setKbView).toHaveBeenCalledWith('chat');
  });

  it('legt über den Plus-Knopf einen neuen Chat an', async () => {
    render(<HistoryPanel />);
    await userEvent.click(screen.getByRole('button', { name: 'newChat' }));
    expect(handleNewChat).toHaveBeenCalled();
  });
});
