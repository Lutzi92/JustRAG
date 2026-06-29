import { useEffect, useRef, type RefObject } from 'react';
import { createPortal } from 'react-dom';
import { useAnchoredPosition, type AnchorAlign, type AnchorPlacement } from '../hooks/useAnchoredPosition';

interface AnchoredPopoverProps {
    open: boolean;
    /** The button the popover is anchored to. */
    triggerRef: RefObject<HTMLElement | null>;
    onClose: () => void;
    align?: AnchorAlign;
    placement?: AnchorPlacement;
    /** Fixed popover width in px. Omit to size to content. */
    width?: number;
    role?: 'menu' | 'dialog' | 'tooltip';
    ariaLabel?: string;
    children: React.ReactNode;
}

/**
 * AnchoredPopover renders its children into a body-level portal positioned
 * against `triggerRef` via useAnchoredPosition (open inward + clamp to viewport
 * + caret). The portal escapes the virtualized message list's transforms and
 * any ancestor overflow:hidden — the root cause of the §6 clipping. It owns
 * outside-click and Escape dismissal so callers only manage the `open` flag.
 *
 * This is the single shared positioner for every message popover: §6 feedback
 * comment box, the answer export menu, and the §8 citation source preview.
 */
export function AnchoredPopover({
    open,
    triggerRef,
    onClose,
    align = 'start',
    placement = 'bottom',
    width,
    role = 'dialog',
    ariaLabel,
    children,
}: AnchoredPopoverProps) {
    const popoverRef = useRef<HTMLDivElement>(null);
    const pos = useAnchoredPosition(triggerRef, popoverRef, open, { align, placement });

    useEffect(() => {
        if (!open) return;
        const onDown = (e: MouseEvent) => {
            const target = e.target as Node;
            if (popoverRef.current?.contains(target)) return;
            if (triggerRef.current?.contains(target)) return;
            onClose();
        };
        const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose(); };
        document.addEventListener('mousedown', onDown, true);
        document.addEventListener('keydown', onKey);
        return () => {
            document.removeEventListener('mousedown', onDown, true);
            document.removeEventListener('keydown', onKey);
        };
    }, [open, onClose, triggerRef]);

    if (!open) return null;

    const caretSize = 8;
    const caretBase: React.CSSProperties = {
        position: 'absolute',
        left: pos.caretLeft - caretSize / 2,
        width: caretSize,
        height: caretSize,
        background: 'var(--bg-primary)',
        transform: 'rotate(45deg)',
    };
    const caretStyle: React.CSSProperties = pos.placement === 'bottom'
        ? { ...caretBase, top: -caretSize / 2, borderLeft: '1px solid var(--border-color)', borderTop: '1px solid var(--border-color)' }
        : { ...caretBase, bottom: -caretSize / 2, borderRight: '1px solid var(--border-color)', borderBottom: '1px solid var(--border-color)' };

    return createPortal(
        // eslint-disable-next-line jsx-a11y/no-static-element-interactions, jsx-a11y/click-events-have-key-events
        <div
            ref={popoverRef}
            role={role}
            aria-label={ariaLabel}
            tabIndex={-1}
            onClick={(e) => e.stopPropagation()}
            onKeyDown={(e) => { if (e.key === 'Escape') onClose(); }}
            style={{
                ...pos.style,
                width,
                background: 'var(--bg-primary)',
                border: '1px solid var(--border-color)',
                borderRadius: '8px',
                boxShadow: 'var(--shadow-md)',
                zIndex: 4000,
                opacity: pos.ready ? 1 : 0,
            }}
        >
            <div aria-hidden="true" style={caretStyle} />
            {children}
        </div>,
        document.body,
    );
}
