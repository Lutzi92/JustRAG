import { describe, it, expect, vi, beforeEach } from 'vitest';
import { renderHook, waitFor } from '@testing-library/react';
import axios from 'axios';
import { useJoinRedeem } from './useJoinRedeem';
import { JOIN_TOKEN_KEY, JOIN_ATTEMPTS_KEY, MAX_JOIN_ATTEMPTS } from './useJoinLink';

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

  // The re-park introduced for retryable failures is unconditional per mount,
  // so without a budget a DURABLE non-404 error re-fires a request and an
  // error toast on every page load for the rest of the session. Each mount is
  // one page load; after MAX_JOIN_ATTEMPTS the token must be dropped.
  it('stops re-parking once the attempt budget is spent', async () => {
    window.sessionStorage.setItem(JOIN_TOKEN_KEY, 'tok');
    vi.mocked(axios.post).mockRejectedValue(axiosErr(500));
    const openKbById = vi.fn();

    // Each renderHook is a fresh mount, i.e. a fresh page load.
    for (let attempt = 1; attempt < MAX_JOIN_ATTEMPTS; attempt++) {
      renderHook(() => useJoinRedeem({ openKbById }));
      await waitFor(() => expect(toastMock.error).toHaveBeenCalledWith('joinLinkRetry'));
      expect(window.sessionStorage.getItem(JOIN_TOKEN_KEY)).toBe('tok');
      vi.clearAllMocks();
    }

    renderHook(() => useJoinRedeem({ openKbById }));
    await waitFor(() => expect(toastMock.error).toHaveBeenCalledWith('joinLinkFailed'));
    expect(toastMock.error).not.toHaveBeenCalledWith('joinLinkRetry');
    expect(window.sessionStorage.getItem(JOIN_TOKEN_KEY)).toBeNull();

    // A further page load must do nothing at all — no token, no request.
    vi.clearAllMocks();
    renderHook(() => useJoinRedeem({ openKbById }));
    await waitFor(() => expect(axios.post).not.toHaveBeenCalled());
    expect(toastMock.error).not.toHaveBeenCalled();
  });

  it('clears the attempt budget after a success, so a later failure gets its full retries', async () => {
    window.sessionStorage.setItem(JOIN_TOKEN_KEY, 'tok');
    window.sessionStorage.setItem(JOIN_ATTEMPTS_KEY, String(MAX_JOIN_ATTEMPTS - 1));
    vi.mocked(axios.post).mockResolvedValue({
      data: { kbId: 'kb-9', kbName: 'Kurs', role: 'view', alreadyMember: false },
    });

    renderHook(() => useJoinRedeem({ openKbById: vi.fn().mockResolvedValue(undefined) }));

    await waitFor(() => expect(toastMock.success).toHaveBeenCalled());
    expect(window.sessionStorage.getItem(JOIN_ATTEMPTS_KEY)).toBeNull();
  });

  it('clears the attempt budget on a terminal 404', async () => {
    window.sessionStorage.setItem(JOIN_TOKEN_KEY, 'bad');
    window.sessionStorage.setItem(JOIN_ATTEMPTS_KEY, '1');
    vi.mocked(axios.post).mockRejectedValue(axiosErr(404));

    renderHook(() => useJoinRedeem({ openKbById: vi.fn() }));

    await waitFor(() => expect(toastMock.error).toHaveBeenCalledWith('joinLinkInvalid'));
    expect(window.sessionStorage.getItem(JOIN_ATTEMPTS_KEY)).toBeNull();
  });

  // An AxiosError carries config.url, which contains the token. Printing the
  // error object would put a live credential in the browser console — and in
  // any error-reporting SDK wired up later.
  it('does not print the token when logging a failure', async () => {
    const consoleErr = vi.spyOn(console, 'error').mockImplementation(() => {});
    const token = 'AbCdEf0123456789AbCdEf0123456789AbCdEf012';
    window.sessionStorage.setItem(JOIN_TOKEN_KEY, token);
    vi.mocked(axios.post).mockRejectedValue(Object.assign(axiosErr(500), {
      config: { url: `/api/invites/${token}/redeem` },
    }));

    renderHook(() => useJoinRedeem({ openKbById: vi.fn() }));

    await waitFor(() => expect(consoleErr).toHaveBeenCalled());
    for (const call of consoleErr.mock.calls) {
      expect(JSON.stringify(call)).not.toContain(token);
    }
    consoleErr.mockRestore();
  });
});
