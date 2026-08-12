import { describe, it, expect, vi, beforeEach } from 'vitest';
import { renderHook, act, waitFor } from '@testing-library/react';
import axios from 'axios';
import type { ReactNode } from 'react';
import { useKbRemoval } from './useKbRemoval';
import type { KnowledgeBase } from '../types';

const showConfirm = vi.fn();
vi.mock('../contexts/ModalContext', () => ({
  useModalContext: () => ({ showConfirm }),
}));

// t mirrors the real translations closely enough to exercise the {count}
// substitution the leave-confirmation dialog relies on.
vi.mock('../contexts/ThemeContext', () => ({
  useTheme: () => ({
    t: (key: string) => {
      const strings: Record<string, string> = {
        confirmDeleteKB: 'Delete this knowledge base?',
        confirmLeaveKb: 'Leave this KB? {count} of your chats will be deleted.',
        confirmLeaveKbNoChats: 'Leave this KB?',
      };
      return strings[key] ?? key;
    },
  }),
}));

function wrapper({ children }: { children: ReactNode }) {
  return children;
}

const kb = (myRole?: string) =>
  ({ id: 'kb-1', name: 'Handbuch', myRole } as KnowledgeBase);

describe('useKbRemoval', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('deletes the KB when the caller is the owner', async () => {
    const del = vi.spyOn(axios, 'delete').mockResolvedValue({ status: 204 });
    showConfirm.mockResolvedValue(true);

    const { result } = renderHook(() => useKbRemoval(), { wrapper });
    await act(async () => {
      await expect(result.current.removeKb(kb('owner'))).resolves.toBe('deleted');
    });

    expect(del).toHaveBeenCalledWith(expect.stringContaining('/api/kb/kb-1'));
    expect(del).not.toHaveBeenCalledWith(expect.stringContaining('/membership'));
  });

  it('leaves the KB and reports the chat count for a non-owner', async () => {
    vi.spyOn(axios, 'get').mockResolvedValue({ data: { chatCount: 3 } });
    const del = vi.spyOn(axios, 'delete').mockResolvedValue({ data: { deletedChats: 3 } });
    showConfirm.mockResolvedValue(true);

    const { result } = renderHook(() => useKbRemoval(), { wrapper });
    await act(async () => {
      await expect(result.current.removeKb(kb('edit'))).resolves.toBe('left');
    });

    // Der Dialog muss die Zahl nennen — sonst loescht der Nutzer blind Chats.
    expect(showConfirm).toHaveBeenCalledWith(expect.stringContaining('3'));
    expect(del).toHaveBeenCalledWith(expect.stringContaining('/api/kb/kb-1/membership'));
  });

  it('sends no request when the confirmation is dismissed', async () => {
    const del = vi.spyOn(axios, 'delete');
    showConfirm.mockResolvedValue(false);

    const { result } = renderHook(() => useKbRemoval(), { wrapper });
    await act(async () => {
      await expect(result.current.removeKb(kb('owner'))).resolves.toBe('cancelled');
    });
    expect(del).not.toHaveBeenCalled();
  });

  it('treats an implicit viewer as leave, never delete', async () => {
    vi.spyOn(axios, 'get').mockRejectedValue({ response: { status: 404 } });
    const del = vi.spyOn(axios, 'delete').mockResolvedValue({ data: { deletedChats: 0 } });
    showConfirm.mockResolvedValue(true);

    const { result } = renderHook(() => useKbRemoval(), { wrapper });
    // myRole ist undefined: implizite view-Rolle auf einer globalen KB.
    await act(async () => {
      await expect(result.current.removeKb(kb(undefined))).resolves.toBe('left');
    });
    // Phase 1 has no subscription endpoint to leave, so this must not fire
    // ANY delete request — least of all the bare owner-delete endpoint.
    // (A hardcoded 'http://localhost/api/kb/kb-1' literal here would never
    // match the real API_BASE_URL in this test environment regardless of
    // what the hook does, making the assertion vacuously true — see fix
    // report.)
    expect(del).not.toHaveBeenCalled();
  });

  it('exposes removing=true only while a removal is in flight', async () => {
    let resolveConfirm: (v: boolean) => void = () => {};
    showConfirm.mockImplementation(() => new Promise<boolean>(resolve => { resolveConfirm = resolve; }));
    vi.spyOn(axios, 'delete').mockResolvedValue({ status: 204 });

    const { result } = renderHook(() => useKbRemoval(), { wrapper });
    expect(result.current.removing).toBe(false);

    let pending!: Promise<string>;
    act(() => { pending = result.current.removeKb(kb('owner')); });
    await waitFor(() => expect(result.current.removing).toBe(true));

    await act(async () => {
      resolveConfirm(true);
      await pending;
    });
    expect(result.current.removing).toBe(false);
  });

  it('guards against a second removeKb call while one is already in flight', async () => {
    let resolveConfirm: (v: boolean) => void = () => {};
    showConfirm.mockImplementation(() => new Promise<boolean>(resolve => { resolveConfirm = resolve; }));
    const del = vi.spyOn(axios, 'delete').mockResolvedValue({ status: 204 });

    const { result } = renderHook(() => useKbRemoval(), { wrapper });

    // A double-click on the delete button fires two calls before the first
    // one's confirmation dialog has even resolved. The second must not open
    // a second dialog or fire a second request — it should be a no-op that
    // resolves 'cancelled', not a raced second removal.
    let first!: Promise<string>;
    let second!: Promise<string>;
    act(() => {
      first = result.current.removeKb(kb('owner'));
      second = result.current.removeKb(kb('owner'));
    });

    await act(async () => {
      resolveConfirm(true);
      await Promise.all([first, second]);
    });

    await expect(first).resolves.toBe('deleted');
    await expect(second).resolves.toBe('cancelled');
    expect(showConfirm).toHaveBeenCalledTimes(1);
    expect(del).toHaveBeenCalledTimes(1);
  });
});
