import { useState, useEffect, useRef, useMemo, useCallback } from 'react';
import type { Message } from '../types';
import {
  getPathToRoot, getBranchInfo,
} from '../utils/messageTree';

/**
 * Hook for message tree state management and navigation.
 *
 * Owns the message tree Map, the active leaf, comparison state,
 * and branch-switching / comparison logic.
 */
export function useMessageTree() {
  // Message tree state
  const [messageTree, setMessageTree] = useState<Map<string, Message>>(new Map());
  const messageTreeRef = useRef<Map<string, Message>>(new Map());

  // Sync ref with state
  useEffect(() => {
    messageTreeRef.current = messageTree;
  }, [messageTree]);

  const [activeLeafId, setActiveLeafId] = useState<string | null>(null);
  const messages = useMemo(() => getPathToRoot(messageTree, activeLeafId), [messageTree, activeLeafId]);

  // Comparison state
  const [comparisonMode, setComparisonMode] = useState(false);
  const [comparisonLeafId, setComparisonLeafId] = useState<string | null>(null);

  // Branch navigation
  const handleSwitchBranch = useCallback((siblingId: string) => {
    let currentId = siblingId;
    let current = messageTreeRef.current.get(currentId);
    while (current?.childIds && current.childIds.length > 0) {
      const lastChildId = current.childIds[current.childIds.length - 1];
      const child = messageTreeRef.current.get(lastChildId);
      if (!child) break;
      currentId = lastChildId;
      current = child;
    }
    setActiveLeafId(currentId);
  }, []);

  const handleStartComparison = useCallback((messageId: string) => {
    const info = getBranchInfo(messageTreeRef.current, messageId);
    if (!info || info.total < 2) return;
    const currentIdx = info.currentIndex;
    const otherIdx = currentIdx > 0 ? currentIdx - 1 : currentIdx + 1;
    const otherSiblingId = info.siblingIds[otherIdx];

    let otherId = otherSiblingId;
    let other = messageTreeRef.current.get(otherId);
    while (other?.childIds && other.childIds.length > 0) {
      const lastChildId = other.childIds[other.childIds.length - 1];
      const child = messageTreeRef.current.get(lastChildId);
      if (!child) break;
      otherId = lastChildId;
      other = child;
    }

    setComparisonLeafId(otherId);
    setComparisonMode(true);
  }, []);

  return {
    messageTree,
    setMessageTree,
    messageTreeRef,
    activeLeafId,
    setActiveLeafId,
    messages,
    comparisonMode,
    setComparisonMode,
    comparisonLeafId,
    setComparisonLeafId,
    handleSwitchBranch,
    handleStartComparison,
  };
}
