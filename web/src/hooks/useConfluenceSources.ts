import { useState, useCallback, useEffect, useRef } from 'react';
import axios from 'axios';
import type { KnowledgeBase, ConfluenceSource, ConfluenceConnectionInfo, ConfluenceSpace, ConfluencePage, ConfluencePageWithPath } from '../types';
import { API_BASE_URL } from '../api';
import { useTheme } from '../contexts/ThemeContext';
import { useModalContext } from '../contexts/ModalContext';
import { useToast } from '../contexts/ToastContext';
import { getApiErrorMessage } from '../utils/apiError';

interface UseConfluenceSourcesParams {
    currentKb: KnowledgeBase | null;
    fetchFiles: (kbId: string) => Promise<void>;
}

const POLL_INTERVAL_MS = 3000;

export function useConfluenceSources({ currentKb, fetchFiles }: UseConfluenceSourcesParams) {
    const { t } = useTheme();
    const { showConfirm } = useModalContext();
    const toast = useToast();
    const [confluenceSources, setConfluenceSources] = useState<ConfluenceSource[]>([]);
    const [confluenceConnection, setConfluenceConnection] = useState<ConfluenceConnectionInfo | null>(null);
    const [confluenceLoading, setConfluenceLoading] = useState(false);
    const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);
    const wasSyncingRef = useRef(false);
    const pendingSourceIdsRef = useRef<Set<string>>(new Set());

    const fetchConnectionInfo = useCallback(async () => {
        try {
            const res = await axios.get(`${API_BASE_URL}/api/confluence/connections`);
            setConfluenceConnection(res.data);
        } catch (err: unknown) {
            if (axios.isCancel(err)) return;
            console.error('Failed to fetch Confluence connection info:', err);
        }
    }, []);

    const saveConnection = useCallback(async (token: string, displayName?: string) => {
        setConfluenceLoading(true);
        try {
            // PUT when an existing connection is in the DB (even if status is
            // 'error' — that's the failsafe-reconnect path), POST otherwise.
            // The backend rejects POST when a connection already exists for
            // the user, so the distinction is load-bearing here.
            const existing = confluenceConnection?.connection;
            if (existing) {
                const res = await axios.put(`${API_BASE_URL}/api/confluence/connections/${existing.id}`, { token, displayName });
                setConfluenceConnection(prev => prev ? { ...prev, connection: res.data } : prev);
            } else {
                const res = await axios.post(`${API_BASE_URL}/api/confluence/connections`, { token, displayName });
                setConfluenceConnection(res.data);
            }
            toast.success(t('confluenceConnected'));
        } catch (err: unknown) {
            toast.error(getApiErrorMessage(err, t('error')));
            throw err;
        } finally {
            setConfluenceLoading(false);
        }
    }, [confluenceConnection, t, toast]);

    const fetchSources = useCallback(async (kbId?: string, signal?: AbortSignal) => {
        const id = kbId || currentKb?.id;
        if (!id) return;
        try {
            const res = await axios.get(`${API_BASE_URL}/api/kb/${id}/confluence-sources`, { signal });
            setConfluenceSources(Array.isArray(res.data) ? res.data : []);
        } catch (err: unknown) {
            if (axios.isCancel(err)) return;
            console.error('Failed to fetch Confluence sources:', err);
        }
    }, [currentKb?.id]);

    // Poll while any source is syncing or recently added (waiting for worker to start)
    useEffect(() => {
        const isSyncing = confluenceSources.some(s => s.status === 'syncing');

        // Remove pending IDs for sources that have completed (went through syncing and back to active with data)
        if (pendingSourceIdsRef.current.size > 0) {
            for (const id of pendingSourceIdsRef.current) {
                const source = confluenceSources.find(s => s.id === id);
                if (source && source.status !== 'syncing' && (source.pageCount > 0 || source.lastSyncedAt || source.status === 'error' || source.status === 'paused')) {
                    pendingSourceIdsRef.current.delete(id);
                }
            }
        }

        const hasPending = pendingSourceIdsRef.current.size > 0;
        const needsPolling = isSyncing || hasPending;

        if (needsPolling && !pollRef.current) {
            if (isSyncing) wasSyncingRef.current = true;
            pollRef.current = setInterval(() => {
                fetchSources();
                // Also refresh files while syncing so new sources appear incrementally
                if (wasSyncingRef.current && currentKb) {
                    fetchFiles(currentKb.id);
                }
            }, POLL_INTERVAL_MS);
        } else if (!needsPolling && pollRef.current) {
            clearInterval(pollRef.current);
            pollRef.current = null;
            // Sync/pending just finished — refresh files to pick up new sources
            if (currentKb) {
                wasSyncingRef.current = false;
                fetchFiles(currentKb.id);
            }
        }

        // Track syncing state even when polling was already started for pending sources
        if (isSyncing) wasSyncingRef.current = true;

        return () => {
            if (pollRef.current) {
                clearInterval(pollRef.current);
                pollRef.current = null;
            }
        };
    }, [confluenceSources, fetchSources, fetchFiles, currentKb]);

    const addSource = useCallback(async (data: { spaceKey: string; rootPageId?: string; rootPageTitle?: string; includeAttachments?: boolean; syncInterval?: number }) => {
        const connectionId = confluenceConnection?.connection?.id;
        if (!currentKb || !connectionId) return;
        setConfluenceLoading(true);
        try {
            const res = await axios.post(`${API_BASE_URL}/api/kb/${currentKb.id}/confluence-sources`, { ...data, connectionId });
            // Track as pending so polling starts immediately (worker hasn't set 'syncing' yet)
            pendingSourceIdsRef.current.add(res.data.id);
            setConfluenceSources(prev => [res.data, ...prev]);
            toast.success(t('confluenceSourceAdded'));
        } catch (err: unknown) {
            toast.error(getApiErrorMessage(err, t('error')));
        } finally {
            setConfluenceLoading(false);
        }
    }, [currentKb, confluenceConnection, t, toast]);

    const updateSource = useCallback(async (sourceId: string, updates: { syncInterval?: number | null; status?: 'active' | 'paused'; includeAttachments?: boolean }) => {
        if (!currentKb) return;
        setConfluenceSources(prev => prev.map(s => s.id === sourceId ? { ...s, ...updates } : s));
        try {
            const res = await axios.patch(`${API_BASE_URL}/api/kb/${currentKb.id}/confluence-sources/${sourceId}`, updates);
            setConfluenceSources(prev => prev.map(s => s.id === sourceId ? res.data : s));
            toast.success(t('confluenceSourceUpdated'));
        } catch (err: unknown) {
            await fetchSources();
            toast.error(getApiErrorMessage(err, t('error')));
        }
    }, [currentKb, fetchSources, t, toast]);

    const deleteSource = useCallback(async (sourceId: string) => {
        if (!currentKb) return;
        const confirmed = await showConfirm(t('deleteConfluenceSource'), t('deleteConfluenceSourceConfirm'));
        if (!confirmed) return;

        pendingSourceIdsRef.current.delete(sourceId);
        setConfluenceSources(prev => prev.filter(s => s.id !== sourceId));
        try {
            await axios.delete(`${API_BASE_URL}/api/kb/${currentKb.id}/confluence-sources/${sourceId}`);
            toast.success(t('confluenceSourceDeleted'));
            await fetchFiles(currentKb.id);
        } catch (err: unknown) {
            await fetchSources();
            toast.error(getApiErrorMessage(err, t('error')));
        }
    }, [currentKb, fetchFiles, fetchSources, showConfirm, t, toast]);

    const syncNow = useCallback(async (sourceId: string) => {
        if (!currentKb) return;
        try {
            await axios.post(`${API_BASE_URL}/api/kb/${currentKb.id}/confluence-sources/${sourceId}/sync`);
            toast.success(t('confluenceSyncTriggered'));
            // Track as pending so polling starts immediately
            pendingSourceIdsRef.current.add(sourceId);
            await fetchSources();
        } catch (err: unknown) {
            toast.error(getApiErrorMessage(err, t('error')));
        }
    }, [currentKb, fetchSources, t, toast]);

    // fetchSpaces/fetchSpacePages let errors propagate so the modal can render
    // an inline "reconnect" failsafe. Returning [] on error (the old behaviour)
    // made the modal's space-loading effect see length=0, retrigger via its
    // dep array, and spin into an infinite refresh loop whenever the stored
    // token had silently gone bad.
    const fetchSpaces = useCallback(async (): Promise<ConfluenceSpace[]> => {
        const res = await axios.get(`${API_BASE_URL}/api/confluence/spaces`);
        return Array.isArray(res.data) ? res.data : [];
    }, []);

    const fetchSpacePages = useCallback(async (spaceKey: string): Promise<ConfluencePage[]> => {
        const res = await axios.get(`${API_BASE_URL}/api/confluence/spaces/${spaceKey}/pages`);
        return Array.isArray(res.data) ? res.data : [];
    }, []);

    const fetchPageChildren = useCallback(async (pageId: string): Promise<ConfluencePage[]> => {
        try {
            const res = await axios.get(`${API_BASE_URL}/api/confluence/pages/${pageId}/children`);
            return Array.isArray(res.data) ? res.data : [];
        } catch (err: unknown) {
            toast.error(getApiErrorMessage(err, t('error')));
            return [];
        }
    }, [t, toast]);

    const fetchAllSpacePages = useCallback(async (spaceKey: string): Promise<ConfluencePageWithPath[]> => {
        try {
            const res = await axios.get(`${API_BASE_URL}/api/confluence/spaces/${spaceKey}/pages/all`);
            return Array.isArray(res.data) ? res.data : [];
        } catch (err: unknown) {
            toast.error(getApiErrorMessage(err, t('error')));
            return [];
        }
    }, [t, toast]);

    return {
        confluenceSources,
        confluenceConnection,
        confluenceLoading,
        fetchConnectionInfo,
        saveConnection,
        fetchSources,
        addSource,
        updateSource,
        deleteSource,
        syncNow,
        fetchSpaces,
        fetchSpacePages,
        fetchPageChildren,
        fetchAllSpacePages,
    };
}
