import type { Message, BranchInfo } from '../types';

/**
 * Build a map of messages keyed by ID, with computed childIds.
 * Messages without IDs are skipped.
 */
export function buildMessageMap(messages: Message[]): Map<string, Message> {
    const map = new Map<string, Message>();

    // First pass: add all messages with IDs
    for (const msg of messages) {
        if (msg.id) {
            map.set(msg.id, { ...msg, childIds: [] });
        }
    }

    // Second pass: compute childIds from parentMessageId
    for (const msg of map.values()) {
        if (msg.parentMessageId && map.has(msg.parentMessageId)) {
            const parent = map.get(msg.parentMessageId)!;
            if (!parent.childIds) parent.childIds = [];
            parent.childIds.push(msg.id!);
        }
    }

    return map;
}

/**
 * Get the path from root to the given leaf message (root-first order).
 * Returns empty array if leafId is null or not found.
 */
export function getPathToRoot(map: Map<string, Message>, leafId: string | null): Message[] {
    if (!leafId || !map.has(leafId)) return [];

    const path: Message[] = [];
    let currentId: string | null = leafId;

    while (currentId) {
        const msg = map.get(currentId);
        if (!msg) break;
        path.unshift(msg);
        currentId = msg.parentMessageId || null;
    }

    return path;
}

/**
 * Find the default leaf by following the last child at each branch point,
 * starting from the root(s).
 */
export function findDefaultLeaf(map: Map<string, Message>): string | null {
    if (map.size === 0) return null;

    // Find root messages (no parentMessageId or parent not in map)
    const roots: Message[] = [];
    for (const msg of map.values()) {
        if (!msg.parentMessageId || !map.has(msg.parentMessageId)) {
            roots.push(msg);
        }
    }

    if (roots.length === 0) return null;

    // Start from the last root (most recent if multiple exist)
    let current = roots[roots.length - 1];

    // Follow the last child at each node
    while (current.childIds && current.childIds.length > 0) {
        const lastChildId = current.childIds[current.childIds.length - 1];
        const child = map.get(lastChildId);
        if (!child) break;
        current = child;
    }

    return current.id || null;
}

/**
 * Get branch navigation info for a given message.
 * Returns null if the message is not at a branch point (only child of its parent).
 */
export function getBranchInfo(map: Map<string, Message>, messageId: string): BranchInfo | null {
    const msg = map.get(messageId);
    if (!msg) return null;

    // Find siblings: messages with the same parentMessageId
    const parentId = msg.parentMessageId;
    if (!parentId) {
        // Root message - find all roots
        const roots: string[] = [];
        for (const m of map.values()) {
            if (!m.parentMessageId || !map.has(m.parentMessageId)) {
                if (m.id) roots.push(m.id);
            }
        }
        if (roots.length <= 1) return null;
        return {
            parentMessageId: '',
            siblingIds: roots,
            currentIndex: roots.indexOf(messageId),
            total: roots.length,
        };
    }

    const parent = map.get(parentId);
    if (!parent || !parent.childIds || parent.childIds.length <= 1) return null;

    return {
        parentMessageId: parentId,
        siblingIds: parent.childIds,
        currentIndex: parent.childIds.indexOf(messageId),
        total: parent.childIds.length,
    };
}

/**
 * Find the common ancestor of two messages.
 */
export function findCommonAncestor(map: Map<string, Message>, idA: string, idB: string): string | null {
    // Build ancestor set for A
    const ancestorsA = new Set<string>();
    let current: string | null = idA;
    while (current) {
        ancestorsA.add(current);
        const msg = map.get(current);
        if (!msg) break;
        current = msg.parentMessageId || null;
    }

    // Walk up from B until we find a common ancestor
    current = idB;
    while (current) {
        if (ancestorsA.has(current)) return current;
        const msg = map.get(current);
        if (!msg) break;
        current = msg.parentMessageId || null;
    }

    return null;
}

/**
 * Add a message to the tree map, updating parent's childIds.
 */
export function addMessageToTree(map: Map<string, Message>, msg: Message): Map<string, Message> {
    const newMap = new Map(map);
    if (!msg.id) return newMap;

    newMap.set(msg.id, { ...msg, childIds: [] });

    if (msg.parentMessageId && newMap.has(msg.parentMessageId)) {
        const parent = newMap.get(msg.parentMessageId)!;
        const updatedParent = {
            ...parent,
            childIds: [...(parent.childIds || []), msg.id],
        };
        newMap.set(msg.parentMessageId, updatedParent);
    }

    return newMap;
}

/**
 * Update a message in the tree by ID, preserving childIds.
 */
export function updateMessageInTree(map: Map<string, Message>, msgId: string, updates: Partial<Message>): Map<string, Message> {
    const newMap = new Map(map);
    const existing = newMap.get(msgId);
    if (!existing) return newMap;

    newMap.set(msgId, { ...existing, ...updates, childIds: existing.childIds });
    return newMap;
}

/**
 * Remap a temp ID to a real ID in the tree.
 */
export function remapMessageId(map: Map<string, Message>, tempId: string, realId: string): Map<string, Message> {
    const newMap = new Map(map);
    const msg = newMap.get(tempId);
    if (!msg) return newMap;

    // Remove old entry
    newMap.delete(tempId);

    // Add with new ID
    const updated = { ...msg, id: realId };
    newMap.set(realId, updated);

    // Update parent's childIds reference
    if (msg.parentMessageId && newMap.has(msg.parentMessageId)) {
        const parent = newMap.get(msg.parentMessageId)!;
        const updatedChildIds = (parent.childIds || []).map(id => id === tempId ? realId : id);
        newMap.set(msg.parentMessageId, { ...parent, childIds: updatedChildIds });
    }

    // Update children's parentMessageId reference
    if (msg.childIds) {
        for (const childId of msg.childIds) {
            const child = newMap.get(childId);
            if (child) {
                newMap.set(childId, { ...child, parentMessageId: realId });
            }
        }
    }

    return newMap;
}
