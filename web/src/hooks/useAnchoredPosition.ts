import { useCallback, useLayoutEffect, useState, type RefObject } from 'react';

export type AnchorAlign = 'start' | 'end' | 'center';
export type AnchorPlacement = 'bottom' | 'top';

interface UseAnchoredPositionOptions {
    /** Gap in px between the trigger and the popover. Default 8. */
    gap?: number;
    /** Minimum gap in px to keep from any viewport edge. Default 8. */
    margin?: number;
    /**
     * Horizontal alignment of the popover relative to the trigger.
     * 'start' (default) left-aligns the popover with the trigger — i.e. it
     * opens toward the column center for a left-anchored action bar, which is
     * exactly what fixes the §6 clipping (a right-anchored popover on a
     * left-edge trigger extended leftward off-screen).
     */
    align?: AnchorAlign;
    /** Preferred vertical placement. Default 'bottom' (opens downward). */
    placement?: AnchorPlacement;
}

export interface AnchoredPosition {
    /** Ready means a measurement has happened; render the popover invisibly until then to measure it. */
    ready: boolean;
    /** position:fixed coordinates for the popover surface. */
    style: { position: 'fixed'; top: number; left: number };
    /** Where the caret sits relative to the popover, and which edge it points from. */
    caretLeft: number;
    placement: AnchorPlacement;
}

const INITIAL: AnchoredPosition = {
    ready: false,
    style: { position: 'fixed', top: -9999, left: -9999 },
    caretLeft: 0,
    placement: 'bottom',
};

function clamp(value: number, min: number, max: number): number {
    if (max < min) return min;
    return Math.min(Math.max(value, min), max);
}

/**
 * useAnchoredPosition computes fixed-viewport coordinates for a popover so it
 * (a) anchors to its trigger button, (b) opens inward toward the column center,
 * and (c) flips/shifts back inside the viewport with an 8px margin when it would
 * otherwise overflow any edge. It is the single clamping rule shared by every
 * message popover (§6 feedback comment, export menu, §8 citation preview).
 *
 * The popover should be rendered into a portal with the returned `style`
 * (position:fixed) so it escapes the virtualized message list's transforms and
 * any ancestor `overflow:hidden`.
 */
export function useAnchoredPosition(
    triggerRef: RefObject<HTMLElement | null>,
    popoverRef: RefObject<HTMLElement | null>,
    open: boolean,
    options: UseAnchoredPositionOptions = {},
): AnchoredPosition {
    const { gap = 8, margin = 8, align = 'start', placement: preferred = 'bottom' } = options;
    const [pos, setPos] = useState<AnchoredPosition>(INITIAL);

    const compute = useCallback(() => {
        const trigger = triggerRef.current;
        const popover = popoverRef.current;
        if (!trigger || !popover) return;

        const t = trigger.getBoundingClientRect();
        const p = popover.getBoundingClientRect();
        const vw = window.innerWidth;
        const vh = window.innerHeight;
        const popW = p.width;
        const popH = p.height;

        // Horizontal: align the popover edge to the trigger, then clamp inside.
        let left: number;
        if (align === 'end') left = t.right - popW;
        else if (align === 'center') left = t.left + t.width / 2 - popW / 2;
        else left = t.left;
        left = clamp(left, margin, vw - popW - margin);

        // Vertical: prefer below the trigger; flip above when it would overflow
        // the bottom and there is more room above.
        let placement: AnchorPlacement = preferred;
        let top: number;
        const below = t.bottom + gap;
        const above = t.top - gap - popH;
        if (preferred === 'bottom') {
            if (below + popH > vh - margin && t.top - gap - popH > margin) {
                placement = 'top';
                top = above;
            } else {
                top = below;
            }
        } else {
            if (above < margin && t.bottom + gap + popH < vh - margin) {
                placement = 'bottom';
                top = below;
            } else {
                top = above;
            }
        }
        top = clamp(top, margin, vh - popH - margin);

        // Caret points at the trigger center, clamped to stay on the popover.
        const triggerCenter = t.left + t.width / 2;
        const caretLeft = clamp(triggerCenter - left, 14, popW - 14);

        setPos({
            ready: true,
            style: { position: 'fixed', top, left },
            caretLeft,
            placement,
        });
    }, [triggerRef, popoverRef, gap, margin, align, preferred]);

    useLayoutEffect(() => {
        // No reset on close: AnchoredPopover unmounts the portal when closed, and
        // compute() re-measures before paint on the next open, so stale coords are
        // never shown. (Avoids a setState-in-effect on the close branch.)
        if (!open) return;
        compute();
        const onScroll = () => compute();
        const onResize = () => compute();
        // capture:true catches scrolls on any ancestor (the message list).
        window.addEventListener('scroll', onScroll, true);
        window.addEventListener('resize', onResize);
        return () => {
            window.removeEventListener('scroll', onScroll, true);
            window.removeEventListener('resize', onResize);
        };
    }, [open, compute]);

    return pos;
}
