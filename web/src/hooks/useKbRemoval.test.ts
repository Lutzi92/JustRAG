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
        confirmUnsubscribeKb: 'Unsubscribe from this KB?',
        confirmRemoveFavorite: 'Remove from favorites? Your chats are kept, and it stays under "Discover KBs".',
      };
      return strings[key] ?? key;
    },
  }),
}));

function wrapper({ children }: { children: ReactNode }) {
  return children;
}

// visibility/isPublished matter: the deployment-day shape is a curator holding
// kb_members.role='admin' on a PUBLISHED PUBLIC KB (migration 0064 turned every
// global_kb_editors row into 'admin', 0065 set auto_subscribe on every
// previously published global KB). The factory used to build private KBs only,
// which is why that shape had no coverage at all.
const kb = (myRole?: string, overrides: Partial<KnowledgeBase> = {}) =>
  ({ id: 'kb-1', name: 'Handbuch', myRole, ...overrides } as KnowledgeBase);

const publicKb = (myRole?: string) =>
  kb(myRole, { visibility: 'public', isPublished: true });

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

  // Ein Kurator (kb_members.role='admin') auf einer oeffentlichen KB: der
  // Stern in den Favoriten ist ein reiner Favoriten-Schalter. Frueher lief
  // dieser Fall in den Leave-Zweig — Mitgliedschaft weg, Chats weg, und weil
  // der Katalog damals nur veroeffentlichte KBs listete, war eine staged KB
  // danach ueberhaupt nicht mehr erreichbar.
  it('unsubscribes (never leaves) a curator on a public KB, keeping membership and chats', async () => {
    const get = vi.spyOn(axios, 'get');
    const del = vi.spyOn(axios, 'delete').mockResolvedValue({ status: 204 });
    showConfirm.mockResolvedValue(true);

    const { result } = renderHook(() => useKbRemoval(), { wrapper });
    await act(async () => {
      await expect(result.current.removeKb(publicKb('admin'))).resolves.toBe('unsubscribed');
    });

    expect(del).toHaveBeenCalledWith(expect.stringContaining('/api/kb/kb-1/subscription'));
    expect(del).not.toHaveBeenCalledWith(expect.stringContaining('/membership'));
    expect(del).toHaveBeenCalledTimes(1);
    // Der Chat-Zaehler ist hier bedeutungslos, weil keine Chats geloescht
    // werden — /membership/impact darf gar nicht erst abgefragt werden.
    expect(get).not.toHaveBeenCalled();
  });

  // Der Dialog muss sagen, was wirklich passiert: Chats bleiben, die KB bleibt
  // unter „KBs entdecken" auffindbar. Vorher stand dort die Leave-Warnung.
  it('confirms with the favorites wording, not the chat-loss warning', async () => {
    vi.spyOn(axios, 'delete').mockResolvedValue({ status: 204 });
    showConfirm.mockResolvedValue(true);

    const { result } = renderHook(() => useKbRemoval(), { wrapper });
    await act(async () => {
      await expect(result.current.removeKb(publicKb('admin'))).resolves.toBe('unsubscribed');
    });

    const message = showConfirm.mock.calls[0][0] as string;
    expect(message.toLowerCase()).toContain('favorites');
    expect(message.toLowerCase()).not.toContain('will be deleted');
  });

  // Defensiv: eine oeffentliche KB hat per Konstruktion keinen Owner (das
  // Veroeffentlichen degradiert ihn auf 'admin' und nullt user_id). Sollte
  // trotzdem je eine mit myRole='owner' hereinkommen, darf der Stern die KB
  // nicht loeschen — deshalb steht der public-Zweig VOR dem Owner-Zweig.
  it('never deletes a public KB from the favorites toggle, even with myRole=owner', async () => {
    const del = vi.spyOn(axios, 'delete').mockResolvedValue({ status: 204 });
    showConfirm.mockResolvedValue(true);

    const { result } = renderHook(() => useKbRemoval(), { wrapper });
    await act(async () => {
      await expect(result.current.removeKb(publicKb('owner'))).resolves.toBe('unsubscribed');
    });

    expect(del).toHaveBeenCalledWith(expect.stringContaining('/api/kb/kb-1/subscription'));
    expect(del).toHaveBeenCalledTimes(1);
  });

  // Ohne Chats darf der Dialog nicht "0 deiner Chats werden geloescht" sagen.
  it('drops the chat clause from the leave confirmation when there is nothing to lose', async () => {
    vi.spyOn(axios, 'get').mockResolvedValue({ data: { chatCount: 0 } });
    vi.spyOn(axios, 'delete').mockResolvedValue({ data: { deletedChats: 0 } });
    showConfirm.mockResolvedValue(true);

    const { result } = renderHook(() => useKbRemoval(), { wrapper });
    await act(async () => {
      await expect(result.current.removeKb(kb('admin'))).resolves.toBe('left');
    });

    const message = showConfirm.mock.calls[0][0] as string;
    expect(message).not.toContain('0');
    expect(message.toLowerCase()).not.toContain('chats will be deleted');
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

  it('unsubscribes on a 404 from /membership/impact, never leaves or deletes', async () => {
    vi.spyOn(axios, 'get').mockRejectedValue({ response: { status: 404 } });
    const del = vi.spyOn(axios, 'delete').mockResolvedValue({ status: 204 });
    showConfirm.mockResolvedValue(true);

    const { result } = renderHook(() => useKbRemoval(), { wrapper });
    // Keine kb_members-Zeile und keine 'public'-Sichtbarkeit auf der Karte:
    // der defensive Zweig. Ohne Mitgliedschaft gibt es nichts zu verlassen,
    // also faellt er auf Abbestellen zurueck statt Chats zu loeschen.
    await act(async () => {
      await expect(result.current.removeKb(kb(undefined))).resolves.toBe('unsubscribed');
    });
    // Unsubscribing deletes no chats and must hit the subscription endpoint
    // only — never /membership (that would delete chats) and never the bare
    // owner-delete endpoint.
    expect(del).toHaveBeenCalledWith(expect.stringContaining('/api/kb/kb-1/subscription'));
    expect(del).not.toHaveBeenCalledWith(expect.stringContaining('/membership'));
    expect(del).toHaveBeenCalledTimes(1);
    // The dialog must name the difference: no chat-loss warning here, unlike
    // the member-leave path above.
    const message = showConfirm.mock.calls[0][0] as string;
    expect(message.toLowerCase()).not.toContain('chat');
  });

  it('sends no request when the subscriber dismisses the unsubscribe confirmation', async () => {
    vi.spyOn(axios, 'get').mockRejectedValue({ response: { status: 404 } });
    const del = vi.spyOn(axios, 'delete');
    showConfirm.mockResolvedValue(false);

    const { result } = renderHook(() => useKbRemoval(), { wrapper });
    await act(async () => {
      await expect(result.current.removeKb(kb(undefined))).resolves.toBe('cancelled');
    });
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
