import { render, screen } from '@testing-library/react';
import { describe, it, expect, vi } from 'vitest';
import { SourcesPanel } from './SourcesPanel';

vi.mock('../sidebar/SourcesGrid', () => ({ SourcesGrid: () => <div data-testid="sources-grid" /> }));
vi.mock('../sidebar/SourcesSection', () => ({ SourcesSection: () => <div data-testid="sources-section" /> }));
vi.mock('../sidebar/CrawlModal', () => ({ CrawlModal: () => null }));
vi.mock('../sidebar/RssModal', () => ({ RssModal: () => null }));
vi.mock('../sidebar/ConfluenceModal', () => ({ ConfluenceModal: () => null }));
vi.mock('../sidebar/GitRepoModal', () => ({ GitRepoModal: () => null }));
vi.mock('../sidebar/FileUploadModal', () => ({ FileUploadModal: () => null }));
vi.mock('../../contexts/MobileContext', () => ({ useIsMobileContext: () => false }));
vi.mock('../../contexts/ThemeContext', () => ({ useTheme: () => ({ t: (k: string) => k }) }));
vi.mock('../../contexts/AuthContext', () => ({ useAuth: () => ({ siteConfigs: {} }) }));
vi.mock('../../contexts/KbCoreContext', () => ({
  useKbCore: () => ({ currentKb: { id: 'kb1', isGlobal: false }, setKbView: vi.fn(), handleGoHome: vi.fn(), handleViewHome: vi.fn() }),
}));
vi.mock('../../contexts/KbLayoutContext', () => ({
  useKbLayout: () => ({ sidebar: { isRightSidebarOpen: true, rightSidebarWidth: 500, setIsRightSidebarOpen: vi.fn() } }),
}));
vi.mock('../../contexts/KbDataContext', () => ({
  useKbData: () => ({
    fileMgmt: { files: [], fileInputRef: { current: null }, showUploadModal: false, setShowUploadModal: vi.fn() },
    webTools: { setToolTab: vi.fn(), searchResults: [], crawlResults: [], sourcesAddedCount: 0, setShowWebWorkspace: vi.fn() },
    rssFeeds: [], confluenceSources: [], gitRepoSources: [],
  }),
}));

describe('SourcesPanel', () => {
  it('rendert in der rechten Spalte', () => {
    const { container } = render(<SourcesPanel />);
    expect(container.querySelector('aside')).toHaveClass('sidebar-shell--right');
    expect(screen.getByTestId('sources-section')).toBeInTheDocument();
  });
});
