import { useState, useCallback } from 'react';
import axios from 'axios';
import type { KnowledgeBase } from '../types';
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
  const [sharePermission, setSharePermission] = useState<'view' | 'edit'>('view');
  const [copySuccess, setCopySuccess] = useState(false);

  const handleOpenShare = useCallback((kb: KnowledgeBase, e: React.MouseEvent) => {
    e.stopPropagation();
    setSharingKb(kb);
    setShowShareModal(true);
    setShareUserId('');
    setShareTargetUser(null);
    setSharePermission('view');
  }, []);

  const lookupUser = useCallback(async () => {
    if (!shareUserId.trim()) return;
    setShareLoading(true);
    try {
      const res = await axios.get(`${API_BASE_URL}/api/users/${shareUserId}`);
      setShareTargetUser(res.data);
    } catch {
      setShareTargetUser(null);
      toast.warning(t('userNotFound'));
    } finally {
      setShareLoading(false);
    }
  }, [shareUserId, t, toast]);

  const confirmShare = useCallback(async () => {
    if (!sharingKb || !shareTargetUser) return;
    setShareLoading(true);
    try {
      await axios.post(`${API_BASE_URL}/api/kb/${sharingKb.id}/share`, {
        userId: shareTargetUser.id,
        permission: sharePermission
      });
      const permissionLabel = sharePermission === 'edit' ? t('editPermission') : t('viewPermission');
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

  return {
    showShareModal, setShowShareModal,
    sharingKb, shareUserId, setShareUserId,
    shareTargetUser, shareLoading,
    sharePermission, setSharePermission,
    copySuccess,
    handleOpenShare, lookupUser, confirmShare, copyUserId,
  };
}
