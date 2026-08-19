import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { KbWorkspaceLayout } from './KbWorkspaceLayout';

vi.mock('./sources/SourcesPanel', () => ({ SourcesPanel: () => <div data-testid="sources-panel" /> }));
vi.mock('./history/HistoryPanel', () => ({ HistoryPanel: () => <div data-testid="history-panel" /> }));
vi.mock('./ChatView', () => ({ ChatView: () => <div data-testid="chat-view" /> }));
vi.mock('../contexts/ThemeContext', () => ({ useTheme: () => ({ t: (k: string) => k }) }));

let isMobile = false;
vi.mock('../contexts/MobileContext', () => ({ useIsMobileContext: () => isMobile }));

const setIsRightSidebarOpen = vi.fn();
let kbView = 'chat';
const setKbView = vi.fn();

vi.mock('../contexts/KbCoreContext', () => ({ useKbCore: () => ({ kbView, setKbView }) }));
vi.mock('../contexts/KbLayoutContext', () => ({
  useKbLayout: () => ({
    sidebar: {
      isLeftSidebarOpen: true, isRightSidebarOpen: true,
      leftSidebarWidth: 320, rightSidebarWidth: 500,
      isResizingLeft: false, isResizingRight: false,
      setIsResizingLeft: vi.fn(), setIsResizingRight: vi.fn(),
      setLeftSidebarWidth: vi.fn(), setRightSidebarWidth: vi.fn(),
      setIsRightSidebarOpen,
    },
  }),
}));

const noSwipe = { onTouchStart: vi.fn(), onTouchEnd: vi.fn() };

beforeEach(() => {
  isMobile = false;
  kbView = 'chat';
  setKbView.mockClear();
  setIsRightSidebarOpen.mockClear();
});

describe('KbWorkspaceLayout Quellen-Sichtbarkeit', () => {
  it('blendet die Quellenleiste im Workspace aus und danach wieder ein, ohne den Öffnen-Setter zu rufen', () => {
    // Diese Assertion deckt NUR den Codepfad von KbWorkspaceLayout.tsx selbst
    // ab (es ruft `setIsRightSidebarOpen` an keiner Stelle auf) — nicht das
    // Verhalten von HistoryPanel oder SourcesPanel, die hier gemockt sind.
    // Die eigentliche Regression, gegen die dieser Test ursprünglich geschrieben
    // wurde, saß in AuthenticatedApp.handleExpandStudio; die Presence/Absence-
    // Assertions unten sind die primäre Absicherung dieses Tests.
    kbView = 'chat';
    const { rerender } = render(
      <KbWorkspaceLayout mobileTab="chat" setMobileTab={vi.fn()} swipeHandlers={noSwipe} />,
    );
    expect(screen.getByTestId('sources-panel')).toBeInTheDocument();
    expect(screen.getByTestId('history-panel')).toBeInTheDocument();

    kbView = 'workspace';
    rerender(<KbWorkspaceLayout mobileTab="chat" setMobileTab={vi.fn()} swipeHandlers={noSwipe} />);
    expect(screen.queryByTestId('sources-panel')).not.toBeInTheDocument();
    expect(screen.getByTestId('history-panel')).toBeInTheDocument();

    kbView = 'chat';
    rerender(<KbWorkspaceLayout mobileTab="chat" setMobileTab={vi.fn()} swipeHandlers={noSwipe} />);
    expect(screen.getByTestId('sources-panel')).toBeInTheDocument();
    expect(setIsRightSidebarOpen).not.toHaveBeenCalled();
  });

  it('rendert den Verlauf VOR dem Chat im DOM — "Verlauf nach links" ist sonst nur eine Behauptung', () => {
    kbView = 'chat';
    const { container } = render(
      <KbWorkspaceLayout mobileTab="chat" setMobileTab={vi.fn()} swipeHandlers={noSwipe} />,
    );
    const testIds = Array.from(container.querySelectorAll('[data-testid]')).map(el => el.getAttribute('data-testid'));
    expect(testIds.indexOf('history-panel')).toBeLessThan(testIds.indexOf('chat-view'));
  });
});

describe('KbWorkspaceLayout mobiler Zweig', () => {
  it('zeigt bei mobileTab="history" das Verlaufspanel und die Tab-Leiste', () => {
    isMobile = true;
    render(<KbWorkspaceLayout mobileTab="history" setMobileTab={vi.fn()} swipeHandlers={noSwipe} />);
    expect(screen.getByTestId('history-panel')).toBeInTheDocument();
    expect(screen.queryByTestId('chat-view')).not.toBeInTheDocument();
    expect(screen.queryByTestId('sources-panel')).not.toBeInTheDocument();
    expect(screen.getByRole('navigation')).toBeInTheDocument();
  });

  it('ruft beim Tippen auf den Workspace-Reiter setKbView("workspace") auf', async () => {
    isMobile = true;
    kbView = 'chat';
    const setMobileTab = vi.fn();
    render(<KbWorkspaceLayout mobileTab="chat" setMobileTab={setMobileTab} swipeHandlers={noSwipe} />);
    await userEvent.click(screen.getByRole('button', { name: 'tabWorkspace' }));
    expect(setKbView).toHaveBeenCalledWith('workspace');
    expect(setMobileTab).toHaveBeenCalledWith('workspace');
  });

  it('GUARD (#1): bei mobileTab="chat" aber kbView="workspace" zeigt die Leiste "workspace" als aktiven Reiter, nicht "chat"', () => {
    isMobile = true;
    kbView = 'workspace';
    render(<KbWorkspaceLayout mobileTab="chat" setMobileTab={vi.fn()} swipeHandlers={noSwipe} />);
    expect(screen.getByRole('button', { name: 'tabWorkspace' })).toHaveAttribute('aria-current', 'page');
    expect(screen.getByRole('button', { name: 'tabChat' })).not.toHaveAttribute('aria-current');
  });
});
