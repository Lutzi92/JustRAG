import { useTheme } from '../contexts/ThemeContext';
import { useIsMobileContext } from '../contexts/MobileContext';
import { useKbCore } from '../contexts/KbCoreContext';
import { useKbLayout } from '../contexts/KbLayoutContext';
import { SourcesPanel } from './sources/SourcesPanel';
import { SidebarRight } from './SidebarRight';
import { ChatView } from './ChatView';
import { MobileTabBar, type MobileTab } from './MobileTabBar';

interface KbWorkspaceLayoutProps {
  mobileTab: MobileTab;
  setMobileTab: (tab: MobileTab) => void;
  swipeHandlers: { onTouchStart: (e: React.TouchEvent) => void; onTouchEnd: (e: React.TouchEvent) => void };
}

export function KbWorkspaceLayout({ mobileTab, setMobileTab, swipeHandlers }: KbWorkspaceLayoutProps) {
  const { t } = useTheme();
  const isMobile = useIsMobileContext();
  const { kbView } = useKbCore();
  const { sidebar } = useKbLayout();

  // Die Quellenleiste wird im Workspace ausgeblendet, aber NICHT zugeklappt:
  // `setIsRightSidebarOpen(false)` (so lief es bis 2026-08) ließ sie nach dem
  // Verlassen des Workspace zugeklappt zurück, weil niemand sie wieder öffnete.
  const showSources = kbView !== 'workspace';

  if (isMobile) {
    return (
      <div className="notebook-container notebook-container--mobile" {...swipeHandlers}>
        {mobileTab === 'files' && <SourcesPanel />}
        {mobileTab === 'chat' && <ChatView />}
        {mobileTab === 'studio' && <SidebarRight />}
        <MobileTabBar activeTab={mobileTab} onTabChange={setMobileTab} />
      </div>
    );
  }

  return (
    <div className="notebook-container">
      <SidebarRight />

      {/* Resize Handle Left */}
      {sidebar.isLeftSidebarOpen && (
        <div
          role="slider"
          aria-orientation="vertical"
          aria-label={t('resizeLeftSidebar')}
          aria-valuemin={150}
          aria-valuemax={600}
          aria-valuenow={sidebar.leftSidebarWidth}
          tabIndex={0}
          onMouseDown={() => sidebar.setIsResizingLeft(true)}
          onKeyDown={(e) => {
            if (e.key === 'ArrowLeft') {
              e.preventDefault();
              sidebar.setLeftSidebarWidth?.(Math.max(150, sidebar.leftSidebarWidth - 10));
            } else if (e.key === 'ArrowRight') {
              e.preventDefault();
              sidebar.setLeftSidebarWidth?.(Math.min(600, sidebar.leftSidebarWidth + 10));
            }
          }}
          className="resize-handle"
          style={{
            width: '5px',
            cursor: 'col-resize',
            background: sidebar.isResizingLeft ? 'var(--accent-primary)' : 'transparent',
            zIndex: 20,
            transition: 'background 0.2s',
            borderRight: '1px solid var(--border-color)',
            marginLeft: '-1px'
          }}
        />
      )}

      {/* Main Content */}
      <ChatView />

      {/* Resize Handle Right */}
      {showSources && (
        <div
          role="slider"
          aria-orientation="vertical"
          aria-label={t('resizeRightSidebar')}
          aria-valuemin={150}
          aria-valuemax={800}
          aria-valuenow={sidebar.rightSidebarWidth}
          tabIndex={0}
          onMouseDown={() => sidebar.setIsResizingRight(true)}
          onKeyDown={(e) => {
            if (e.key === 'ArrowLeft') {
              e.preventDefault();
              sidebar.setRightSidebarWidth?.(Math.min(800, sidebar.rightSidebarWidth + 10));
            } else if (e.key === 'ArrowRight') {
              e.preventDefault();
              sidebar.setRightSidebarWidth?.(Math.max(150, sidebar.rightSidebarWidth - 10));
            }
          }}
          style={{
            width: '5px',
            cursor: 'col-resize',
            background: sidebar.isResizingRight ? 'var(--accent-primary)' : 'transparent',
            zIndex: 20,
            transition: 'background 0.2s',
            borderLeft: '1px solid var(--border-color)',
            marginRight: '-1px'
          }}
          className="resize-handle"
        />
      )}

      {showSources && <SourcesPanel />}
    </div>
  );
}
