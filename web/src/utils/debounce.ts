export interface Debounced<A extends unknown[]> {
  (...args: A): void;
  cancel: () => void;
}

/**
 * debounce coalesces rapid calls into a single trailing call fired `waitMs`
 * after the last call. When `maxWaitMs` is given, the callback is guaranteed to
 * fire at least once per that window even during a continuous burst that never
 * goes quiet (so live-update streams don't stall indefinitely).
 */
export function debounce<A extends unknown[]>(
  fn: (...args: A) => void,
  waitMs: number,
  maxWaitMs?: number,
): Debounced<A> {
  let timer: ReturnType<typeof setTimeout> | null = null;
  let firstCallAt: number | null = null;
  let lastArgs: A | null = null;

  const fire = () => {
    if (timer) { clearTimeout(timer); timer = null; }
    firstCallAt = null;
    const args = lastArgs;
    lastArgs = null;
    if (args) fn(...args);
  };

  const debounced = (...args: A) => {
    lastArgs = args;
    const now = Date.now();
    if (firstCallAt === null) firstCallAt = now;
    if (timer) clearTimeout(timer);
    if (maxWaitMs != null && now - firstCallAt >= maxWaitMs) {
      fire();
      return;
    }
    const delay = maxWaitMs != null
      ? Math.min(waitMs, maxWaitMs - (now - firstCallAt))
      : waitMs;
    timer = setTimeout(fire, delay);
  };

  debounced.cancel = () => {
    if (timer) clearTimeout(timer);
    timer = null;
    firstCallAt = null;
    lastArgs = null;
  };

  return debounced;
}
