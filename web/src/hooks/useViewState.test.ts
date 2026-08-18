import { describe, it, expect, vi, beforeEach } from 'vitest';
import { renderHook, act } from '@testing-library/react';
import type { TouchEvent as ReactTouchEvent } from 'react';
import { useViewState } from './useViewState';

// Minimal fake touch events matching what useSwipeGesture reads
// (touches[0].clientX/clientY on start, changedTouches[0].clientX/clientY on
// end). threshold is 50px and horizontal movement must exceed vertical.
function touchStart(x: number): ReactTouchEvent {
  return { touches: [{ clientX: x, clientY: 0 }] } as unknown as ReactTouchEvent;
}
function touchEnd(x: number): ReactTouchEvent {
  return { changedTouches: [{ clientX: x, clientY: 0 }] } as unknown as ReactTouchEvent;
}

function setup() {
  const setView = vi.fn();
  const setKbView = vi.fn();
  const setShowSettings = vi.fn();
  const { result } = renderHook(() => useViewState({ setView, setKbView, setShowSettings }));
  return { result, setKbView };
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
});
