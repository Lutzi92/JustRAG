import { render, screen } from '@testing-library/react';
import { describe, it, expect, vi } from 'vitest';
import { KbWorkspaceLayout } from './KbWorkspaceLayout';

vi.mock('./sources/SourcesPanel', () => ({ SourcesPanel: () => <div data-testid="sources-panel" /> }));
vi.mock('./SidebarRight', () => ({ SidebarRight: () => <div data-testid="history-panel" /> }));
vi.mock('./ChatView', () => ({ ChatView: () => <div data-testid="chat-view" /> }));
vi.mock('../contexts/ThemeContext', () => ({ useTheme: () => ({ t: (k: string) => k }) }));
vi.mock('../contexts/MobileContext', () => ({ useIsMobileContext: () => false }));

const setIsRightSidebarOpen = vi.fn();
let kbView = 'chat';

vi.mock('../contexts/KbCoreContext', () => ({ useKbCore: () => ({ kbView }) }));
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

describe('KbWorkspaceLayout Quellen-Sichtbarkeit', () => {
  it('blendet die Quellenleiste im Workspace aus und danach wieder ein, ohne den Öffnen-Setter zu rufen', () => {
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
});
