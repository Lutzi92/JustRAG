import '@testing-library/jest-dom/vitest';
import { cleanup } from '@testing-library/react';
import { afterEach, vi } from 'vitest';

afterEach(() => {
  cleanup();
});

// Mock framer-motion to avoid jsdom issues with animations
vi.mock('framer-motion', async () => {
  const { forwardRef, createElement } = await vi.importActual<typeof import('react')>('react');

  // framer-motion-specific props that must be stripped before rendering the
  // plain HTML element, so they don't leak onto the DOM node.
  const MOTION_PROPS = [
    'initial', 'animate', 'exit', 'transition', 'variants',
    'whileHover', 'whileTap', 'whileFocus', 'whileDrag', 'whileInView',
    'layout', 'layoutId',
  ];

  return {
    motion: new Proxy({}, {
      get: (_target, prop: string) => {
        // Return a forwardRef component that renders the HTML element
        return forwardRef((props: Record<string, unknown>, ref: unknown) => {
          const rest = Object.fromEntries(
            Object.entries(props).filter(([k]) => !MOTION_PROPS.includes(k))
          );
          return createElement(prop, { ...rest, ref });
        });
      },
    }),
    AnimatePresence: ({ children }: { children: React.ReactNode }) => children,
  };
});
