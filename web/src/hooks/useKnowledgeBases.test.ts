import { describe, it, expect, vi, beforeEach } from 'vitest';
import { renderHook, act, waitFor } from '@testing-library/react';
import axios from 'axios';
import type { MouseEvent } from 'react';
import { useKnowledgeBases } from './useKnowledgeBases';
import type { KnowledgeBase } from '../types';

vi.mock('axios');
const mockedAxios = vi.mocked(axios, true);

// removeKb's own delete-vs-leave-vs-unsubscribe decision is useKbRemoval's
// job (covered by useKbRemoval.test.ts); here it's mocked so these tests can
// drive handleDeleteKB purely through its return value ('deleted' / 'left' /
// 'unsubscribed' / 'cancelled').
const removeKb = vi.fn();
vi.mock('./useKbRemoval', () => ({
  useKbRemoval: () => ({ removeKb, removing: false }),
}));

vi.mock('../contexts/ThemeContext', () => ({
  useTheme: () => ({ t: (key: string) => key }),
}));

const showConfirm = vi.fn();
const showPrompt = vi.fn();
vi.mock('../contexts/ModalContext', () => ({
  useModalContext: () => ({ showConfirm, showPrompt }),
}));

const toastError = vi.fn();
vi.mock('../contexts/ToastContext', () => ({
  useToast: () => ({ error: toastError, success: vi.fn(), info: vi.fn(), warning: vi.fn() }),
}));

const fakeEvent = { stopPropagation: vi.fn() } as unknown as MouseEvent;

const personalKb: KnowledgeBase = {
  id: 'kb-1',
  name: 'Handbuch',
  description: null,
  userId: 'owner-1',
  createdAt: '2026-01-01T00:00:00Z',
  isPro: false,
  aiConfigId: null,
  chatModel: null,
  embeddingModel: null,
  rerankModel: null,
  ttsModel: null,
  myRole: 'owner',
};

const subscribedGlobalKb: KnowledgeBase = {
  id: 'kb-2',
  name: 'Katalog-KB',
  description: null,
  userId: null,
  createdAt: '2026-01-01T00:00:00Z',
  isPro: false,
  aiConfigId: null,
  chatModel: null,
  embeddingModel: null,
  rerankModel: null,
  ttsModel: null,
  isGlobal: true,
  isPublished: true,
  visibility: 'public',
};

const handleGoHome = vi.fn();

const setCurrentKb = vi.fn();

function setup() {
  return renderHook(() => useKnowledgeBases({
    onKBSelected: vi.fn(),
    handleGoHome,
    setCurrentKb,
    setIsPro: vi.fn(),
    setKbView: vi.fn(),
    setView: vi.fn(),
    setSelectedContent: vi.fn(),
    setGeneratedContent: vi.fn(),
  }));
}

describe('useKnowledgeBases handleDeleteKB', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  // Pins the reviewer-identified gap directly: nothing previously exercised
  // useKnowledgeBases' reaction to a removal outcome. The old implementation
  // relied entirely on the async fetchKBs() reconciliation to update local
  // state, so if that refetch never resolves (fetchKBs swallows its own
  // errors and never rethrows — a slow or failed network leaves it hanging
  // from the caller's perspective), the just-removed KB stayed on screen.
  // The fix splices both lists synchronously first, then still reconciles.
  it('removes the KB from local state before the reconciling refetch resolves, and still triggers the refetch', async () => {
    let resolveGet!: (v: { data: KnowledgeBase[] }) => void;
    mockedAxios.get.mockImplementation(() => new Promise((resolve) => { resolveGet = resolve; }));
    removeKb.mockResolvedValue('deleted');

    const { result } = setup();
    act(() => { result.current.setKbs([personalKb]); });

    act(() => { void result.current.handleDeleteKB(personalKb, fakeEvent); });

    // The KB must be gone from local state even though the GET requests
    // fetchKBs fired are still pending — proves removal doesn't depend on
    // the refetch completing.
    await waitFor(() => expect(result.current.kbs).toEqual([]));
    expect(mockedAxios.get).toHaveBeenCalled();

    // Drain the pending refetch so it doesn't dangle past the test.
    await act(async () => { resolveGet({ data: [] }); await Promise.resolve(); });
  });

  // The latent bug the reviewer flagged the refetch-only version as fixing:
  // a KB living in globalKbs (a subscription) must be spliced out of
  // globalKbs, not kbs — splicing the wrong array would leave a stale card.
  it('removes a subscribed KB from globalKbs, not kbs', async () => {
    mockedAxios.get.mockResolvedValue({ data: [] });
    removeKb.mockResolvedValue('unsubscribed');

    const { result } = setup();
    act(() => { result.current.setGlobalKbs([subscribedGlobalKb]); });

    await act(async () => {
      await result.current.handleDeleteKB(subscribedGlobalKb, fakeEvent);
    });

    expect(result.current.globalKbs).toEqual([]);
    expect(mockedAxios.get).toHaveBeenCalled();
  });

  // Navigation must not wait on the network. Deleting the KB you are currently
  // viewing used to await fetchKBs() first, parking the caller on the deleted
  // KB's view for the whole round trip — and fetchKBs never rejects (it
  // swallows into console.error + a toast), so a hanging network stranded them
  // there indefinitely.
  it('navigates home before the reconciling refetch resolves', async () => {
    let resolveGet!: (v: { data: KnowledgeBase[] }) => void;
    mockedAxios.get.mockImplementation(() => new Promise((resolve) => { resolveGet = resolve; }));
    removeKb.mockResolvedValue('deleted');

    const { result } = setup();
    act(() => { result.current.setKbs([personalKb]); });

    act(() => { void result.current.handleDeleteKB(personalKb, fakeEvent); });

    await waitFor(() => expect(handleGoHome).toHaveBeenCalledTimes(1));
    // Die GETs stehen zu diesem Zeitpunkt noch aus — genau das ist der Punkt.
    expect(mockedAxios.get).toHaveBeenCalled();

    await act(async () => { resolveGet({ data: [] }); await Promise.resolve(); });
  });

  it('leaves local state untouched when the removal is cancelled', async () => {
    removeKb.mockResolvedValue('cancelled');

    const { result } = setup();
    act(() => { result.current.setKbs([personalKb]); });

    await act(async () => {
      await result.current.handleDeleteKB(personalKb, fakeEvent);
    });

    expect(result.current.kbs).toEqual([personalKb]);
    expect(mockedAxios.get).not.toHaveBeenCalled();
  });
});

describe('useKnowledgeBases handleRenameKB', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('prompts with the current name, PATCHes the trimmed new name and updates the list', async () => {
    showPrompt.mockResolvedValue('  Neues Handbuch ');
    const renamed = { ...personalKb, name: 'Neues Handbuch' };
    mockedAxios.patch.mockResolvedValue({ data: renamed });

    const { result } = setup();
    act(() => { result.current.setKbs([personalKb]); });

    await act(async () => { await result.current.handleRenameKB(personalKb, fakeEvent); });

    expect(fakeEvent.stopPropagation).toHaveBeenCalled();
    expect(showPrompt).toHaveBeenCalledWith('renameKbPrompt', 'Handbuch');
    expect(mockedAxios.patch).toHaveBeenCalledWith(expect.stringMatching(/\/api\/kb\/kb-1$/), { name: 'Neues Handbuch' });
    expect(result.current.kbs).toEqual([renamed]);
  });

  it('propagates the renamed row into the current KB only when it is the same KB', async () => {
    showPrompt.mockResolvedValue('Neu');
    const renamed = { ...personalKb, name: 'Neu' };
    mockedAxios.patch.mockResolvedValue({ data: renamed });

    const { result } = setup();
    await act(async () => { await result.current.handleRenameKB(personalKb); });

    // setCurrentKb is React's state setter; the hook must use the functional
    // form so a rename from the Home card of a KB that is NOT open never
    // hijacks currentKb.
    expect(setCurrentKb).toHaveBeenCalledTimes(1);
    const updater = setCurrentKb.mock.calls[0][0] as (prev: KnowledgeBase | null) => KnowledgeBase | null;
    expect(typeof updater).toBe('function');
    expect(updater(null)).toBeNull();
    expect(updater(subscribedGlobalKb)).toBe(subscribedGlobalKb);
    expect(updater(personalKb)).toEqual(renamed);
  });

  it('updates a public KB in globalKbs', async () => {
    showPrompt.mockResolvedValue('Katalog 2');
    const renamed = { ...subscribedGlobalKb, name: 'Katalog 2' };
    mockedAxios.patch.mockResolvedValue({ data: renamed });

    const { result } = setup();
    act(() => { result.current.setGlobalKbs([subscribedGlobalKb]); });

    await act(async () => { await result.current.handleRenameKB(subscribedGlobalKb); });

    expect(result.current.globalKbs).toEqual([renamed]);
  });

  it.each([null, '', '   ', 'Handbuch', ' Handbuch '])('does nothing when the prompt yields %j', async (answer) => {
    showPrompt.mockResolvedValue(answer);

    const { result } = setup();
    act(() => { result.current.setKbs([personalKb]); });

    await act(async () => { await result.current.handleRenameKB(personalKb); });

    expect(mockedAxios.patch).not.toHaveBeenCalled();
    expect(result.current.kbs).toEqual([personalKb]);
  });

  it('toasts and leaves the list untouched when the PATCH fails', async () => {
    showPrompt.mockResolvedValue('Neu');
    mockedAxios.patch.mockRejectedValue(new Error('403'));

    const { result } = setup();
    act(() => { result.current.setKbs([personalKb]); });

    await act(async () => { await result.current.handleRenameKB(personalKb); });

    expect(toastError).toHaveBeenCalledWith('kbRenameError');
    expect(result.current.kbs).toEqual([personalKb]);
    expect(setCurrentKb).not.toHaveBeenCalled();
  });
});
