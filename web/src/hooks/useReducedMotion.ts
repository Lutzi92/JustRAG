import { useState, useEffect } from 'react';

export function useReducedMotion(): boolean {
  const [reduced, setReduced] = useState(false);

  useEffect(() => {
    if (typeof window === 'undefined') return;

    const mq = window.matchMedia('(prefers-reduced-motion: reduce)');
    setReduced(mq.matches);
    const handler = (e: MediaQueryListEvent) => setReduced(e.matches);
    mq.addEventListener('change', handler);
    return () => mq.removeEventListener('change', handler);
  }, []);

  return reduced;
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
