import { describe, it, expect, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ConfluenceModal } from './ConfluenceModal';
import type { ConfluenceConnectionInfo } from '../../types';

vi.mock('../../contexts/ThemeContext', () => ({
  useTheme: () => ({
    t: (key: string) => key,
  }),
}));

// SourceModal pulls in useReducedMotion which calls window.matchMedia,
// not available in jsdom by default.
vi.mock('../../hooks/useReducedMotion', () => ({
  useReducedMotion: () => false,
  getMotionProps: () => ({}),
}));

const activeConnection: ConfluenceConnectionInfo = {
  enabled: true,
  baseUrl: 'https://confluence.example.com',
  connection: {
    id: 'conn-1',
    userId: 'user-1',
    displayName: 'alice',
    token: '••••1234',
    status: 'active',
    errorMessage: null,
    lastVerifiedAt: '2026-05-01T00:00:00Z',
    createdAt: '2026-04-01T00:00:00Z',
  },
};

const baseProps = {
  show: true,
  onClose: () => {},
  confluenceConnection: activeConnection,
  confluenceLoading: false,
  onSaveConnection: vi.fn(),
  onAddSource: vi.fn(),
  fetchSpaces: vi.fn(),
  fetchSpacePages: vi.fn(),
  fetchPageChildren: vi.fn(),
  fetchAllSpacePages: vi.fn(),
};

describe('ConfluenceModal failsafe', () => {
  it('renders the spaces picker when fetchSpaces succeeds', async () => {
    const fetchSpaces = vi.fn().mockResolvedValue([{ key: 'ENG', name: 'Engineering' }]);
    render(<ConfluenceModal {...baseProps} fetchSpaces={fetchSpaces} />);

    await waitFor(() => expect(fetchSpaces).toHaveBeenCalledTimes(1));
    await screen.findByText('Engineering');
    // No failsafe banner.
    expect(screen.queryByText('confluenceTokenInvalidTitle')).not.toBeInTheDocument();
  });

  it('does NOT re-fetch in a loop when fetchSpaces fails — the bug fix', async () => {
    // Bug pre-fix: the spaces effect kept retriggering because
    // spaces.length stayed at 0 and spacesLoading cycled false→true→false.
    // With the spacesAttempted guard, the effect runs at most once.
    const fetchSpaces = vi.fn().mockRejectedValue(new Error('Confluence rejected the token'));
    render(<ConfluenceModal {...baseProps} fetchSpaces={fetchSpaces} />);

    // Wait long enough that a buggy loop would re-fetch multiple times.
    await waitFor(() => expect(fetchSpaces).toHaveBeenCalledTimes(1));
    await new Promise(r => setTimeout(r, 50));
    expect(fetchSpaces).toHaveBeenCalledTimes(1);
  });

  it('shows the reconnect failsafe when fetchSpaces fails with the active connection', async () => {
    const fetchSpaces = vi.fn().mockRejectedValue(new Error('Confluence rejected the token'));
    render(<ConfluenceModal {...baseProps} fetchSpaces={fetchSpaces} />);

    // Failsafe title and the "how to create a token" disclosure are present.
    await screen.findByText('confluenceTokenInvalidTitle');
    expect(screen.getByText('confluenceTokenInvalidHelp')).toBeInTheDocument();
    expect(screen.getByText('confluencePatHowTo')).toBeInTheDocument();
    expect(screen.getByText('confluenceRetryAfterReconnect')).toBeInTheDocument();
  });

  it('retries spaces fetch after the user submits a new token via the failsafe', async () => {
    const user = userEvent.setup();
    const fetchSpaces = vi.fn()
      .mockRejectedValueOnce(new Error('Confluence rejected the token'))
      .mockResolvedValueOnce([{ key: 'ENG', name: 'Engineering' }]);
    const onSaveConnection = vi.fn().mockResolvedValue(undefined);
    render(
      <ConfluenceModal
        {...baseProps}
        fetchSpaces={fetchSpaces}
        onSaveConnection={onSaveConnection}
      />,
    );

    // First load fails → failsafe appears.
    await screen.findByText('confluenceTokenInvalidTitle');

    // User pastes a fresh token and clicks "Retry with new token".
    const input = screen.getByPlaceholderText('••••••••••••');
    await user.type(input, 'new-token-1234');
    await user.click(screen.getByText('confluenceRetryAfterReconnect'));

    await waitFor(() => expect(onSaveConnection).toHaveBeenCalledWith('new-token-1234'));
    // Second fetchSpaces call → success → list shows up.
    await waitFor(() => expect(fetchSpaces).toHaveBeenCalledTimes(2));
    await screen.findByText('Engineering');
  });
});
