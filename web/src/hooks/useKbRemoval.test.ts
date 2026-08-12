import { describe, it, expect, vi, beforeEach } from 'vitest';
import { renderHook } from '@testing-library/react';
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
    await expect(result.current.removeKb(kb('owner'))).resolves.toBe('deleted');

    expect(del).toHaveBeenCalledWith(expect.stringContaining('/api/kb/kb-1'));
    expect(del).not.toHaveBeenCalledWith(expect.stringContaining('/membership'));
  });

  it('leaves the KB and reports the chat count for a non-owner', async () => {
    vi.spyOn(axios, 'get').mockResolvedValue({ data: { chatCount: 3 } });
    const del = vi.spyOn(axios, 'delete').mockResolvedValue({ data: { deletedChats: 3 } });
    showConfirm.mockResolvedValue(true);

    const { result } = renderHook(() => useKbRemoval(), { wrapper });
    await expect(result.current.removeKb(kb('edit'))).resolves.toBe('left');

    // Der Dialog muss die Zahl nennen — sonst loescht der Nutzer blind Chats.
    expect(showConfirm).toHaveBeenCalledWith(expect.stringContaining('3'));
    expect(del).toHaveBeenCalledWith(expect.stringContaining('/api/kb/kb-1/membership'));
  });

  it('sends no request when the confirmation is dismissed', async () => {
    const del = vi.spyOn(axios, 'delete');
    showConfirm.mockResolvedValue(false);

    const { result } = renderHook(() => useKbRemoval(), { wrapper });
    await expect(result.current.removeKb(kb('owner'))).resolves.toBe('cancelled');
    expect(del).not.toHaveBeenCalled();
  });

  it('treats an implicit viewer as leave, never delete', async () => {
    vi.spyOn(axios, 'get').mockRejectedValue({ response: { status: 404 } });
    const del = vi.spyOn(axios, 'delete').mockResolvedValue({ data: { deletedChats: 0 } });
    showConfirm.mockResolvedValue(true);

    const { result } = renderHook(() => useKbRemoval(), { wrapper });
    // myRole ist undefined: implizite view-Rolle auf einer globalen KB.
    await expect(result.current.removeKb(kb(undefined))).resolves.toBe('left');
    expect(del).not.toHaveBeenCalledWith('http://localhost/api/kb/kb-1');
  });
});
