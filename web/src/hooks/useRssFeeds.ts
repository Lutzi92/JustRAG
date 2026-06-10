import { useState, useCallback } from 'react';
import axios from 'axios';
import type { KnowledgeBase, RssFeed } from '../types';
import { API_BASE_URL } from '../api';
import { useTheme } from '../contexts/ThemeContext';
import { useModalContext } from '../contexts/ModalContext';
import { useToast } from '../contexts/ToastContext';
import { getApiErrorMessage } from '../utils/apiError';

interface UseRssFeedsParams {
    currentKb: KnowledgeBase | null;
    fetchFiles: (kbId: string) => Promise<void>;
}

export function useRssFeeds({ currentKb, fetchFiles }: UseRssFeedsParams) {
    const { t } = useTheme();
    const { showConfirm } = useModalContext();
    const toast = useToast();
    const [rssFeeds, setRssFeeds] = useState<RssFeed[]>([]);
    const [rssLoading, setRssLoading] = useState(false);

    const fetchRssFeeds = useCallback(async (kbId?: string, signal?: AbortSignal) => {
        const id = kbId || currentKb?.id;
        if (!id) return;
        try {
            const res = await axios.get(`${API_BASE_URL}/api/kb/${id}/rss`, { signal });
            setRssFeeds(Array.isArray(res.data) ? res.data : []);
        } catch (err: unknown) {
            if (axios.isCancel(err)) return;
            console.error('Failed to fetch RSS feeds:', err);
            toast.error(t('rssFetchError'));
        }
    }, [currentKb?.id, t, toast]);

    const addRssFeed = useCallback(async (url: string, pollInterval: number) => {
        if (!currentKb) return;
        setRssLoading(true);
        try {
            const res = await axios.post(`${API_BASE_URL}/api/kb/${currentKb.id}/rss`, { url, pollInterval });
            setRssFeeds(prev => [res.data, ...prev]);
            toast.success(t('rssFeedAdded'));
            // Poll for updates: the worker processes the initial feed asynchronously.
            // Refresh both the feed list (for itemCount) and files at 3s, 8s, 15s.
            for (const delay of [3000, 8000, 15000]) {
                setTimeout(() => {
                    fetchRssFeeds();
                    fetchFiles(currentKb.id);
                }, delay);
            }
        } catch (err: unknown) {
            toast.error(getApiErrorMessage(err, t('error')));
        } finally {
            setRssLoading(false);
        }
    }, [currentKb, fetchFiles, fetchRssFeeds, t, toast]);

    const updateRssFeed = useCallback(async (feedId: string, updates: { pollInterval?: number; status?: 'active' | 'paused' }) => {
        if (!currentKb) return;
        setRssFeeds(prev => prev.map(f => f.id === feedId ? { ...f, ...updates } : f));
        try {
            const res = await axios.patch(`${API_BASE_URL}/api/kb/${currentKb.id}/rss/${feedId}`, updates);
            setRssFeeds(prev => prev.map(f => f.id === feedId ? res.data : f));
            toast.success(t('rssFeedUpdated'));
        } catch (err: unknown) {
            await fetchRssFeeds();
            toast.error(getApiErrorMessage(err, t('error')));
        }
    }, [currentKb, fetchRssFeeds, t, toast]);

    const deleteRssFeed = useCallback(async (feedId: string) => {
        if (!currentKb) return;
        const confirmed = await showConfirm(t('deleteRssFeed'), t('deleteRssFeedConfirm'));
        if (!confirmed) return;

        setRssFeeds(prev => prev.filter(f => f.id !== feedId));
        try {
            await axios.delete(`${API_BASE_URL}/api/kb/${currentKb.id}/rss/${feedId}`);
            toast.success(t('rssFeedDeleted'));
            await fetchFiles(currentKb.id);
        } catch (err: unknown) {
            await fetchRssFeeds();
            toast.error(getApiErrorMessage(err, t('error')));
        }
    }, [currentKb, fetchFiles, fetchRssFeeds, showConfirm, t, toast]);

    const pollFeedNow = useCallback(async (feedId: string) => {
        if (!currentKb) return;
        try {
            await axios.post(`${API_BASE_URL}/api/kb/${currentKb.id}/rss/${feedId}/poll`);
            toast.success(t('rssPollTriggered'));
            setTimeout(() => {
                fetchRssFeeds();
                fetchFiles(currentKb.id);
            }, 5000);
        } catch (err: unknown) {
            toast.error(getApiErrorMessage(err, t('error')));
        }
    }, [currentKb, fetchFiles, fetchRssFeeds, t, toast]);

    return { rssFeeds, rssLoading, fetchRssFeeds, addRssFeed, updateRssFeed, deleteRssFeed, pollFeedNow };
}
