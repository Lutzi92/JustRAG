import { describe, it, expect, vi, beforeEach } from 'vitest';
import { renderHook, act, waitFor } from '@testing-library/react';
import axios from 'axios';
import { useSharing } from './useSharing';

vi.mock('axios');
const mockedAxios = axios as unknown as {
  get: ReturnType<typeof vi.fn>;
  isAxiosError: (err: unknown) => boolean;
};

const toastMock = { success: vi.fn(), error: vi.fn(), warning: vi.fn() };
vi.mock('../contexts/ThemeContext', () => ({ useTheme: () => ({ t: (k: string) => k }) }));
vi.mock('../contexts/ToastContext', () => ({ useToast: () => toastMock }));

describe('useSharing', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    // vi.mock('axios') automocks every export, including isAxiosError, into a
    // vi.fn() that returns undefined. Reimplement it the same way the real
    // axios does — checking the `isAxiosError` marker — so errors built with
    // that marker in these tests are recognized correctly.
    mockedAxios.isAxiosError = (err: unknown) => !!(err as { isAxiosError?: boolean } | null)?.isAxiosError;
  });

  it('normalizes a messy username into notFoundUsername on a 404 lookup', async () => {
    const notFoundErr = Object.assign(new Error('Not Found'), {
      isAxiosError: true,
      response: { status: 404 },
    });
    mockedAxios.get = vi.fn().mockRejectedValue(notFoundErr);

    const { result } = renderHook(() => useSharing({}));

    act(() => {
      result.current.setShareUserId('  MMuster12 ');
    });
    expect(result.current.shareUserId).toBe('  MMuster12 ');

    await act(async () => {
      await result.current.lookupUser();
    });

    await waitFor(() => {
      expect(result.current.notFoundUsername).toBe('mmuster12');
    });
    expect(toastMock.error).not.toHaveBeenCalled();
  });

  it('does not treat a non-404 failure as "not found" and surfaces a toast instead', async () => {
    const serverErr = Object.assign(new Error('Internal Server Error'), {
      isAxiosError: true,
      response: { status: 500 },
    });
    mockedAxios.get = vi.fn().mockRejectedValue(serverErr);

    const { result } = renderHook(() => useSharing({}));

    act(() => {
      result.current.setShareUserId('mmuster12');
    });

    await act(async () => {
      await result.current.lookupUser();
    });

    await waitFor(() => {
      expect(toastMock.error).toHaveBeenCalled();
    });
    expect(result.current.notFoundUsername).toBeNull();
  });

  it('clears a stale notFoundUsername when the input is edited', async () => {
    const notFoundErr = Object.assign(new Error('Not Found'), {
      isAxiosError: true,
      response: { status: 404 },
    });
    mockedAxios.get = vi.fn().mockRejectedValue(notFoundErr);

    const { result } = renderHook(() => useSharing({}));

    act(() => {
      result.current.setShareUserId('alice');
    });
    await act(async () => {
      await result.current.lookupUser();
    });
    await waitFor(() => {
      expect(result.current.notFoundUsername).toBe('alice');
    });

    // Editing the input without re-running the search must drop the stale
    // notFoundUsername for the previous, different username.
    act(() => {
      result.current.setShareUserId('alicia');
    });
    expect(result.current.notFoundUsername).toBeNull();
  });

  it('exposes a referentially stable setShareUserId across renders', () => {
    mockedAxios.get = vi.fn().mockResolvedValue({ data: {} });
    const { result, rerender } = renderHook(() => useSharing({}));
    const first = result.current.setShareUserId;
    rerender();
    expect(result.current.setShareUserId).toBe(first);
  });
});
