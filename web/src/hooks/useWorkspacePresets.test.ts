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

  it('ruft setState nicht mehr auf, nachdem der Hook vor der Antwort demontiert wurde', async () => {
    const pending = deferred<{ data: typeof presetsA }>();
    vi.spyOn(axios, 'get').mockReturnValueOnce(pending.promise);
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {});

    const { unmount } = renderHook(() => useWorkspacePresets('kb1', 'de'));
    unmount();

    // If the effect's cleanup didn't guard the .then with `cancelled`, this
    // would trigger React's "Can't perform a React state update on an
    // unmounted component" warning via console.error.
    await act(async () => { pending.resolve({ data: presetsA }); });

    expect(errorSpy).not.toHaveBeenCalled();
  });

  it('degradiert bei einem Fehler auf leere Listen', async () => {
    vi.spyOn(axios, 'get').mockRejectedValue(new Error('network'));

    const { result } = renderHook(() => useWorkspacePresets('kb1', 'de'));

    await waitFor(() => expect(result.current).toEqual(EMPTY));
  });
});
