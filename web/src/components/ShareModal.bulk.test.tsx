import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import axios from 'axios';
import { ShareModal } from './ShareModal';

vi.mock('axios');
const mockedAxios = axios as unknown as { get: ReturnType<typeof vi.fn>; post: ReturnType<typeof vi.fn>; delete: ReturnType<typeof vi.fn> };

// useReducedMotion calls window.matchMedia which doesn't exist in jsdom.
vi.mock('../hooks/useReducedMotion', () => ({
  useReducedMotion: () => false,
  getMotionProps: () => ({}),
}));

// Minimal context mocks — these hooks are imported by ShareModal.
// bulkInviteSummary must include the placeholder strings so the component's
// .replace('{shared}', ...) calls produce text the test can assert on.
const tMock = (k: string) =>
  k === 'bulkInviteSummary' ? '{shared} shared now · {pending} pending · {already} already had access' : k;
vi.mock('../contexts/ThemeContext', () => ({ useTheme: () => ({ t: tMock }) }));
vi.mock('../contexts/AuthContext', () => ({ useAuth: () => ({ token: 'tok' }) }));
vi.mock('../contexts/ModalContext', () => ({ useModalContext: () => ({ showConfirm: () => Promise.resolve(true) }) }));
vi.mock('../contexts/ToastContext', () => ({ useToast: () => ({ success: vi.fn(), error: vi.fn(), warning: vi.fn() }) }));

const baseProps = {
  show: true,
  onClose: () => {},
  sharingKb: { id: 'kb-1', name: 'Test KB' } as never,
  shareUserId: '',
  setShareUserId: () => {},
  shareTargetUser: null,
  shareLoading: false,
  sharePermission: 'view' as const,
  setSharePermission: () => {},
  onLookupUser: () => {},
  onConfirmShare: () => {},
};

describe('ShareModal bulk invite', () => {
  beforeEach(() => {
    mockedAxios.get = vi.fn().mockResolvedValue({ data: { shares: [], pending: [] } });
    mockedAxios.post = vi.fn().mockResolvedValue({ data: { shared: ['alice'], pending: ['carol'], alreadyHadAccess: [] } });
    mockedAxios.delete = vi.fn().mockResolvedValue({});
  });

  it('submits pasted usernames and shows a summary', async () => {
    render(<ShareModal {...baseProps} />);

    fireEvent.click(screen.getByText('bulkInvite')); // switch to bulk tab
    const textarea = await screen.findByLabelText('bulkInvite');
    fireEvent.change(textarea, { target: { value: 'alice, carol' } });
    fireEvent.click(screen.getByText('bulkInviteButton'));

    await waitFor(() => {
      expect(mockedAxios.post).toHaveBeenCalledWith(
        expect.stringContaining('/api/kb/kb-1/share/bulk'),
        { usernames: ['alice', 'carol'], permission: 'view' },
      );
    });
    // Summary text uses the bulkInviteSummary key; assert the resolved counts appear.
    await screen.findByText((s) => s.includes('1') && s.includes('shared'));
  });
});
