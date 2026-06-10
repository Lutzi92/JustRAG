import { useSyncExternalStore } from 'react';

const QUERY = '(prefers-reduced-motion: reduce)';

function subscribe(callback: () => void): () => void {
  const mq = window.matchMedia(QUERY);
  mq.addEventListener('change', callback);
  return () => mq.removeEventListener('change', callback);
}

function getSnapshot(): boolean {
  return window.matchMedia(QUERY).matches;
}

function getServerSnapshot(): boolean {
  return false;
}

export function useReducedMotion(): boolean {
  return useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot);
}

const noMotion = {
  initial: false as const,
  animate: false as const,
  exit: undefined,
  transition: { duration: 0 },
};

/**
 * Returns Framer Motion props that disable animation when reduced motion is preferred.
 * Spread onto a motion.div: `<motion.div {...getMotionProps(reduced)} initial={...} animate={...}>`
 * When reduced is true, overrides initial/animate/exit/transition to skip animation.
 */
export function getMotionProps(reduced: boolean) {
  return reduced ? noMotion : {};
}
