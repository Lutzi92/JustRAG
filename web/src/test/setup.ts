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

  // One component per tag, created once. React identifies elements by their
  // type, so handing back a fresh forwardRef on every property access makes
  // every `motion.div` in a re-render a *different* type — React then unmounts
  // the old subtree and mounts a new one on each render. Anything holding a DOM
  // node across a state update (an element a test just queried, a focused
  // input, scroll position) silently loses it, which surfaced as a CI-only
  // "element could not be found in the document" in Login.test.tsx.
  const componentsByTag = new Map<string | symbol, unknown>();

  return {
    motion: new Proxy({}, {
      get: (_target, prop: string) => {
        const cached = componentsByTag.get(prop);
        if (cached) return cached;

        // A forwardRef component that renders the plain HTML element.
        const Component = forwardRef((props: Record<string, unknown>, ref: unknown) => {
          const rest = Object.fromEntries(
            Object.entries(props).filter(([k]) => !MOTION_PROPS.includes(k))
          );
          return createElement(prop, { ...rest, ref });
        });
        Component.displayName = `motion.${String(prop)}`;
        componentsByTag.set(prop, Component);
        return Component;
      },
    }),
    AnimatePresence: ({ children }: { children: React.ReactNode }) => children,
  };
});
