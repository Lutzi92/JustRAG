import { describe, it, expect } from 'vitest';
import type { Message } from '../types';
import {
  buildMessageMap,
  getPathToRoot,
  findDefaultLeaf,
  getBranchInfo,
  findCommonAncestor,
  addMessageToTree,
  updateMessageInTree,
  remapMessageId,
  nearestPersistedAncestorId,
} from './messageTree';

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------
function msg(id: string, role: 'user' | 'ai', parentMessageId?: string): Message {
  return { id, role, content: `msg-${id}`, parentMessageId };
}

// A simple linear chain: u1 -> a1 -> u2 -> a2
const linearMessages: Message[] = [
  msg('u1', 'user'),
  msg('a1', 'ai', 'u1'),
  msg('u2', 'user', 'a1'),
  msg('a2', 'ai', 'u2'),
];

// A branching tree:
//   u1 -> a1 -> u2 -> a2
//           \-> u3 -> a3
const branchingMessages: Message[] = [
  msg('u1', 'user'),
  msg('a1', 'ai', 'u1'),
  msg('u2', 'user', 'a1'),
  msg('a2', 'ai', 'u2'),
  msg('u3', 'user', 'a1'),
  msg('a3', 'ai', 'u3'),
];

// ---------------------------------------------------------------------------
// buildMessageMap
// ---------------------------------------------------------------------------
describe('buildMessageMap', () => {
  it('creates a map with all messages keyed by id', () => {
    const map = buildMessageMap(linearMessages);
    expect(map.size).toBe(4);
    expect(map.has('u1')).toBe(true);
    expect(map.has('a2')).toBe(true);
  });

  it('computes childIds from parentMessageId', () => {
    const map = buildMessageMap(linearMessages);
    expect(map.get('u1')!.childIds).toEqual(['a1']);
    expect(map.get('a1')!.childIds).toEqual(['u2']);
    expect(map.get('a2')!.childIds).toEqual([]);
  });

  it('computes multiple childIds at a branch point', () => {
    const map = buildMessageMap(branchingMessages);
    expect(map.get('a1')!.childIds).toEqual(['u2', 'u3']);
  });

  it('returns an empty map for empty input', () => {
    const map = buildMessageMap([]);
    expect(map.size).toBe(0);
  });

  it('skips messages without IDs', () => {
    const msgs: Message[] = [
      { role: 'user', content: 'no id' },
      msg('a1', 'ai'),
    ];
    const map = buildMessageMap(msgs);
    expect(map.size).toBe(1);
    expect(map.has('a1')).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// getPathToRoot
// ---------------------------------------------------------------------------
describe('getPathToRoot', () => {
  it('returns root-first path from root to the given leaf', () => {
    const map = buildMessageMap(linearMessages);
    const path = getPathToRoot(map, 'a2');
    expect(path.map(m => m.id)).toEqual(['u1', 'a1', 'u2', 'a2']);
  });

  it('returns single-element path for a root message', () => {
    const map = buildMessageMap(linearMessages);
    const path = getPathToRoot(map, 'u1');
    expect(path.map(m => m.id)).toEqual(['u1']);
  });

  it('returns empty array for null leafId', () => {
    const map = buildMessageMap(linearMessages);
    expect(getPathToRoot(map, null)).toEqual([]);
  });

  it('returns empty array for unknown leafId', () => {
    const map = buildMessageMap(linearMessages);
    expect(getPathToRoot(map, 'nonexistent')).toEqual([]);
  });
});

// ---------------------------------------------------------------------------
// findDefaultLeaf
// ---------------------------------------------------------------------------
describe('findDefaultLeaf', () => {
  it('follows last child at each branch to reach the deepest leaf', () => {
    const map = buildMessageMap(branchingMessages);
    // Last root = u1, a1 has children [u2, u3], last = u3, u3 -> a3 (leaf)
    expect(findDefaultLeaf(map)).toBe('a3');
  });

  it('returns the deepest node in a linear chain', () => {
    const map = buildMessageMap(linearMessages);
    expect(findDefaultLeaf(map)).toBe('a2');
  });

  it('returns null for an empty map', () => {
    expect(findDefaultLeaf(new Map())).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// getBranchInfo
// ---------------------------------------------------------------------------
describe('getBranchInfo', () => {
  it('returns null for an only-child (no branching)', () => {
    const map = buildMessageMap(linearMessages);
    expect(getBranchInfo(map, 'a1')).toBeNull();
  });

  it('returns sibling info at a branch point', () => {
    const map = buildMessageMap(branchingMessages);
    const info = getBranchInfo(map, 'u2');
    expect(info).toEqual({
      parentMessageId: 'a1',
      siblingIds: ['u2', 'u3'],
      currentIndex: 0,
      total: 2,
    });
  });

  it('returns correct index for second sibling', () => {
    const map = buildMessageMap(branchingMessages);
    const info = getBranchInfo(map, 'u3');
    expect(info).toEqual({
      parentMessageId: 'a1',
      siblingIds: ['u2', 'u3'],
      currentIndex: 1,
      total: 2,
    });
  });

  it('returns null for unknown messageId', () => {
    const map = buildMessageMap(linearMessages);
    expect(getBranchInfo(map, 'nonexistent')).toBeNull();
  });

  it('handles root-level branches', () => {
    const msgs: Message[] = [msg('r1', 'user'), msg('r2', 'user')];
    const map = buildMessageMap(msgs);
    const info = getBranchInfo(map, 'r1');
    expect(info).not.toBeNull();
    expect(info!.siblingIds).toEqual(['r1', 'r2']);
    expect(info!.total).toBe(2);
  });
});

// ---------------------------------------------------------------------------
// findCommonAncestor
// ---------------------------------------------------------------------------
describe('findCommonAncestor', () => {
  it('finds the fork point for two branches', () => {
    const map = buildMessageMap(branchingMessages);
    // u2 and u3 both descend from a1
    expect(findCommonAncestor(map, 'a2', 'a3')).toBe('a1');
  });

  it('returns the node itself when it is an ancestor of the other', () => {
    const map = buildMessageMap(linearMessages);
    expect(findCommonAncestor(map, 'u1', 'a2')).toBe('u1');
  });

  it('returns null when no common ancestor exists', () => {
    const msgs: Message[] = [msg('x', 'user'), msg('y', 'user')];
    const map = buildMessageMap(msgs);
    expect(findCommonAncestor(map, 'x', 'y')).toBeNull();
  });

  it('returns the id itself when both ids are the same', () => {
    const map = buildMessageMap(linearMessages);
    expect(findCommonAncestor(map, 'a1', 'a1')).toBe('a1');
  });
});

// ---------------------------------------------------------------------------
// addMessageToTree
// ---------------------------------------------------------------------------
describe('addMessageToTree', () => {
  it('adds a new message and updates parent childIds', () => {
    const map = buildMessageMap(linearMessages);
    const newMsg = msg('u3', 'user', 'a2');
    const updated = addMessageToTree(map, newMsg);

    expect(updated.has('u3')).toBe(true);
    expect(updated.get('u3')!.childIds).toEqual([]);
    expect(updated.get('a2')!.childIds).toContain('u3');
  });

  it('returns a new Map (does not mutate original)', () => {
    const map = buildMessageMap(linearMessages);
    const newMsg = msg('u3', 'user', 'a2');
    const updated = addMessageToTree(map, newMsg);

    expect(updated).not.toBe(map);
    expect(map.has('u3')).toBe(false);
  });

  it('handles message without an id gracefully', () => {
    const map = buildMessageMap(linearMessages);
    const noId: Message = { role: 'user', content: 'no id' };
    const updated = addMessageToTree(map, noId);
    expect(updated.size).toBe(map.size);
  });
});

// ---------------------------------------------------------------------------
// updateMessageInTree
// ---------------------------------------------------------------------------
describe('updateMessageInTree', () => {
  it('updates content while preserving childIds', () => {
    const map = buildMessageMap(linearMessages);
    const updated = updateMessageInTree(map, 'a1', { content: 'new content' });

    expect(updated.get('a1')!.content).toBe('new content');
    expect(updated.get('a1')!.childIds).toEqual(map.get('a1')!.childIds);
  });

  it('returns unchanged map for unknown id', () => {
    const map = buildMessageMap(linearMessages);
    const updated = updateMessageInTree(map, 'nonexistent', { content: 'x' });
    expect(updated.size).toBe(map.size);
  });
});

// ---------------------------------------------------------------------------
// remapMessageId
// ---------------------------------------------------------------------------
describe('remapMessageId', () => {
  it('replaces temp ID with real ID', () => {
    const map = buildMessageMap(linearMessages);
    const updated = remapMessageId(map, 'a2', 'real-a2');

    expect(updated.has('a2')).toBe(false);
    expect(updated.has('real-a2')).toBe(true);
    expect(updated.get('real-a2')!.id).toBe('real-a2');
  });

  it('updates parent childIds reference', () => {
    const map = buildMessageMap(linearMessages);
    const updated = remapMessageId(map, 'a2', 'real-a2');

    expect(updated.get('u2')!.childIds).toContain('real-a2');
    expect(updated.get('u2')!.childIds).not.toContain('a2');
  });

  it('updates children parentMessageId reference', () => {
    const map = buildMessageMap(linearMessages);
    // Remap a1 which has child u2
    const updated = remapMessageId(map, 'a1', 'real-a1');

    expect(updated.get('u2')!.parentMessageId).toBe('real-a1');
  });

  it('returns unchanged map for unknown tempId', () => {
    const map = buildMessageMap(linearMessages);
    const updated = remapMessageId(map, 'nonexistent', 'real');
    expect(updated.size).toBe(map.size);
  });
});

describe('nearestPersistedAncestorId', () => {
  // A failed send leaves a temp-error leaf whose ancestors up to the last
  // real message are all temp ids: temp-error -> temp-user -> <real parent>.
  const map = buildMessageMap([
    msg('real-a1', 'ai', 'real-u1'),
    msg('temp-user-123', 'user', 'real-a1'),
    msg('temp-error-123', 'ai', 'temp-user-123'),
  ]);

  it('returns the id itself when it is already persisted (non-temp)', () => {
    expect(nearestPersistedAncestorId(map, 'real-a1')).toBe('real-a1');
  });

  it('walks past temp ancestors to the nearest persisted id', () => {
    expect(nearestPersistedAncestorId(map, 'temp-error-123')).toBe('real-a1');
  });

  it('returns null when the whole chain is temp / unpersisted', () => {
    const allTemp = buildMessageMap([
      msg('temp-user-9', 'user'),
      msg('temp-error-9', 'ai', 'temp-user-9'),
    ]);
    expect(nearestPersistedAncestorId(allTemp, 'temp-error-9')).toBeNull();
  });

  it('returns null for null/undefined input', () => {
    expect(nearestPersistedAncestorId(map, undefined)).toBeNull();
    expect(nearestPersistedAncestorId(map, null)).toBeNull();
  });
});
