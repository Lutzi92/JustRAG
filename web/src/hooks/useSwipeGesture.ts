import { useRef, useCallback, type TouchEvent } from 'react';

interface SwipeHandlers {
    onTouchStart: (e: TouchEvent) => void;
    onTouchEnd: (e: TouchEvent) => void;
}

/**
 * Detects horizontal swipe gestures. Calls onSwipeLeft / onSwipeRight
 * when the user swipes beyond the threshold distance and the gesture
 * is more horizontal than vertical (to avoid hijacking scrolls).
 */
export function useSwipeGesture(
    onSwipeLeft: () => void,
    onSwipeRight: () => void,
    threshold = 50,
): SwipeHandlers {
    const startX = useRef(0);
    const startY = useRef(0);

    const onTouchStart = useCallback((e: TouchEvent) => {
        startX.current = e.touches[0].clientX;
        startY.current = e.touches[0].clientY;
    }, []);

    const onTouchEnd = useCallback((e: TouchEvent) => {
        const dx = e.changedTouches[0].clientX - startX.current;
        const dy = e.changedTouches[0].clientY - startY.current;

        // Only trigger if horizontal movement exceeds vertical (not a scroll)
        if (Math.abs(dx) > Math.abs(dy) && Math.abs(dx) > threshold) {
            if (dx < 0) onSwipeLeft();
            else onSwipeRight();
        }
    }, [onSwipeLeft, onSwipeRight, threshold]);

    return { onTouchStart, onTouchEnd };
}
