import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { HistoryPanel } from './HistoryPanel';

const setKbView = vi.fn();
const handleSelectChat = vi.fn();
const handleSelectContent = vi.fn();
const handleNewChat = vi.fn();
const handleDeleteChat = vi.fn();
const handleDeleteGeneratedContent = vi.fn();

let activeChatId: string | null = null;

const shellProps: Record<string, unknown> = {};

vi.mock('../sidebar-shell/SidebarShell', () => ({
  SidebarShell: (props: Record<string, unknown>) => {
    Object.assign(shellProps, props);
    return <aside>{props.children as React.ReactNode}</aside>;
  },
}));
vi.mock('../../contexts/MobileContext', () => ({ useIsMobileContext: () => false }));
vi.mock('../../contexts/ThemeContext', () => ({ useTheme: () => ({ t: (k: string) => k }) }));
vi.mock('../../contexts/KbCoreContext', () => ({
  useKbCore: () => ({ currentKb: { id: 'kb1' }, setKbView, handleGoHome: vi.fn() }),
}));
// Links/Rechts-Werte bewusst unterschiedlich, damit ein Test, der versehentlich
// die rechten Sidebar-Felder verdrahtet, an isOpen/width erkennbar rot wird.
vi.mock('../../contexts/KbLayoutContext', () => ({
  useKbLayout: () => ({
    sidebar: {
      isLeftSidebarOpen: true, leftSidebarWidth: 320, setIsLeftSidebarOpen: vi.fn(),
      isRightSidebarOpen: false, rightSidebarWidth: 500,
    },
  }),
}));
vi.mock('../../contexts/KbChatContext', () => ({
  useKbChat: () => ({
    chat: {
      chats: [
        { id: 'c1', title: 'Budget?', createdAt: '2026-08-10T00:00:00Z' },
        { id: 'r1', title: 'Zero-Trust', createdAt: '2026-08-14T00:00:00Z', type: 'research' },
        { id: 'a1', title: 'Paper', createdAt: '2026-08-12T00:00:00Z', type: 'academic_research' },
      ],
      activeChatId, handleSelectChat, handleDeleteChat, handleNewChat,
    },
  }),
}));
vi.mock('../../contexts/KbDataContext', () => ({
  useKbData: () => ({
    content: {
      generatedContent: [{ id: 'g1', title: 'Analyse: Budget', createdAt: '2026-08-16T00:00:00Z', type: 'analysis', content: { text: '' } }],
      generating: false, handleDeleteGeneratedContent, podcastProgress: null,
    },
    handleSelectContent,
  }),
}));

beforeEach(() => {
  vi.clearAllMocks();
  activeChatId = null;
  for (const k of Object.keys(shellProps)) delete shellProps[k];
});

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

  it('lädt den bereits offenen Chat nicht neu, wechselt aber trotzdem den Reiter', async () => {
    activeChatId = 'c1';
    render(<HistoryPanel />);
    await userEvent.click(screen.getByText('Budget?'));
    expect(handleSelectChat).not.toHaveBeenCalled();
    expect(setKbView).toHaveBeenCalledWith('chat');
  });

  it('hängt an der LINKEN Seitenleiste', () => {
    render(<HistoryPanel />);
    expect(shellProps.side).toBe('left');
    expect(shellProps.isOpen).toBe(true);      // aus dem useKbLayout-Mock: isLeftSidebarOpen
    expect(shellProps.width).toBe(320);        // leftSidebarWidth, nicht rightSidebarWidth (500)
  });

  it('löscht ein Artefakt über handleDeleteGeneratedContent, nicht über handleDeleteChat', async () => {
    render(<HistoryPanel />);
    await userEvent.click(screen.getByRole('button', { name: 'deleteItem Analyse: Budget' }));
    expect(handleDeleteGeneratedContent).toHaveBeenCalledWith('g1', expect.anything());
    expect(handleDeleteChat).not.toHaveBeenCalled();
  });

  it('löscht einen Chat über handleDeleteChat, nicht über handleDeleteGeneratedContent', async () => {
    render(<HistoryPanel />);
    await userEvent.click(screen.getByRole('button', { name: 'deleteItem Budget?' }));
    expect(handleDeleteChat).toHaveBeenCalledWith('c1', expect.anything());
    expect(handleDeleteGeneratedContent).not.toHaveBeenCalled();
  });
});
