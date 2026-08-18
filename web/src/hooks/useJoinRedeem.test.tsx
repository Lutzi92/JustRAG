import { describe, it, expect, vi, beforeEach } from 'vitest';
import { renderHook, waitFor } from '@testing-library/react';
import axios from 'axios';
import { useJoinRedeem } from './useJoinRedeem';
import { JOIN_TOKEN_KEY } from './useJoinLink';

vi.mock('axios');

const toastMock = { success: vi.fn(), error: vi.fn(), warning: vi.fn() };
vi.mock('../contexts/ToastContext', () => ({ useToast: () => toastMock }));
vi.mock('../contexts/ThemeContext', () => ({ useTheme: () => ({ t: (k: string) => k }) }));

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

  it('clears the token before the request, so a failure cannot loop', async () => {
    window.sessionStorage.setItem(JOIN_TOKEN_KEY, 'bad');
    vi.mocked(axios.post).mockRejectedValue(new Error('404'));
    const openKbById = vi.fn();

    renderHook(() => useJoinRedeem({ openKbById }));

    await waitFor(() => expect(toastMock.error).toHaveBeenCalled());
    expect(window.sessionStorage.getItem(JOIN_TOKEN_KEY)).toBeNull();
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
