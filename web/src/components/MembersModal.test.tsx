import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, within, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import axios from 'axios';
import type { KnowledgeBase } from '../types';
import { MembersModal } from './MembersModal';

// framer-motion is mocked globally in src/test/setup.ts with a stable
// per-tag component cache — no per-file override needed here (see that
// file's comment for the CI-only remount flake a fresh-stub mock causes).

vi.mock('../hooks/useReducedMotion', () => ({
  useReducedMotion: () => false,
  getMotionProps: () => ({}),
}));

// Real context hooks return referentially stable values across renders (the
// provider's state/memo doesn't change just because a child re-rendered).
// These mocks do the same: hoisted, module-level singleton objects returned
// as-is on every call. A fresh object literal per call (`useX: () => ({...})`)
// would make every consumer's prop/useCallback dependency on that value
// change identity on every render, which — for fetchMembers' useCallback
// (deps include `toast`) — re-fires the mount effect several times before
// things settle and makes any assertion on axios.get's call count
// non-deterministic.
// makeOwner needs a real space for the /make owner|zum owner/i queries below
// to match — every other key just echoes back, which is enough since the
// tests select by data-testid/role rather than translated text.
const tMock = (k: string) => (k === 'makeOwner' ? 'Make owner' : k);
const themeMock = { t: tMock };
const authMock = { token: 'tok' };
const showConfirm = vi.fn();
const modalMock = { showConfirm };
const toastMock = { success: vi.fn(), error: vi.fn(), warning: vi.fn() };
vi.mock('../contexts/ThemeContext', () => ({ useTheme: () => themeMock }));
vi.mock('../contexts/AuthContext', () => ({ useAuth: () => authMock }));
vi.mock('../contexts/ModalContext', () => ({ useModalContext: () => modalMock }));
vi.mock('../contexts/ToastContext', () => ({ useToast: () => toastMock }));

const kb = { id: 'kb-1', name: 'Test KB' } as KnowledgeBase;

const noopProps = {
  shareUserId: '',
  setShareUserId: () => {},
  shareTargetUser: null,
  shareLoading: false,
  sharePermission: 'view' as const,
  setSharePermission: () => {},
  onLookupUser: () => {},
  onConfirmShare: () => {},
  notFoundUsername: null,
  onPendingInvited: () => {},
};

const members = [
  { userId: 'u1', username: 'ada', firstName: 'Ada', lastName: 'L', role: 'owner' },
  { userId: 'u2', username: 'grace', firstName: 'Grace', lastName: 'H', role: 'admin' },
  { userId: 'u3', username: 'alan', firstName: 'Alan', lastName: 'T', role: 'view' },
];

const renderModal = (myRole: string) => {
  vi.spyOn(axios, 'get').mockResolvedValue({ data: { members, pending: [] } });
  return render(<MembersModal show onClose={vi.fn()} sharingKb={kb} myRole={myRole} {...noopProps} />);
};

describe('MembersModal', () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    showConfirm.mockReset();
    toastMock.error.mockReset();
    toastMock.success.mockReset();
  });

  it('renders the owner row without role select or remove button', async () => {
    renderModal('owner');
    const row = await screen.findByTestId('member-row-u1');
    expect(within(row).queryByRole('combobox')).toBeNull();
    expect(within(row).queryByRole('button', { name: /remove|entfernen/i })).toBeNull();
  });

  it('offers exactly view, edit and admin in the role select', async () => {
    renderModal('owner');
    const row = await screen.findByTestId('member-row-u3');
    const options = within(row).getAllByRole('option').map(o => o.getAttribute('value'));
    expect(options).toEqual(['view', 'edit', 'admin']);
  });

  it('hides the ownership transfer for a non-owner', async () => {
    renderModal('admin');
    await screen.findByTestId('member-row-u2');
    expect(screen.queryByRole('button', { name: /make owner|zum owner/i })).toBeNull();
  });

  it('posts the transfer for the owner', async () => {
    const post = vi.spyOn(axios, 'post').mockResolvedValue({ status: 204 });
    showConfirm.mockResolvedValue(true);
    renderModal('owner');

    await userEvent.click(await screen.findByRole('button', { name: /make owner|zum owner/i }));
    expect(post).toHaveBeenCalledWith(
      expect.stringContaining('/transfer-owner'), { userId: 'u2' });
  });

  it('rolls the role select back when the request fails', async () => {
    vi.spyOn(axios, 'put').mockRejectedValue(new Error('boom'));
    renderModal('owner');

    const row = await screen.findByTestId('member-row-u3');
    const select = within(row).getByRole('combobox');
    await userEvent.selectOptions(select, 'edit');

    // Optimistisch auf 'edit', nach dem Fehlschlag zurueck auf 'view'.
    await waitFor(() => expect(select).toHaveValue('view'));
  });
});
