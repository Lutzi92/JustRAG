import { useState, useCallback } from 'react';
import axios from 'axios';
import type { KnowledgeBase, KbAssignableRole } from '../types';
import { API_BASE_URL } from '../api';
import { useTheme } from '../contexts/ThemeContext';
import { useToast } from '../contexts/ToastContext';
import { copyToClipboard } from '../utils/clipboard';
import { getApiErrorMessage } from '../utils/apiError';

interface UseSharingParams {
  username?: string;
}

export function useSharing({ username }: UseSharingParams) {
  const { t } = useTheme();
  const toast = useToast();
  const [showShareModal, setShowShareModal] = useState(false);
  const [sharingKb, setSharingKb] = useState<KnowledgeBase | null>(null);
  const [shareUserId, setShareUserId] = useState('');
  const [shareTargetUser, setShareTargetUser] = useState<{ id: string; firstName: string; lastName: string; username: string } | null>(null);
  const [shareLoading, setShareLoading] = useState(false);
  const [sharePermission, setSharePermission] = useState<KbAssignableRole>('view');
  const [copySuccess, setCopySuccess] = useState(false);
  const [notFoundUsername, setNotFoundUsername] = useState<string | null>(null);

  const handleOpenShare = useCallback((kb: KnowledgeBase, e: React.MouseEvent) => {
    e.stopPropagation();
    setSharingKb(kb);
    setShowShareModal(true);
    setShareUserId('');
    setShareTargetUser(null);
    setSharePermission('view');
    setNotFoundUsername(null);
  }, []);

  const lookupUser = useCallback(async () => {
    if (!shareUserId.trim()) return;
    setShareLoading(true);
    try {
      const res = await axios.get(`${API_BASE_URL}/api/users/${shareUserId}`);
      setShareTargetUser(res.data);
      setNotFoundUsername(null);
    } catch (err: unknown) {
      setShareTargetUser(null);
      if (axios.isAxiosError(err) && err.response?.status === 404) {
        // No toast here — MembersModal renders an inline "invite anyway"
        // block for this username, so a toast would announce the same
        // outcome twice.
        setNotFoundUsername(shareUserId.trim().toLowerCase());
      } else {
        // A genuine failure (500, timeout, offline) is NOT "no account yet" —
        // treating it as such would let the owner park an invite for a
        // username that may well exist, which reclassifies as a real share
        // the moment the network recovers. Surface the error and leave the
        // lookup state untouched.
        setNotFoundUsername(null);
        toast.error(getApiErrorMessage(err, t('shareError')));
      }
    } finally {
      setShareLoading(false);
    }
  }, [shareUserId, toast, t]);

  const confirmShare = useCallback(async () => {
    if (!sharingKb || !shareTargetUser) return;
    setShareLoading(true);
    try {
      // Four-role /members surface (Task 9): PUT with { role }, not the
      // deprecated POST /share with { permission }. The picker spans all
      // three assignable roles, the same set kbaccess.Assignable accepts and
      // the same set the per-member <select> in MembersModal offers — an
      // invite could previously only grant view/edit, so making somebody a
      // KB admin meant sharing first and correcting the role afterwards.
      await axios.put(`${API_BASE_URL}/api/kb/${sharingKb.id}/members/${shareTargetUser.id}`, {
        role: sharePermission
      });
      const permissionLabel = sharePermission === 'admin'
        ? t('adminPermission')
        : sharePermission === 'edit' ? t('editPermission') : t('viewPermission');
      toast.success(`${t('shareSuccess')} ${shareTargetUser.firstName || shareTargetUser.username} (${permissionLabel})`);
      setShowShareModal(false);
    } catch (err: unknown) {
      toast.error(getApiErrorMessage(err, t('shareError')));
    } finally {
      setShareLoading(false);
    }
  }, [sharingKb, shareTargetUser, sharePermission, t, toast]);

  const copyUserId = useCallback(() => {
    if (username) {
      void copyToClipboard(username).then((copied) => {
        if (copied) {
          setCopySuccess(true);
          setTimeout(() => setCopySuccess(false), 2000);
        } else {
          toast.error(t('clipboardCopyFailed'));
        }
      });
    }
  }, [username, toast, t]);

  const clearNotFound = useCallback(() => setNotFoundUsername(null), []);

  // Editing the input after a 404 must drop the stale notFoundUsername, or
  // the "Invite anyway" block stays live for whatever was typed before the
  // edit (e.g. typing "alicia" over a not-found "alice" without re-clicking
  // Search would park a pending invite for "alice"). Wrapped here — rather
  // than threading a 5th prop through MembersModal — so every caller of
  // setShareUserId gets the fix for free.
  const setShareUserIdAndClearNotFound = useCallback((id: string) => {
    setShareUserId(id);
    setNotFoundUsername(null);
  }, []);

  return {
    showShareModal, setShowShareModal,
    sharingKb, shareUserId, setShareUserId: setShareUserIdAndClearNotFound,
    shareTargetUser, shareLoading,
    sharePermission, setSharePermission,
    copySuccess,
    handleOpenShare, lookupUser, confirmShare, copyUserId,
    notFoundUsername, clearNotFound,
  };
}
