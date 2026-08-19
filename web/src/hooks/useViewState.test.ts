import { describe, it, expect, vi, beforeEach } from 'vitest';
import { renderHook, act } from '@testing-library/react';
import { useState } from 'react';
import type { Dispatch, SetStateAction, TouchEvent as ReactTouchEvent } from 'react';
import { useViewState, type KbViewType } from './useViewState';

// Minimal fake touch events matching what useSwipeGesture reads
// (touches[0].clientX/clientY on start, changedTouches[0].clientX/clientY on
// end). threshold is 50px and horizontal movement must exceed vertical.
function touchStart(x: number): ReactTouchEvent {
  return { touches: [{ clientX: x, clientY: 0 }] } as unknown as ReactTouchEvent;
}
function touchEnd(x: number): ReactTouchEvent {
  return { changedTouches: [{ clientX: x, clientY: 0 }] } as unknown as ReactTouchEvent;
}

// Backs `kbView` with real React state (like AuthenticatedApp does), not a
// bare vi.fn() spy — useViewState's swipe handlers now read `kbView` on every
// render to derive the swipe's starting tab (see `deriveActiveMobileTab`), so
// a call to `setKbView` has to actually feed back into the next render for
// the multi-swipe tests below to track real app behavior. `setKbViewSpy`
// still lets tests assert on individual calls the way the plain vi.fn() did.
function setup(initialKbView: KbViewType = 'chat') {
  const setView = vi.fn();
  const setKbViewSpy = vi.fn();
  const setShowSettings = vi.fn();
  const { result } = renderHook(() => {
    const [kbView, setKbViewState] = useState<KbViewType>(initialKbView);
    const setKbView: Dispatch<SetStateAction<KbViewType>> = (v) => {
      setKbViewSpy(v);
      setKbViewState(v);
    };
    return useViewState({ setView, kbView, setKbView, setShowSettings });
  });
  return { result, setKbView: setKbViewSpy };
}

// dx = end.x - start.x. dx < 0 (drag left) triggers onSwipeLeft, which in
// useViewState advances TAB_ORDER forward. dx > 0 (drag right) triggers
// onSwipeRight, which moves backward. TAB_ORDER = [history, chat, workspace, files].
function swipeLeft(result: ReturnType<typeof setup>['result']) {
  act(() => {
    result.current.swipeHandlers.onTouchStart(touchStart(200));
    result.current.swipeHandlers.onTouchEnd(touchEnd(100));
  });
}
function swipeRight(result: ReturnType<typeof setup>['result']) {
  act(() => {
    result.current.swipeHandlers.onTouchStart(touchStart(100));
    result.current.swipeHandlers.onTouchEnd(touchEnd(200));
  });
}

describe('useViewState swipe navigation', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('startet auf "chat"', () => {
    const { result } = setup();
    expect(result.current.mobileTab).toBe('chat');
  });

  it('Swipe rechts von chat landet auf history (kein setKbView-Aufruf)', () => {
    const { result, setKbView } = setup();
    swipeRight(result);
    expect(result.current.mobileTab).toBe('history');
    expect(setKbView).not.toHaveBeenCalled();
  });

  it('Swipe links von chat landet auf workspace und ruft setKbView("workspace")', () => {
    const { result, setKbView } = setup();
    swipeLeft(result);
    expect(result.current.mobileTab).toBe('workspace');
    expect(setKbView).toHaveBeenCalledWith('workspace');
  });

  it('Swipe links erneut landet auf files, OHNE kbView von "workspace" wegzubewegen', () => {
    const { result, setKbView } = setup();
    swipeLeft(result); // chat -> workspace
    expect(result.current.mobileTab).toBe('workspace');
    expect(setKbView).toHaveBeenCalledWith('workspace');

    setKbView.mockClear();
    swipeLeft(result); // workspace -> files
    expect(result.current.mobileTab).toBe('files');
    expect(setKbView).not.toHaveBeenCalled();
  });

  it('Swipe rechts von workspace kehrt zu chat zurück und ruft setKbView("chat")', () => {
    const { result, setKbView } = setup();
    swipeLeft(result); // chat -> workspace
    expect(result.current.mobileTab).toBe('workspace');

    setKbView.mockClear();
    swipeRight(result); // workspace -> chat
    expect(result.current.mobileTab).toBe('chat');
    expect(setKbView).toHaveBeenCalledWith('chat');
  });

  it('Swipe rechts an der Grenze "history" ist ein No-op', () => {
    const { result, setKbView } = setup();
    swipeRight(result); // chat -> history
    expect(result.current.mobileTab).toBe('history');

    setKbView.mockClear();
    swipeRight(result); // history -> history (Grenze)
    expect(result.current.mobileTab).toBe('history');
    expect(setKbView).not.toHaveBeenCalled();
  });

  it('Swipe links an der Grenze "files" ist ein No-op', () => {
    const { result, setKbView } = setup();
    swipeLeft(result); // chat -> workspace
    swipeLeft(result); // workspace -> files
    expect(result.current.mobileTab).toBe('files');

    setKbView.mockClear();
    swipeLeft(result); // files -> files (Grenze)
    expect(result.current.mobileTab).toBe('files');
    expect(setKbView).not.toHaveBeenCalled();
  });

  // Fix wave item 4: ChatView's own Workspace tab (icon-only on mobile) calls
  // setKbView('workspace') directly, bypassing applyTab — so mobileTab can
  // stay at its default 'chat' while kbView is already 'workspace'. Before
  // this fix, swipeLeft/swipeRight indexed TAB_ORDER by the stale mobileTab
  // ('chat') instead of the displayed tab ('workspace'), leaving a left swipe
  // dead (it "advanced" to the already-shown workspace tab) and a right swipe
  // skipping straight past chat to history.
  describe('driftete mobileTab/kbView (ChatViews eigener Workspace-Tab)', () => {
    it('Swipe links folgt dem angezeigten Tab (workspace) statt dem veralteten mobileTab (chat)', () => {
      const { result, setKbView } = setup('workspace');
      expect(result.current.mobileTab).toBe('chat'); // roher State, gedriftet

      swipeLeft(result);
      expect(result.current.mobileTab).toBe('files');
      expect(setKbView).not.toHaveBeenCalled();
    });

    it('Swipe rechts kehrt von workspace zu chat zurück, statt chat zu überspringen', () => {
      const { result, setKbView } = setup('workspace');

      swipeRight(result);
      expect(result.current.mobileTab).toBe('chat');
      expect(setKbView).toHaveBeenCalledWith('chat');
    });
  });
});
