import { useState, useCallback, useEffect, useRef } from 'react';
import axios from 'axios';
import type { KnowledgeBase, GitRepoSource } from '../types';
import { API_BASE_URL } from '../api';
import { useTheme } from '../contexts/ThemeContext';
import { useModalContext } from '../contexts/ModalContext';
import { useToast } from '../contexts/ToastContext';
import { getApiErrorMessage } from '../utils/apiError';

interface UseGitRepoSourcesParams {
    currentKb: KnowledgeBase | null;
    fetchFiles: (kbId: string) => Promise<void>;
}

const POLL_INTERVAL_MS = 3000;

export function useGitRepoSources({ currentKb, fetchFiles }: UseGitRepoSourcesParams) {
    const { t } = useTheme();
    const { showConfirm } = useModalContext();
    const toast = useToast();
    const [gitRepoSources, setGitRepoSources] = useState<GitRepoSource[]>([]);
    const [gitRepoLoading, setGitRepoLoading] = useState(false);
    const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);
    const wasSyncingRef = useRef(false);

    const fetchGitRepoSources = useCallback(async (kbId?: string) => {
        const id = kbId || currentKb?.id;
        if (!id) return;
        try {
            const res = await axios.get(`${API_BASE_URL}/api/kb/${id}/git-repos`);
            setGitRepoSources(Array.isArray(res.data) ? res.data : []);
        } catch (err: unknown) {
            if (axios.isCancel(err)) return;
            console.error('Failed to fetch git repo sources:', err);
        }
    }, [currentKb?.id]);

    // Poll while any source is syncing
    useEffect(() => {
        const isSyncing = gitRepoSources.some(s => s.status === 'syncing');

        if (isSyncing && !pollRef.current) {
            wasSyncingRef.current = true;
            pollRef.current = setInterval(() => {
                fetchGitRepoSources();
                if (wasSyncingRef.current && currentKb) {
                    fetchFiles(currentKb.id);
                }
            }, POLL_INTERVAL_MS);
        } else if (!isSyncing && pollRef.current) {
            clearInterval(pollRef.current);
            pollRef.current = null;
            if (currentKb) {
                wasSyncingRef.current = false;
                fetchFiles(currentKb.id);
            }
        }

        if (isSyncing) wasSyncingRef.current = true;

        return () => {
            if (pollRef.current) {
                clearInterval(pollRef.current);
                pollRef.current = null;
            }
        };
    }, [gitRepoSources, fetchGitRepoSources, fetchFiles, currentKb]);

    const addGitRepoSource = useCallback(async (data: {
        repoUrl: string;
        isPrivate: boolean;
        accessToken?: string;
        branch?: string;
    }) => {
        if (!currentKb) return;
        setGitRepoLoading(true);
        try {
            await axios.post(`${API_BASE_URL}/api/kb/${currentKb.id}/git-repos`, data);
            toast.success(t('gitRepoSourceAdded'));
            await fetchGitRepoSources(currentKb.id);
            await fetchFiles(currentKb.id);
        } catch (err: unknown) {
            toast.error(getApiErrorMessage(err, t('error')));
        } finally {
            setGitRepoLoading(false);
        }
    }, [currentKb, fetchGitRepoSources, fetchFiles, t, toast]);

    const updateGitRepoSource = useCallback(async (sourceId: string, updates: { status?: 'active' | 'paused' }) => {
        if (!currentKb) return;
        setGitRepoSources(prev => prev.map(s => s.id === sourceId ? { ...s, ...updates } : s));
        try {
            const res = await axios.patch(`${API_BASE_URL}/api/kb/${currentKb.id}/git-repos/${sourceId}`, updates);
            setGitRepoSources(prev => prev.map(s => s.id === sourceId ? res.data : s));
            toast.success(t('gitRepoSourceUpdated'));
        } catch (err: unknown) {
            await fetchGitRepoSources(currentKb.id);
            toast.error(getApiErrorMessage(err, t('error')));
        }
    }, [currentKb, fetchGitRepoSources, t, toast]);

    const deleteGitRepoSource = useCallback(async (sourceId: string) => {
        if (!currentKb) return;
        const confirmed = await showConfirm(t('deleteGitRepoSource'), t('deleteGitRepoSourceConfirm'));
        if (!confirmed) return;

        setGitRepoSources(prev => prev.filter(s => s.id !== sourceId));
        try {
            await axios.delete(`${API_BASE_URL}/api/kb/${currentKb.id}/git-repos/${sourceId}`);
            toast.success(t('gitRepoSourceDeleted'));
            await fetchFiles(currentKb.id);
        } catch (err: unknown) {
            await fetchGitRepoSources(currentKb.id);
            toast.error(getApiErrorMessage(err, t('error')));
        }
    }, [currentKb, fetchFiles, fetchGitRepoSources, showConfirm, t, toast]);

    const syncGitRepoNow = useCallback(async (sourceId: string) => {
        if (!currentKb) return;
        try {
            await axios.post(`${API_BASE_URL}/api/kb/${currentKb.id}/git-repos/${sourceId}/sync`);
            toast.success(t('gitRepoSyncTriggered'));
            await fetchGitRepoSources(currentKb.id);
        } catch (err: unknown) {
            toast.error(getApiErrorMessage(err, t('error')));
        }
    }, [currentKb, fetchGitRepoSources, t, toast]);

    return {
        gitRepoSources,
        gitRepoLoading,
        fetchGitRepoSources,
        addGitRepoSource,
        updateGitRepoSource,
        deleteGitRepoSource,
        syncGitRepoNow,
    };
}
