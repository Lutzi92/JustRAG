import { useCallback, useState } from 'react';

/**
 * Expandable sections inside an answer whose open/closed state changes the
 * message's rendered height.
 */
export type MessageSection = 'reasoning' | 'sources' | 'confidence';

/**
 * Per-message expand state, owned ABOVE the virtualized message list.
 *
 * The chat list is virtualized (react-virtuoso), so off-screen messages are
 * unmounted and re-measured when they scroll back in. Any expand state held
 * inside the message component — `useState`, or the DOM state of a native
 * `<details open>` — dies with the node, so a message that was tall while
 * expanded comes back collapsed and short. Virtuoso then revises its total
 * content height mid-scroll, and when that height shrinks the browser clamps
 * scrollTop: the user is thrown backwards, and near the end of a long chat the
 * bottom becomes unreachable.
 *
 * Keeping the state here means an item's height is identical before and after
 * remount, which is what virtualization requires.
 */
export function useMessageSections() {
    const [openKeys, setOpenKeys] = useState<ReadonlySet<string>>(() => new Set());

    const isOpen = useCallback(
        (messageId: string | undefined, section: MessageSection) =>
            messageId ? openKeys.has(`${messageId}:${section}`) : false,
        [openKeys],
    );

    // Stable across renders (functional update, no deps) so it doesn't defeat
    // the memo() on MessageBubble.
    const toggle = useCallback((messageId: string, section: MessageSection) => {
        setOpenKeys(prev => {
            const next = new Set(prev);
            const key = `${messageId}:${section}`;
            if (!next.delete(key)) next.add(key);
            return next;
        });
    }, []);

    return { isOpen, toggle };
}
