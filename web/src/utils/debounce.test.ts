import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { debounce } from './debounce';

describe('debounce', () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it('coalesces rapid calls into one trailing call with the latest args', () => {
    const fn = vi.fn();
    const d = debounce(fn, 100);
    d('a');
    d('b');
    d('c');
    expect(fn).not.toHaveBeenCalled();
    vi.advanceTimersByTime(100);
    expect(fn).toHaveBeenCalledTimes(1);
    expect(fn).toHaveBeenCalledWith('c');
  });

  it('does not fire before the quiet window elapses', () => {
    const fn = vi.fn();
    const d = debounce(fn, 100);
    d();
    vi.advanceTimersByTime(99);
    expect(fn).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1);
    expect(fn).toHaveBeenCalledTimes(1);
  });

  it('cancel() prevents a pending call', () => {
    const fn = vi.fn();
    const d = debounce(fn, 100);
    d();
    d.cancel();
    vi.advanceTimersByTime(200);
    expect(fn).not.toHaveBeenCalled();
  });

  it('fires within maxWait during a continuous burst that never goes quiet', () => {
    const fn = vi.fn();
    const d = debounce(fn, 100, 300);
    // Call every 50ms (never idle for the full 100ms quiet window) for 300ms.
    for (let i = 0; i < 6; i++) {
      d();
      vi.advanceTimersByTime(50);
    }
    // By 300ms of continuous churn, maxWait must have forced at least one call.
    expect(fn.mock.calls.length).toBeGreaterThanOrEqual(1);
  });
});
