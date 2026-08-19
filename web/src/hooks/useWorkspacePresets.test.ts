import { describe, it, expect, vi, beforeEach } from 'vitest';
import { renderHook, waitFor, act } from '@testing-library/react';
import axios from 'axios';
import { useWorkspacePresets } from './useWorkspacePresets';

const EMPTY = { analysis: [], comparison: [], compareEnabled: false };
const presetsA = { analysis: [{ label: 'A', prompt: 'Frage A' }], comparison: [], compareEnabled: false };
const presetsB = { analysis: [{ label: 'B', prompt: 'Frage B' }], comparison: [], compareEnabled: true };

/** A promise the test resolves by hand, to hold the hook mid-fetch. */
function deferred<T>() {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>((res) => { resolve = res; });
  return { promise, resolve };
}

/** Lets a real unhandled-rejection check (if any) surface before we assert on it. */
function flushMacrotask() {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

describe('useWorkspacePresets', () => {
  beforeEach(() => {
    vi.restoreAllMocks();
  });

  it('lädt die Presets der KB', async () => {
    vi.spyOn(axios, 'get').mockResolvedValue({ data: presetsA });

    const { result } = renderHook(() => useWorkspacePresets('kb1', 'de'));

    await waitFor(() => expect(result.current).toEqual(presetsA));
  });

  it('setzt den Zustand zurück, sobald sich die kbId ändert, statt die alte KB durchscheinen zu lassen', async () => {
    const pendingB = deferred<{ data: typeof presetsB }>();
    const get = vi.spyOn(axios, 'get')
      .mockResolvedValueOnce({ data: presetsA })
      .mockReturnValueOnce(pendingB.promise);

    const { result, rerender } = renderHook(
      ({ kbId }) => useWorkspacePresets(kbId, 'de'),
      { initialProps: { kbId: 'kb-a' } },
    );
    await waitFor(() => expect(result.current).toEqual(presetsA));

    rerender({ kbId: 'kb-b' });
    // kb-b's fetch is still in flight — kb-a's data must not leak into kb-b's
    // slot while the request settles (mirrors the identical leakage guard on
    // useKbAgents, which resets on every kbId change for the same reason).
    expect(result.current).toEqual(EMPTY);

    await act(async () => { pendingB.resolve({ data: presetsB }); });
    await waitFor(() => expect(result.current).toEqual(presetsB));
    expect(get).toHaveBeenCalledTimes(2);
  });

  it('ignoriert eine verspätet eintreffende Antwort für eine bereits verlassene kbId', async () => {
    // A's request is still in flight when the caller switches to B; A's
    // response then lands *after* B's — the out-of-order case the `cancelled`
    // closure exists for. If this were unguarded, A's stale data would
    // silently overwrite B's, and nobody would report it as a bug — they'd
    // just wonder why the wrong KB's presets showed up.
    const pendingA = deferred<{ data: typeof presetsA }>();
    const pendingB = deferred<{ data: typeof presetsB }>();
    vi.spyOn(axios, 'get')
      .mockReturnValueOnce(pendingA.promise)
      .mockReturnValueOnce(pendingB.promise);

    const { result, rerender } = renderHook(
      ({ kbId }) => useWorkspacePresets(kbId, 'de'),
      { initialProps: { kbId: 'kb-a' } },
    );
    rerender({ kbId: 'kb-b' });

    // B's response arrives first.
    await act(async () => { pendingB.resolve({ data: presetsB }); });
    await waitFor(() => expect(result.current).toEqual(presetsB));

    // A's response arrives late, for a kbId the hook has already left.
    await act(async () => { pendingA.resolve({ data: presetsA }); });
    expect(result.current).toEqual(presetsB);
  });

  it('degradiert bei einem Fehler auf leere Listen, ohne die Ablehnung entkommen zu lassen', async () => {
    vi.spyOn(axios, 'get').mockRejectedValue(new Error('network'));
    const unhandledRejection = vi.fn();
    process.on('unhandledRejection', unhandledRejection);

    try {
      const { result } = renderHook(() => useWorkspacePresets('kb1', 'de'));
      await waitFor(() => expect(result.current).toEqual(EMPTY));
      // Node only fires 'unhandledRejection' once the microtask queue has
      // drained without a handler attached — give it a macrotask to do so
      // before asserting it never fired.
      await flushMacrotask();
      expect(unhandledRejection).not.toHaveBeenCalled();
    } finally {
      process.off('unhandledRejection', unhandledRejection);
    }
  });
});
