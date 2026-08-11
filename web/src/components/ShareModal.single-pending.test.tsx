import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import axios from 'axios';
import { ShareModal } from './ShareModal';

vi.mock('axios');
const mockedAxios = axios as unknown as { get: ReturnType<typeof vi.fn>; post: ReturnType<typeof vi.fn>; delete: ReturnType<typeof vi.fn> };

vi.mock('../hooks/useReducedMotion', () => ({
  useReducedMotion: () => false,
  getMotionProps: () => ({}),
}));

// noAccountYet carries a {username} placeholder the component fills in.
const tMock = (k: string) => (k === 'noAccountYet' ? 'No account yet for "{username}"' : k);
// Real context hooks return a referentially stable value across renders (the
// provider's state/memo doesn't change just because a child re-rendered).
// These mocks must do the same: hoisted, module-level singleton objects
// returned as-is on every call. Returning a *fresh* object literal from the
// factory (the naive `useX: () => ({...})` pattern) makes every consumer's
// prop/useCallback dependency on that value change identity on every render,
// which — for ShareModal's `fetchShares` useCallback (deps include `toast`)
// — re-fires the mount useEffect several times before things settle and
// makes any assertion on axios.get's call count non-deterministic.
const themeMock = { t: tMock };
const authMock = { token: 'tok' };
const modalMock = { showConfirm: () => Promise.resolve(true) };
const toastMock = { success: vi.fn(), error: vi.fn(), warning: vi.fn() };
vi.mock('../contexts/ThemeContext', () => ({ useTheme: () => themeMock }));
vi.mock('../contexts/AuthContext', () => ({ useAuth: () => authMock }));
vi.mock('../contexts/ModalContext', () => ({ useModalContext: () => modalMock }));
vi.mock('../contexts/ToastContext', () => ({ useToast: () => toastMock }));

const baseProps = {
  show: true,
  onClose: () => {},
  sharingKb: { id: 'kb-1', name: 'Test KB' } as never,
  shareUserId: 'mmuster12',
  setShareUserId: () => {},
  shareTargetUser: null,
  shareLoading: false,
  sharePermission: 'view' as const,
  setSharePermission: () => {},
  onLookupUser: () => {},
  onConfirmShare: () => {},
  notFoundUsername: null as string | null,
  onPendingInvited: () => {},
};

describe('ShareModal single-tab pending invite', () => {
  beforeEach(() => {
    mockedAxios.get = vi.fn().mockResolvedValue({ data: { shares: [], pending: [] } });
    mockedAxios.post = vi.fn().mockResolvedValue({ data: { shared: [], pending: ['mmuster12'], alreadyHadAccess: [] } });
    mockedAxios.delete = vi.fn().mockResolvedValue({});
  });

  it('shows no block when the username resolved', () => {
    render(<ShareModal {...baseProps} />);
    expect(screen.queryByText('inviteAnyway')).not.toBeInTheDocument();
  });

  it('renders the warning with the typed username when lookup found nothing', async () => {
    render(<ShareModal {...baseProps} notFoundUsername="mmuster12" />);
    expect(await screen.findByText('No account yet for "mmuster12"')).toBeInTheDocument();
    expect(screen.getByText('pendingInviteHint')).toBeInTheDocument();
    expect(screen.getByText('inviteAnyway')).toBeInTheDocument();
  });

  it('posts a one-element bulk invite and refreshes the lists', async () => {
    const onPendingInvited = vi.fn();
    render(<ShareModal {...baseProps} notFoundUsername="mmuster12" onPendingInvited={onPendingInvited} />);

    // The mount effect fires exactly one GET (the mocks above are stable
    // singletons, matching real Context providers, so fetchShares' identity
    // doesn't churn across renders and re-fire the effect).
    await waitFor(() => expect(mockedAxios.get).toHaveBeenCalledTimes(1));

    fireEvent.click(await screen.findByText('inviteAnyway'));

    await waitFor(() => {
      expect(mockedAxios.post).toHaveBeenCalledWith(
        expect.stringContaining('/api/kb/kb-1/share/bulk'),
        { usernames: ['mmuster12'], permission: 'view' },
      );
    });
    // The pending list is re-fetched so the new invite shows immediately.
    await waitFor(() => expect(mockedAxios.get).toHaveBeenCalledTimes(2));
    expect(onPendingInvited).toHaveBeenCalled();
  });

  it('sends the selected edit permission', async () => {
    render(<ShareModal {...baseProps} notFoundUsername="mmuster12" sharePermission="edit" />);

    fireEvent.click(await screen.findByText('inviteAnyway'));

    await waitFor(() => {
      expect(mockedAxios.post).toHaveBeenCalledWith(
        expect.stringContaining('/api/kb/kb-1/share/bulk'),
        { usernames: ['mmuster12'], permission: 'edit' },
      );
    });
  });
});
