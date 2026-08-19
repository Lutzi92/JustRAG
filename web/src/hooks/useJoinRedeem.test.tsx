import { describe, it, expect, vi, beforeEach } from 'vitest';
import { renderHook, waitFor } from '@testing-library/react';
import axios from 'axios';
import { useJoinRedeem } from './useJoinRedeem';
import { JOIN_TOKEN_KEY } from './useJoinLink';

vi.mock('axios');
const mockedAxios = axios as unknown as {
  post: ReturnType<typeof vi.fn>;
  isAxiosError: (err: unknown) => boolean;
};

const toastMock = { success: vi.fn(), error: vi.fn(), warning: vi.fn() };
vi.mock('../contexts/ToastContext', () => ({ useToast: () => toastMock }));
vi.mock('../contexts/ThemeContext', () => ({ useTheme: () => ({ t: (k: string) => k }) }));

function axiosErr(status: number) {
  return Object.assign(new Error(`status ${status}`), {
    isAxiosError: true,
    response: { status },
  });
}

class MemoryStorage implements Storage {
  private data = new Map<string, string>();
  get length() { return this.data.size; }
  clear() { this.data.clear(); }
  getItem(k: string) { return this.data.get(k) ?? null; }
  key(i: number) { return Array.from(this.data.keys())[i] ?? null; }
  removeItem(k: string) { this.data.delete(k); }
  setItem(k: string, v: string) { this.data.set(k, v); }
}

beforeEach(() => {
  vi.clearAllMocks();
  Object.defineProperty(window, 'sessionStorage', {
    value: new MemoryStorage(), configurable: true, writable: true,
  });
  // vi.mock('axios') automocks every export, including isAxiosError, into a
  // vi.fn() that returns undefined. Reimplement it the same way the real
  // axios does — checking the `isAxiosError` marker — so errors built with
  // that marker in these tests are recognized correctly (see useSharing.test.tsx).
  mockedAxios.isAxiosError = (err: unknown) => !!(err as { isAxiosError?: boolean } | null)?.isAxiosError;
});

describe('useJoinRedeem', () => {
  it('redeems a parked token and opens the KB', async () => {
    window.sessionStorage.setItem(JOIN_TOKEN_KEY, 'tok');
    vi.mocked(axios.post).mockResolvedValue({
      data: { kbId: 'kb-9', kbName: 'Kurs', role: 'view', alreadyMember: false },
    });
    const openKbById = vi.fn().mockResolvedValue(undefined);

    renderHook(() => useJoinRedeem({ openKbById }));

    await waitFor(() => expect(openKbById).toHaveBeenCalledWith('kb-9'));
    expect(axios.post).toHaveBeenCalledWith(expect.stringContaining('/api/invites/tok/redeem'));
    expect(toastMock.success).toHaveBeenCalled();
  });

  it('does nothing when no token is parked', async () => {
    const openKbById = vi.fn();
    renderHook(() => useJoinRedeem({ openKbById }));

    await waitFor(() => expect(axios.post).not.toHaveBeenCalled());
    expect(openKbById).not.toHaveBeenCalled();
  });

  it('on a 404, leaves the token cleared and shows the invalid-link message', async () => {
    window.sessionStorage.setItem(JOIN_TOKEN_KEY, 'bad');
    vi.mocked(axios.post).mockRejectedValue(axiosErr(404));
    const openKbById = vi.fn();

    renderHook(() => useJoinRedeem({ openKbById }));

    await waitFor(() => expect(toastMock.error).toHaveBeenCalledWith('joinLinkInvalid'));
    expect(window.sessionStorage.getItem(JOIN_TOKEN_KEY)).toBeNull();
    expect(openKbById).not.toHaveBeenCalled();
  });

  it('on a 500, re-parks the token and shows the distinct retryable message', async () => {
    window.sessionStorage.setItem(JOIN_TOKEN_KEY, 'tok');
    vi.mocked(axios.post).mockRejectedValue(axiosErr(500));
    const openKbById = vi.fn();

    renderHook(() => useJoinRedeem({ openKbById }));

    await waitFor(() => expect(toastMock.error).toHaveBeenCalledWith('joinLinkRetry'));
    expect(window.sessionStorage.getItem(JOIN_TOKEN_KEY)).toBe('tok');
    expect(openKbById).not.toHaveBeenCalled();
  });

  it('on a network error (no response), re-parks the token and shows the retryable message', async () => {
    window.sessionStorage.setItem(JOIN_TOKEN_KEY, 'tok');
    const networkErr = Object.assign(new Error('Network Error'), { isAxiosError: true });
    vi.mocked(axios.post).mockRejectedValue(networkErr);
    const openKbById = vi.fn();

    renderHook(() => useJoinRedeem({ openKbById }));

    await waitFor(() => expect(toastMock.error).toHaveBeenCalledWith('joinLinkRetry'));
    expect(window.sessionStorage.getItem(JOIN_TOKEN_KEY)).toBe('tok');
    expect(openKbById).not.toHaveBeenCalled();
  });

  it('redeems at most once even if the component re-renders', async () => {
    window.sessionStorage.setItem(JOIN_TOKEN_KEY, 'tok');
    vi.mocked(axios.post).mockResolvedValue({
      data: { kbId: 'kb-9', kbName: 'Kurs', role: 'view', alreadyMember: false },
    });
    const openKbById = vi.fn().mockResolvedValue(undefined);

    const { rerender } = renderHook(() => useJoinRedeem({ openKbById }));
    rerender();
    rerender();

    await waitFor(() => expect(openKbById).toHaveBeenCalledTimes(1));
    expect(axios.post).toHaveBeenCalledTimes(1);
  });
});
