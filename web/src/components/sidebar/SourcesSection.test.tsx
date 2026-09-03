import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { SourcesSection } from './SourcesSection';
import type { FileEntry, RssFeed, ConfluenceSource, GitRepoSource } from '../../types';

vi.mock('../../contexts/ThemeContext', () => ({
  useTheme: () => ({ t: (key: string) => key }),
}));
vi.mock('../../hooks/useReducedMotion', () => ({
  useReducedMotion: () => false,
  getMotionProps: () => ({}),
}));

const makeFile = (over: Partial<FileEntry>): FileEntry => ({
  id: 'f-1', name: 'doc.pdf', type: 'application/pdf',
  status: 'completed', progress: 100, origin: 'upload',
  createdAt: '2026-06-12T00:00:00Z', selected: true,
  ...over,
});

const makeRssFeed = (over: Partial<RssFeed> = {}): RssFeed => ({
  id: 'feed-1', kbId: 'kb-1', url: 'https://example.com/feed.xml', title: 'Feed',
  syncSchedule: 'manual', nextSyncAt: null, status: 'active', errorMessage: null,
  consecutiveFailures: 0, lastPolledAt: null, itemCount: 0, fetchFullText: false,
  createdAt: '2026-06-12T00:00:00Z',
  ...over,
});

const makeConfluenceSource = (over: Partial<ConfluenceSource> = {}): ConfluenceSource => ({
  id: 'conf-1', kbId: 'kb-1', connectionId: 'conn-1', spaceKey: 'ENG', rootPageId: null,
  rootPageTitle: null, includeAttachments: false, syncSchedule: 'manual', nextSyncAt: null,
  status: 'active', errorMessage: null, consecutiveFailures: 0, lastSyncedAt: null,
  pageCount: 0, syncProgress: 0, syncTotal: 0, createdAt: '2026-06-12T00:00:00Z',
  ...over,
});

const makeGitRepoSource = (over: Partial<GitRepoSource> = {}): GitRepoSource => ({
  id: 'git-1', kbId: 'kb-1', repoUrl: 'https://github.com/acme/repo', isPrivate: false,
  branch: null, hasToken: false, syncSchedule: 'manual', nextSyncAt: null,
  status: 'active', errorMessage: null, consecutiveFailures: 0, lastSyncedAt: null,
  lastCommitSha: null, fileCount: 0, syncProgress: 0, syncTotal: 0,
  createdAt: '2026-06-12T00:00:00Z',
  ...over,
});

const baseProps = {
  onPreviewSource: vi.fn(),
  onToggleFileSelection: vi.fn(),
  onToggleFilesSelection: vi.fn(),
  onDownloadFile: vi.fn(),
  onDeleteFile: vi.fn(),
  rssFeeds: [],
  onUpdateRssFeed: vi.fn(),
  onDeleteRssFeed: vi.fn(),
  onPollFeedNow: vi.fn(),
  onViewFeed: vi.fn(),
  confluenceSources: [],
  onUpdateConfluenceSource: vi.fn(),
  onDeleteConfluenceSource: vi.fn(),
  onSyncConfluenceNow: vi.fn(),
  gitRepoSources: [],
  onUpdateGitRepoSource: vi.fn(),
  onDeleteGitRepoSource: vi.fn(),
  onSyncGitRepoNow: vi.fn(),
  onRetryFile: vi.fn(),
  onRetryAllFailed: vi.fn(),
};

describe('SourcesSection error display + retry', () => {
  it('shows the translated stage label and retry button for errored files', async () => {
    const onRetryFile = vi.fn();
    const file = makeFile({ status: 'error', errorStage: 'parse', errorMessage: 'The file could not be parsed' });
    render(<SourcesSection {...baseProps} files={[file]} onRetryFile={onRetryFile} />);

    // Stage maps to the translation key (t() is identity-mocked).
    expect(screen.getByText('fileErrorParse')).toBeInTheDocument();
    // Raw message rides along as the tooltip.
    expect(screen.getByText('fileErrorParse')).toHaveAttribute('title', 'The file could not be parsed');

    await userEvent.click(screen.getByRole('button', { name: 'retrySource doc.pdf' }));
    expect(onRetryFile).toHaveBeenCalledWith('f-1');
  });

  it('falls back to errorMessage for unknown stages and to fileErrorUnknown without any detail', () => {
    const weird = makeFile({ id: 'f-2', name: 'w.pdf', status: 'error', errorStage: 'martian', errorMessage: 'Strange failure' });
    const legacy = makeFile({ id: 'f-3', name: 'l.pdf', status: 'error' });
    render(<SourcesSection {...baseProps} files={[weird, legacy]} />);

    expect(screen.getByText('Strange failure')).toBeInTheDocument();
    expect(screen.getByText('fileErrorUnknown')).toBeInTheDocument();
  });

  it('hides retry controls when nothing failed', () => {
    render(<SourcesSection {...baseProps} files={[makeFile({})]} />);
    expect(screen.queryByText(/retryAllFailed/)).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /retrySource/ })).not.toBeInTheDocument();
  });

  it('shows the bulk retry button with the failed count and fires the callback', async () => {
    const onRetryAllFailed = vi.fn();
    const files = [
      makeFile({ id: 'f-1', status: 'error' }),
      makeFile({ id: 'f-2', name: 'b.pdf', status: 'error' }),
      makeFile({ id: 'f-3', name: 'c.pdf' }),
    ];
    render(<SourcesSection {...baseProps} files={files} onRetryAllFailed={onRetryAllFailed} />);

    const btn = screen.getByRole('button', { name: /retryAllFailed \(2\)/ });
    await userEvent.click(btn);
    expect(onRetryAllFailed).toHaveBeenCalledTimes(1);
  });
});

// The API contract: nextSyncAt is displayed only when the source's schedule
// is not 'manual' AND the value is non-null. Asserted on the text the user
// actually sees (t() is identity-mocked, so t('nextSync') renders literally
// as "nextSync") rather than on internal props, per each of the three source
// types the rule applies to.
describe('SourcesSection next-sync display', () => {
  it('shows the next sync time for an RSS feed on a non-manual schedule', () => {
    const feed = makeRssFeed({ syncSchedule: 'daily', nextSyncAt: '2026-09-04T02:00:00Z' });
    render(<SourcesSection {...baseProps} files={[]} rssFeeds={[feed]} />);
    expect(screen.getByText(/nextSync/)).toBeInTheDocument();
  });

  it('hides the next sync time for an RSS feed on a manual schedule, even with a value set', () => {
    const feed = makeRssFeed({ syncSchedule: 'manual', nextSyncAt: '2026-09-04T02:00:00Z' });
    render(<SourcesSection {...baseProps} files={[]} rssFeeds={[feed]} />);
    expect(screen.queryByText(/nextSync/)).not.toBeInTheDocument();
  });

  it('hides the next sync time for an RSS feed with no value, even on a non-manual schedule', () => {
    const feed = makeRssFeed({ syncSchedule: 'daily', nextSyncAt: null });
    render(<SourcesSection {...baseProps} files={[]} rssFeeds={[feed]} />);
    expect(screen.queryByText(/nextSync/)).not.toBeInTheDocument();
  });

  it('shows the next sync time for a Confluence source on a non-manual schedule', () => {
    const source = makeConfluenceSource({ syncSchedule: 'weekly', nextSyncAt: '2026-09-07T02:00:00Z' });
    render(<SourcesSection {...baseProps} files={[]} confluenceSources={[source]} />);
    expect(screen.getByText(/nextSync/)).toBeInTheDocument();
  });

  it('hides the next sync time for a Confluence source on a manual schedule, even with a value set', () => {
    const source = makeConfluenceSource({ syncSchedule: 'manual', nextSyncAt: '2026-09-07T02:00:00Z' });
    render(<SourcesSection {...baseProps} files={[]} confluenceSources={[source]} />);
    expect(screen.queryByText(/nextSync/)).not.toBeInTheDocument();
  });

  it('hides the next sync time for a Confluence source with no value, even on a non-manual schedule', () => {
    const source = makeConfluenceSource({ syncSchedule: 'weekly', nextSyncAt: null });
    render(<SourcesSection {...baseProps} files={[]} confluenceSources={[source]} />);
    expect(screen.queryByText(/nextSync/)).not.toBeInTheDocument();
  });

  it('shows the next sync time for a git repo source on a non-manual schedule', () => {
    const source = makeGitRepoSource({ syncSchedule: 'daily', nextSyncAt: '2026-09-04T02:00:00Z' });
    render(<SourcesSection {...baseProps} files={[]} gitRepoSources={[source]} />);
    expect(screen.getByText(/nextSync/)).toBeInTheDocument();
  });

  it('hides the next sync time for a git repo source on a manual schedule, even with a value set', () => {
    const source = makeGitRepoSource({ syncSchedule: 'manual', nextSyncAt: '2026-09-04T02:00:00Z' });
    render(<SourcesSection {...baseProps} files={[]} gitRepoSources={[source]} />);
    expect(screen.queryByText(/nextSync/)).not.toBeInTheDocument();
  });

  it('hides the next sync time for a git repo source with no value, even on a non-manual schedule', () => {
    const source = makeGitRepoSource({ syncSchedule: 'daily', nextSyncAt: null });
    render(<SourcesSection {...baseProps} files={[]} gitRepoSources={[source]} />);
    expect(screen.queryByText(/nextSync/)).not.toBeInTheDocument();
  });
});

// F3(b): a schedule control is reachable on every source row (not just at
// creation time), wired to the existing update handlers, so a user can
// change their mind about a source's schedule without deleting and
// recreating it.
describe('SourcesSection per-row schedule control', () => {
  it('renders a schedule select for an RSS feed, pre-selected to its current schedule, and calls onUpdateRssFeed on change', () => {
    const onUpdateRssFeed = vi.fn();
    const feed = makeRssFeed({ syncSchedule: 'weekly' });
    render(<SourcesSection {...baseProps} files={[]} rssFeeds={[feed]} onUpdateRssFeed={onUpdateRssFeed} />);

    const select = screen.getByRole('combobox') as HTMLSelectElement;
    expect(select.value).toBe('weekly');

    fireEvent.change(select, { target: { value: 'daily' } });
    expect(onUpdateRssFeed).toHaveBeenCalledWith('feed-1', { syncSchedule: 'daily' });
  });

  it('renders a schedule select for a Confluence source and calls onUpdateConfluenceSource on change', () => {
    const onUpdateConfluenceSource = vi.fn();
    const source = makeConfluenceSource({ syncSchedule: 'manual' });
    render(<SourcesSection {...baseProps} files={[]} confluenceSources={[source]} onUpdateConfluenceSource={onUpdateConfluenceSource} />);

    const select = screen.getByRole('combobox') as HTMLSelectElement;
    expect(select.value).toBe('manual');

    fireEvent.change(select, { target: { value: 'weekly' } });
    expect(onUpdateConfluenceSource).toHaveBeenCalledWith('conf-1', { syncSchedule: 'weekly' });
  });

  it('renders a schedule select for a git repo source and calls onUpdateGitRepoSource on change', () => {
    const onUpdateGitRepoSource = vi.fn();
    const source = makeGitRepoSource({ syncSchedule: 'manual' });
    render(<SourcesSection {...baseProps} files={[]} gitRepoSources={[source]} onUpdateGitRepoSource={onUpdateGitRepoSource} />);

    const select = screen.getByRole('combobox') as HTMLSelectElement;
    expect(select.value).toBe('manual');

    fireEvent.change(select, { target: { value: 'daily' } });
    expect(onUpdateGitRepoSource).toHaveBeenCalledWith('git-1', { syncSchedule: 'daily' });
  });

  it('gives each row schedule select a unique id for label association when multiple sources are shown', () => {
    const feed = makeRssFeed({ id: 'feed-a' });
    const feed2 = makeRssFeed({ id: 'feed-b' });
    render(<SourcesSection {...baseProps} files={[]} rssFeeds={[feed, feed2]} />);

    const selects = screen.getAllByRole('combobox') as HTMLSelectElement[];
    expect(selects).toHaveLength(2);
    expect(selects[0].id).not.toBe(selects[1].id);
    expect(new Set(selects.map(s => s.id)).size).toBe(2);
  });
});
