import { describe, it, expect } from 'vitest';
import { motion } from 'framer-motion';

// Guards an invariant of the framer-motion stub in setup.ts. React identifies
// elements by type, so `motion.div` must be the *same* component across
// renders. When it isn't, every re-render remounts the whole motion subtree and
// any DOM node held across a state update is silently detached — which showed
// up as a CI-only "element could not be found in the document" in Login.test.tsx
// rather than as an obvious failure here.
describe('framer-motion test stub', () => {
    it('returns a stable component per tag', () => {
        expect(motion.div).toBe(motion.div);
        expect(motion.span).toBe(motion.span);
    });

    it('returns distinct components for distinct tags', () => {
        expect(motion.div).not.toBe(motion.span);
    });
});
