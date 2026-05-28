import '@testing-library/jest-dom/vitest';
import { cleanup } from '@testing-library/react';
import { afterEach, vi } from 'vitest';

afterEach(() => {
  cleanup();
});

// Mock framer-motion to avoid jsdom issues with animations
vi.mock('framer-motion', () => ({
  motion: new Proxy({}, {
    get: (_target, prop: string) => {
      // Return a forwardRef component that renders the HTML element
      const { forwardRef } = require('react');
      return forwardRef((props: Record<string, unknown>, ref: unknown) => {
        // Strip framer-motion-specific props
        const {
          initial: _initial,
          animate: _animate,
          exit: _exit,
          transition: _transition,
          variants: _variants,
          whileHover: _whileHover,
          whileTap: _whileTap,
          whileFocus: _whileFocus,
          whileDrag: _whileDrag,
          whileInView: _whileInView,
          layout: _layout,
          layoutId: _layoutId,
          ...rest
        } = props;
        const { createElement } = require('react');
        return createElement(prop, { ...rest, ref });
      });
    },
  }),
  AnimatePresence: ({ children }: { children: React.ReactNode }) => children,
}));
