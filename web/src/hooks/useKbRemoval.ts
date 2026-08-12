import { useCallback, useRef, useState } from 'react';
import axios from 'axios';
import type { KnowledgeBase } from '../types';
import { API_BASE_URL } from '../api';
import { useTheme } from '../contexts/ThemeContext';
import { useModalContext } from '../contexts/ModalContext';

export type KbRemovalOutcome = 'deleted' | 'left' | 'cancelled';

// useKbRemoval is the single place that decides delete vs. leave for a KB
// card's remove action. Nothing else should scatter its own
// `myRole !== 'owner'` check — every caller goes through removeKb.
export function useKbRemoval(): { removeKb: (kb: KnowledgeBase) => Promise<KbRemovalOutcome>; removing: boolean } {
  const { t } = useTheme();
  const { showConfirm } = useModalContext();
  const [removing, setRemoving] = useState(false);
  // A ref alongside the state: removeKb's own re-entry guard must be
  // synchronous (a double-click can fire the second call before React has
  // re-rendered with removing=true), so the guard below reads the ref, not
  // the state. The state exists only so callers can disable the button.
  const removingRef = useRef(false);

  const removeKb = useCallback(async (kb: KnowledgeBase): Promise<KbRemovalOutcome> => {
    // This spans a GET plus a DELETE; guard against a second removeKb call
    // (e.g. a double-click) firing a second confirmation/request before the
    // first one resolves. Not merely cosmetic — disabling the button in the
    // UI is a courtesy, this is the actual guard.
    if (removingRef.current) return 'cancelled';
    removingRef.current = true;
    setRemoving(true);
    try {
      // Exactly one decision point. The default is deliberately 'leave': an
      // absent myRole (an implicit viewer on a published global KB with no
      // kb_members row) must never reach the delete branch.
      const isOwner = kb.myRole === 'owner';

      if (isOwner) {
        const confirmed = await showConfirm(t('confirmDeleteKB'));
        if (!confirmed) return 'cancelled';
        await axios.delete(`${API_BASE_URL}/api/kb/${kb.id}`);
        return 'deleted';
      }

      // Non-owner: look up how many of the caller's chats in this KB would
      // be destroyed by leaving, so the confirmation names the real number
      // instead of asking the user to delete chats blindly.
      let chatCount: number;
      try {
        const res = await axios.get(`${API_BASE_URL}/api/kb/${kb.id}/membership/impact`);
        chatCount = res.data?.chatCount ?? 0;
      } catch (err: unknown) {
        const status = (err as { response?: { status?: number } } | null)?.response?.status;
        if (status !== 404) throw err;

        // 404 means the caller has no kb_members row at all — a subscriber-
        // style implicit viewer (e.g. on a published global KB), not a
        // member. Phase 1 has no subscription endpoint to unsubscribe from,
        // so this just confirms and reports nothing removed; do not invent
        // one.
        const confirmed = await showConfirm(t('confirmLeaveKbNoChats'));
        return confirmed ? 'left' : 'cancelled';
      }

      const confirmed = await showConfirm(t('confirmLeaveKb').replace('{count}', String(chatCount)));
      if (!confirmed) return 'cancelled';
      await axios.delete(`${API_BASE_URL}/api/kb/${kb.id}/membership`);
      return 'left';
    } finally {
      removingRef.current = false;
      setRemoving(false);
    }
  }, [showConfirm, t]);

  return { removeKb, removing };
}
