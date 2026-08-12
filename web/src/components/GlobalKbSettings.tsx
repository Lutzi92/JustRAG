import React, { useState, useEffect, useCallback } from 'react';
import axios from 'axios';
import {
    ArrowLeft, Save, Globe, FileText, Sparkles, Users, Trash2, Plus, Loader2,
    Upload, BookOpen, ToggleLeft, ToggleRight, X, Search, Rss, Link
} from 'lucide-react';
import type { KnowledgeBase, StudioConfig, FileEntry, GlobalKbEditor, SafeAIConfig, RssFeed, ConfluenceSource, ConfluenceConnectionInfo, ConfluenceSpace, ConfluencePage, ConfluencePageWithPath } from '../types';
import { API_BASE_URL } from '../api';
import { MAX_FILES_PER_GLOBAL_KB, ACCEPTED_FILE_TYPES } from '../constants';
import { useTheme } from '../contexts/ThemeContext';
import { useAuth } from '../contexts/AuthContext';
import { useFormValidation } from '../hooks/useFormValidation';
import { useToast } from '../contexts/ToastContext';
import { ConfluenceModal } from './sidebar/ConfluenceModal';
import { getApiErrorMessage } from '../utils/apiError';

interface GlobalKbSettingsProps {
    kb: KnowledgeBase;
    onBack: () => void;
    onOpenKb: () => void;
    onUpdate: (kb: KnowledgeBase) => void;
}

export const GlobalKbSettings: React.FC<GlobalKbSettingsProps> = ({ kb, onBack, onOpenKb, onUpdate }) => {
    const { t } = useTheme();
    const { siteConfigs } = useAuth();
    const { errors, validate, clearError } = useFormValidation({
        name: (v) => !v.trim() && t('fieldRequired'),
    });
    const toast = useToast();
    const [name, setName] = useState(kb.name);
    const [isPublished, setIsPublished] = useState(kb.isPublished ?? false);
    const [headerText, setHeaderText] = useState(kb.headerText || '');
    const [examplePrompts, setExamplePrompts] = useState(kb.examplePrompts || '');
    const [systemPrompt, setSystemPrompt] = useState(kb.systemPrompt || '');
    const [studioConfig, setStudioConfig] = useState<StudioConfig>(
        { cards: true, presentation: true, podcast: true, chart: true, abstract: true, briefingDoc: true, faq: true, studyGuide: true, timeline: true, quiz: true, ...kb.studioConfig }
    );
    const [files, setFiles] = useState<FileEntry[]>([]);
    const [editors, setEditors] = useState<GlobalKbEditor[]>([]);
    const [editorUsername, setEditorUsername] = useState('');
    const [editorLookup, setEditorLookup] = useState<{ id: string; username: string; firstName?: string; lastName?: string } | null>(null);
    const [editorLoading, setEditorLoading] = useState(false);
    const [saving, setSaving] = useState(false);
    const [uploading, setUploading] = useState(false);
    const [availableConfigs, setAvailableConfigs] = useState<SafeAIConfig[]>([]);

    // URL fetch state
    const [fetchUrl, setFetchUrl] = useState('');
    const [fetchingUrl, setFetchingUrl] = useState(false);

    // Crawl state
    const [crawlUrl, setCrawlUrl] = useState('');
    const [crawlMaxPages, setCrawlMaxPages] = useState(5);
    const [crawling, setCrawling] = useState(false);

    // RSS state
    const [rssUrl, setRssUrl] = useState('');
    const [rssPollInterval, setRssPollInterval] = useState(60);
    const [rssFetchFullText, setRssFetchFullText] = useState(false);
    const [rssLoading, setRssLoading] = useState(false);
    const [rssFeeds, setRssFeeds] = useState<RssFeed[]>([]);

    // Confluence state
    const [confluenceConnection, setConfluenceConnection] = useState<ConfluenceConnectionInfo | null>(null);
    const [confluenceLoading, setConfluenceLoading] = useState(false);
    const [confluenceSources, setConfluenceSources] = useState<ConfluenceSource[]>([]);
    const [showConfluenceModal, setShowConfluenceModal] = useState(false);
    const confluenceEnabled = siteConfigs?.confluence_enabled === 'true';

    // AI config state
    const [aiConfigId, setAiConfigId] = useState(kb.aiConfigId || '');
    const [chatModel, setChatModel] = useState(kb.chatModel || '');
    const [embeddingModel, setEmbeddingModel] = useState(kb.embeddingModel || '');
    const [rerankModel, setRerankModel] = useState(kb.rerankModel || '');
    const [ttsModel, setTtsModel] = useState(kb.ttsModel || '');

    const fetchFiles = useCallback(async () => {
        try {
            const res = await axios.get(`${API_BASE_URL}/api/kb/${kb.id}/files`);
            // On the global KB, hide failed uploads from the file list.
            setFiles((res.data as FileEntry[]).filter(f => f.status !== 'error'));
        } catch (err: unknown) {
            console.error('Failed to fetch files:', err);
            toast.error(t('filesFetchError'));
        }
    }, [kb.id, toast, t]);

    const fetchEditors = useCallback(async () => {
        try {
            const res = await axios.get(`${API_BASE_URL}/api/admin/global-kbs/${kb.id}/editors`);
            setEditors(res.data);
        } catch (err: unknown) {
            console.error('Failed to fetch editors:', err);
            toast.error(t('editorsFetchError'));
        }
    }, [kb.id, toast, t]);

    const fetchConfigs = useCallback(async () => {
        try {
            const res = await axios.get(`${API_BASE_URL}/api/public/configs`);
            setAvailableConfigs(res.data);
        } catch (err: unknown) {
            console.error('Failed to fetch configs:', err);
            toast.error(t('configsFetchError'));
        }
    }, [toast, t]);

    const handleSave = async () => {
        if (!validate({ name })) return;
        const optimistic = {
            ...kb, name, isPublished,
            headerText: headerText || null,
            examplePrompts: examplePrompts || null,
            systemPrompt: systemPrompt || null,
            studioConfig,
            aiConfigId: aiConfigId || null,
            chatModel: chatModel || null,
            embeddingModel: embeddingModel || null,
            rerankModel: rerankModel || null,
            ttsModel: ttsModel || null,
        };
        onUpdate(optimistic as KnowledgeBase);
        setSaving(true);
        try {
            const res = await axios.patch(`${API_BASE_URL}/api/admin/global-kbs/${kb.id}`, {
                name,
                isPublished,
                headerText: headerText || null,
                examplePrompts: examplePrompts || null,
                systemPrompt: systemPrompt || null,
                studioConfig,
                aiConfigId: aiConfigId || null,
                chatModel: chatModel || null,
                embeddingModel: embeddingModel || null,
                rerankModel: rerankModel || null,
                ttsModel: ttsModel || null,
            });
            onUpdate(res.data);
        } catch {
            onUpdate(kb);
            toast.error(t('settingsUpdateError'));
        } finally {
            setSaving(false);
        }
    };

    const handleFileUpload = async (e: React.ChangeEvent<HTMLInputElement>) => {
        const selectedFiles = e.target.files;
        if (!selectedFiles || selectedFiles.length === 0) return;

        setUploading(true);
        try {
            for (const file of Array.from(selectedFiles)) {
                if (files.length >= MAX_FILES_PER_GLOBAL_KB) break;
                const formData = new FormData();
                formData.append('file', file);
                try {
                    await axios.post(`${API_BASE_URL}/api/kb/${kb.id}/files`, formData);
                } catch (err: unknown) {
                    console.error('Upload failed:', err);
                    toast.error(getApiErrorMessage(err, t('uploadFailed')));
                }
            }
        } finally {
            setUploading(false);
            fetchFiles();
            e.target.value = '';
        }
    };

    const handleDeleteFile = async (fileId: string) => {
        const prev = files;
        setFiles(files.filter(f => f.id !== fileId));
        try {
            await axios.delete(`${API_BASE_URL}/api/files/${fileId}`);
        } catch {
            setFiles(prev);
            toast.error(t('deleteFileError'));
        }
    };

    // URL fetch
    const handleFetchUrl = async () => {
        if (!fetchUrl.trim()) return;
        setFetchingUrl(true);
        try {
            await axios.post(`${API_BASE_URL}/api/kb/${kb.id}/fetch-url`, { url: fetchUrl.trim() });
            setFetchUrl('');
            toast.success(t('sourceAdded'));
            fetchFiles();
        } catch (err: unknown) {
            toast.error(getApiErrorMessage(err, t('addSourcesFailed')));
        } finally {
            setFetchingUrl(false);
        }
    };

    // Crawl — async: enqueue a worker job and poll for completion. The
    // crawl no longer runs in the HTTP server process, so this code path
    // gets the jobId back immediately and waits for the worker to publish
    // the completed pages list to Redis.
    const handleCrawl = async () => {
        if (!crawlUrl.trim()) return;
        setCrawling(true);
        try {
            const enqueueRes = await axios.post(
                `${API_BASE_URL}/api/kb/${kb.id}/crawl`,
                { url: crawlUrl.trim(), maxPages: crawlMaxPages },
            );
            const jobId: string | undefined = enqueueRes.data?.jobId;
            if (!jobId) throw new Error('crawl: no jobId returned');

            type CrawlStatus = {
                state: 'active' | 'completed' | 'failed';
                pages?: { title: string; content: string; url: string }[];
                error?: string;
            };

            const deadline = Date.now() + 5 * 60 * 1000;
            let pages: { title: string; content: string; url: string }[] = [];
            while (Date.now() < deadline) {
                await new Promise(r => setTimeout(r, 1500));
                const statusRes = await axios.get<CrawlStatus>(
                    `${API_BASE_URL}/api/kb/${kb.id}/crawl/status/${jobId}`,
                );
                if (statusRes.data.state === 'completed') {
                    pages = statusRes.data.pages || [];
                    break;
                }
                if (statusRes.data.state === 'failed') {
                    throw new Error(statusRes.data.error || 'crawl failed');
                }
            }
            if (pages.length === 0 && Date.now() >= deadline) {
                throw new Error('crawl timed out after 5 minutes');
            }

            if (pages.length > 0) {
                await axios.post(`${API_BASE_URL}/api/kb/${kb.id}/add-sources`, {
                    pages: pages.map(p => ({ title: p.title, content: p.content, url: p.url })),
                });
            }
            setCrawlUrl('');
            toast.success(`${pages.length} ${t('foundPages')}`);
            fetchFiles();
        } catch (err: unknown) {
            toast.error(getApiErrorMessage(err, t('addSourcesFailed')));
        } finally {
            setCrawling(false);
        }
    };

    // RSS
    const fetchRssFeeds = useCallback(async () => {
        try {
            const res = await axios.get(`${API_BASE_URL}/api/kb/${kb.id}/rss`);
            setRssFeeds(res.data);
        } catch { /* ignore */ }
    }, [kb.id]);

    const handleAddRssFeed = async () => {
        if (!rssUrl.trim()) return;
        setRssLoading(true);
        try {
            await axios.post(`${API_BASE_URL}/api/kb/${kb.id}/rss`, { url: rssUrl.trim(), pollInterval: rssPollInterval, fetchFullText: rssFetchFullText });
            setRssUrl('');
            setRssFetchFullText(false);
            toast.success(t('rssFeedAdded'));
            fetchRssFeeds();
        } catch (err: unknown) {
            toast.error(getApiErrorMessage(err, t('addSourcesFailed')));
        } finally {
            setRssLoading(false);
        }
    };

    const handleDeleteRssFeed = async (feedId: string) => {
        const prev = rssFeeds;
        setRssFeeds(rssFeeds.filter(f => f.id !== feedId));
        try {
            await axios.delete(`${API_BASE_URL}/api/kb/${kb.id}/rss/${feedId}`);
            toast.success(t('rssFeedDeleted'));
        } catch {
            setRssFeeds(prev);
            toast.error(t('deleteFileError'));
        }
    };

    // Confluence
    const fetchConfluenceConnection = useCallback(async () => {
        try {
            const res = await axios.get(`${API_BASE_URL}/api/confluence/connections`);
            setConfluenceConnection(res.data);
        } catch { /* ignore */ }
    }, []);

    const fetchConfluenceSources = useCallback(async () => {
        try {
            const res = await axios.get(`${API_BASE_URL}/api/kb/${kb.id}/confluence-sources`);
            setConfluenceSources(res.data);
        } catch { /* ignore */ }
    }, [kb.id]);

    // Initial load (and reload when the KB or language changes — the fetchers
    // are memoized on kb.id/t/toast, so this is equivalent to the old
    // [kb.id]-keyed effect plus a refetch when confluence support flips on).
    useEffect(() => {
        fetchFiles();
        fetchEditors();
        fetchConfigs();
        fetchRssFeeds();
        if (confluenceEnabled) {
            fetchConfluenceConnection();
            fetchConfluenceSources();
        }
    }, [fetchFiles, fetchEditors, fetchConfigs, fetchRssFeeds, confluenceEnabled, fetchConfluenceConnection, fetchConfluenceSources]);

    const handleSaveConfluenceConnection = async (token: string, displayName?: string) => {
        setConfluenceLoading(true);
        try {
            await axios.post(`${API_BASE_URL}/api/confluence/connections`, { pat: token, displayName });
            await fetchConfluenceConnection();
        } finally {
            setConfluenceLoading(false);
        }
    };

    const handleAddConfluenceSource = async (data: { spaceKey: string; rootPageId?: string; rootPageTitle?: string; includeAttachments?: boolean; syncInterval?: number; connectionId: string }) => {
        setConfluenceLoading(true);
        try {
            await axios.post(`${API_BASE_URL}/api/kb/${kb.id}/confluence-sources`, data);
            toast.success(t('confluenceSourceAdded'));
            fetchConfluenceSources();
        } catch (err: unknown) {
            toast.error(getApiErrorMessage(err, t('addSourcesFailed')));
        } finally {
            setConfluenceLoading(false);
        }
    };

    const handleDeleteConfluenceSource = async (sourceId: string) => {
        const prev = confluenceSources;
        setConfluenceSources(confluenceSources.filter(s => s.id !== sourceId));
        try {
            await axios.delete(`${API_BASE_URL}/api/kb/${kb.id}/confluence-sources/${sourceId}`);
            toast.success(t('confluenceSourceDeleted'));
        } catch {
            setConfluenceSources(prev);
            toast.error(t('deleteFileError'));
        }
    };

    const handleSyncConfluenceNow = async (sourceId: string) => {
        try {
            await axios.post(`${API_BASE_URL}/api/kb/${kb.id}/confluence-sources/${sourceId}/sync`);
            toast.success(t('confluenceSyncTriggered'));
        } catch (err: unknown) {
            toast.error(getApiErrorMessage(err, t('addSourcesFailed')));
        }
    };

    const handleFetchConfluenceSpaces = async (): Promise<ConfluenceSpace[]> => {
        const res = await axios.get(`${API_BASE_URL}/api/confluence/spaces`);
        return res.data;
    };

    const handleFetchSpacePages = async (spaceKey: string): Promise<ConfluencePage[]> => {
        const res = await axios.get(`${API_BASE_URL}/api/confluence/spaces/${spaceKey}/pages`);
        return res.data;
    };

    const handleFetchPageChildren = async (pageId: string): Promise<ConfluencePage[]> => {
        const res = await axios.get(`${API_BASE_URL}/api/confluence/pages/${pageId}/children`);
        return res.data;
    };

    const handleFetchAllSpacePages = async (spaceKey: string): Promise<ConfluencePageWithPath[]> => {
        const res = await axios.get(`${API_BASE_URL}/api/confluence/spaces/${spaceKey}/pages/all`);
        return Array.isArray(res.data) ? res.data : [];
    };

    const lookupEditor = async () => {
        if (!editorUsername.trim()) return;
        setEditorLoading(true);
        setEditorLookup(null);
        try {
            const res = await axios.get(`${API_BASE_URL}/api/users/${encodeURIComponent(editorUsername.trim())}`);
            setEditorLookup(res.data);
        } catch {
            setEditorLookup(null);
            toast.warning(t('userNotFound'));
        } finally {
            setEditorLoading(false);
        }
    };

    const handleAddEditor = async () => {
        if (!editorLookup) return;
        try {
            await axios.post(`${API_BASE_URL}/api/admin/global-kbs/${kb.id}/editors`, { userId: editorLookup.id });
            setEditorUsername('');
            setEditorLookup(null);
            fetchEditors();
        } catch (err: unknown) {
            console.error('Failed to add editor:', err);
            toast.error(t('addEditorError'));
        }
    };

    const handleRemoveEditor = async (userId: string) => {
        const prev = editors;
        setEditors(editors.filter(e => e.id !== userId));
        try {
            await axios.delete(`${API_BASE_URL}/api/admin/global-kbs/${kb.id}/editors/${userId}`);
        } catch {
            setEditors(prev);
            toast.error(t('removeEditorError'));
        }
    };

    const selectedConfig = availableConfigs.find(c => c.id === aiConfigId);
    const getStatusLabel = (file: FileEntry) => {
        if (file.status === 'processing') {
            return `${t('status.processing')} (${file.progress}%)`;
        }

        switch (file.status) {
            case 'completed':
                return t('status.completed');
            case 'pending':
                return t('status.pending');
            case 'error':
                return t('status.error');
            default:
                return t('status.unknown');
        }
    };

    return (
        <div style={{ height: '100vh', overflow: 'auto', background: 'var(--bg-primary)', color: 'var(--text-primary)' }}>
            {/* Header */}
            <div style={{
                position: 'sticky', top: 0, zIndex: 10,
                display: 'flex', justifyContent: 'space-between', alignItems: 'center',
                padding: '1rem 2rem',
                background: 'var(--bg-secondary)',
                borderBottom: '1px solid var(--border-color)'
            }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: '1rem' }}>
                    <button onClick={onBack} style={{ background: 'none', cursor: 'pointer', color: 'var(--text-secondary)', display: 'flex', alignItems: 'center' }}>
                        <ArrowLeft size={20} />
                    </button>
                    <Globe size={20} color="var(--accent-primary)" />
                    <h1 style={{ margin: 0, fontSize: '1.2rem' }}>{t('globalKbSettings')}: {kb.name}</h1>
                </div>
                <div style={{ display: 'flex', gap: '0.75rem' }}>
                    <button
                        onClick={onOpenKb}
                        style={{
                            padding: '0.5rem 1rem', borderRadius: '8px', border: '1px solid var(--border-color)',
                            background: 'var(--bg-primary)', color: 'var(--text-primary)', cursor: 'pointer',
                            display: 'flex', alignItems: 'center', gap: '6px', fontSize: '0.85rem'
                        }}
                    >
                        <BookOpen size={16} /> {t('openKb')}
                    </button>
                    <button
                        onClick={handleSave}
                        disabled={saving}
                        style={{
                            padding: '0.5rem 1.5rem', borderRadius: '8px', border: 'none',
                            background: 'var(--accent-primary)', color: 'white', cursor: 'pointer',
                            display: 'flex', alignItems: 'center', gap: '6px', fontSize: '0.85rem',
                            opacity: saving ? 0.7 : 1
                        }}
                    >
                        <span className="icon-swap" key={saving ? 'loading' : 'save'}>{saving ? <Loader2 size={16} className="animate-spin" /> : <Save size={16} />}</span>
                        {t('save')}
                    </button>
                </div>
            </div>

            {/* Content */}
            <div style={{ maxWidth: '900px', margin: '2rem auto', padding: '0 2rem', display: 'flex', flexDirection: 'column', gap: '2rem' }}>

                {/* General Info */}
                <section style={{ background: 'var(--bg-secondary)', borderRadius: '12px', padding: '1.5rem', border: '1px solid var(--border-color)' }}>
                    <h2 style={{ margin: '0 0 1rem', fontSize: '1rem', display: 'flex', alignItems: 'center', gap: '8px' }}>
                        <Globe size={18} /> {t('generalInfo')}
                    </h2>
                    <div style={{ marginBottom: '1rem' }}>
                        <label style={{ display: 'block', fontSize: '0.8rem', fontWeight: 600, color: 'var(--text-secondary)', marginBottom: '0.25rem' }}>{t('kbName')}</label>
                        <input
                            type="text"
                            value={name}
                            onChange={(e) => { setName(e.target.value); clearError('name'); }}
                            style={{ width: '100%', padding: '0.5rem', borderRadius: '6px', border: '1px solid var(--border-color)', background: 'var(--bg-primary)', color: 'var(--text-primary)', boxSizing: 'border-box' }}
                        />
                        {errors.name && <span className="field-error" role="alert">{errors.name}</span>}
                    </div>
                    <div>
                        <label style={{ display: 'block', fontSize: '0.8rem', fontWeight: 600, color: 'var(--text-secondary)', marginBottom: '0.25rem' }}>{t('visibility')}</label>
                        <button
                            type="button"
                            onClick={() => setIsPublished(!isPublished)}
                            style={{
                                display: 'flex', alignItems: 'center', gap: '8px',
                                padding: '0.75rem', borderRadius: '8px',
                                border: '1px solid var(--border-color)',
                                background: isPublished ? 'var(--tag-bg)' : 'var(--bg-primary)',
                                cursor: 'pointer', color: 'var(--text-primary)', width: '100%'
                            }}
                        >
                            <span className="icon-swap" key={isPublished ? 'on' : 'off'}>{isPublished
                                ? <ToggleRight size={20} color="var(--accent-primary)" />
                                : <ToggleLeft size={20} color="var(--text-secondary)" />}</span>
                            <span style={{ fontSize: '0.85rem', fontWeight: 500 }}>
                                {isPublished ? t('published') : t('unpublished')}
                            </span>
                        </button>
                        <p style={{ fontSize: '0.75rem', color: 'var(--text-secondary)', margin: '0.5rem 0 0' }}>
                            {isPublished ? t('publishedDescription') : t('unpublishedDescription')}
                        </p>
                    </div>
                </section>


                {/* Content Text / Tips */}
                <section style={{ background: 'var(--bg-secondary)', borderRadius: '12px', padding: '1.5rem', border: '1px solid var(--border-color)' }}>
                    <h2 style={{ margin: '0 0 1rem', fontSize: '1rem', display: 'flex', alignItems: 'center', gap: '8px' }}>
                        <FileText size={18} /> {t('headerTextLabel')}
                    </h2>
                    <p style={{ fontSize: '0.8rem', color: 'var(--text-secondary)', margin: '0 0 0.75rem' }}>{t('headerTextDescription')}</p>
                    <textarea
                        value={headerText}
                        onChange={(e) => setHeaderText(e.target.value)}
                        placeholder={t('headerTextPlaceholder')}
                        rows={4}
                        style={{ width: '100%', padding: '0.5rem', borderRadius: '6px', border: '1px solid var(--border-color)', background: 'var(--bg-primary)', color: 'var(--text-primary)', resize: 'vertical', fontFamily: 'inherit', boxSizing: 'border-box' }}
                    />
                </section>

                {/* Example Prompts */}
                <section style={{ background: 'var(--bg-secondary)', borderRadius: '12px', padding: '1.5rem', border: '1px solid var(--border-color)' }}>
                    <h2 style={{ margin: '0 0 1rem', fontSize: '1rem', display: 'flex', alignItems: 'center', gap: '8px' }}>
                        <Sparkles size={18} /> {t('examplePromptsLabel')}
                    </h2>
                    <p style={{ fontSize: '0.8rem', color: 'var(--text-secondary)', margin: '0 0 0.75rem' }}>{t('examplePromptsDescription')}</p>
                    <textarea
                        value={examplePrompts}
                        onChange={(e) => setExamplePrompts(e.target.value)}
                        placeholder={t('examplePromptsPlaceholder')}
                        rows={5}
                        style={{ width: '100%', padding: '0.5rem', borderRadius: '6px', border: '1px solid var(--border-color)', background: 'var(--bg-primary)', color: 'var(--text-primary)', resize: 'vertical', fontFamily: 'inherit', boxSizing: 'border-box' }}
                    />
                </section>

                {/* System Prompt */}
                <section style={{ background: 'var(--bg-secondary)', borderRadius: '12px', padding: '1.5rem', border: '1px solid var(--border-color)' }}>
                    <h2 style={{ margin: '0 0 0.25rem', fontSize: '1rem', display: 'flex', alignItems: 'center', gap: '8px' }}>
                        <Sparkles size={18} /> {t('systemPromptLabel')}
                    </h2>
                    <p style={{ fontSize: '0.8rem', color: 'var(--text-secondary)', margin: '0 0 0.75rem' }}>{t('systemPromptDescription')}</p>
                    <textarea
                        value={systemPrompt}
                        onChange={(e) => setSystemPrompt(e.target.value)}
                        placeholder={t('systemPromptPlaceholder')}
                        maxLength={8000}
                        rows={4}
                        style={{ width: '100%', padding: '0.75rem', borderRadius: '8px', border: '1px solid var(--border-color)', background: 'var(--bg-primary)', color: 'var(--text-primary)', resize: 'vertical', fontFamily: 'inherit' }}
                    />
                    <div style={{ fontSize: '0.75rem', color: 'var(--text-secondary)', textAlign: 'right', marginTop: '0.25rem' }}>
                        {systemPrompt.length} / 8000
                    </div>
                </section>

                {/* Studio Configuration */}
                <section style={{ background: 'var(--bg-secondary)', borderRadius: '12px', padding: '1.5rem', border: '1px solid var(--border-color)' }}>
                    <h2 style={{ margin: '0 0 1rem', fontSize: '1rem', display: 'flex', alignItems: 'center', gap: '8px' }}>
                        <Sparkles size={18} /> {t('studioConfigLabel')}
                    </h2>
                    <p style={{ fontSize: '0.8rem', color: 'var(--text-secondary)', margin: '0 0 0.75rem' }}>{t('studioConfigDescription')}</p>
                    <div style={{ display: 'grid', gridTemplateColumns: 'repeat(2, 1fr)', gap: '0.75rem' }}>
                        {(['cards', 'presentation', 'podcast', 'chart', 'abstract', 'briefingDoc', 'faq', 'studyGuide', 'timeline', 'quiz'] as const).map(key => (
                            <button
                                key={key}
                                type="button"
                                onClick={() => setStudioConfig(prev => ({ ...prev, [key]: !prev[key] }))}
                                style={{
                                    display: 'flex', alignItems: 'center', gap: '8px',
                                    padding: '0.75rem', borderRadius: '8px',
                                    border: '1px solid var(--border-color)',
                                    background: studioConfig[key] ? 'var(--tag-bg)' : 'var(--bg-primary)',
                                    cursor: 'pointer', color: 'var(--text-primary)'
                                }}
                            >
                                <span className="icon-swap" key={studioConfig[key] ? 'on' : 'off'}>{studioConfig[key]
                                    ? <ToggleRight size={20} color="var(--accent-primary)" />
                                    : <ToggleLeft size={20} color="var(--text-secondary)" />}</span>
                                <span style={{ fontSize: '0.85rem', fontWeight: 500 }}>
                                    {t(key === 'cards' ? 'flashcards' : key === 'presentation' ? 'slides' : key)}
                                </span>
                            </button>
                        ))}
                    </div>
                </section>

                {/* Content Sources */}
                <section style={{ background: 'var(--bg-secondary)', borderRadius: '12px', padding: '1.5rem', border: '1px solid var(--border-color)' }}>
                    <h2 style={{ margin: '0 0 1rem', fontSize: '1rem', display: 'flex', alignItems: 'center', gap: '8px' }}>
                        <BookOpen size={18} /> {t('addSources')}
                    </h2>

                    {/* File Upload */}
                    <div style={{ marginBottom: '1.5rem' }}>
                        <h3 style={{ margin: '0 0 0.5rem', fontSize: '0.9rem', display: 'flex', alignItems: 'center', gap: '6px' }}>
                            <Upload size={16} /> {t('uploadFiles')}
                            <span style={{ fontSize: '0.75rem', color: 'var(--text-secondary)', fontWeight: 400 }}>
                                ({files.length} / {MAX_FILES_PER_GLOBAL_KB})
                            </span>
                        </h3>
                        <label
                            style={{
                                display: 'inline-flex', alignItems: 'center', gap: '6px',
                                padding: '0.5rem 1rem', borderRadius: '8px',
                                border: '1px dashed var(--border-color)',
                                background: 'var(--bg-primary)', cursor: 'pointer',
                                color: 'var(--accent-primary)', fontSize: '0.85rem'
                            }}
                        >
                            <span className="icon-swap" key={uploading ? 'loading' : 'plus'}>{uploading ? <Loader2 size={16} className="animate-spin" /> : <Plus size={16} />}</span>
                            {t('uploadFiles')}
                            <input
                                type="file"
                                multiple
                                accept={ACCEPTED_FILE_TYPES}
                                onChange={handleFileUpload}
                                style={{ display: 'none' }}
                                disabled={uploading}
                            />
                        </label>
                    </div>

                    {/* URL Fetch */}
                    <div style={{ marginBottom: '1.5rem' }}>
                        <h3 style={{ margin: '0 0 0.5rem', fontSize: '0.9rem', display: 'flex', alignItems: 'center', gap: '6px' }}>
                            <Link size={16} /> {t('enterUrl')}
                        </h3>
                        <div style={{ display: 'flex', gap: '0.5rem' }}>
                            <input
                                type="url"
                                placeholder="https://example.com/page"
                                value={fetchUrl}
                                onChange={(e) => setFetchUrl(e.target.value)}
                                onKeyDown={(e) => { if (e.key === 'Enter') handleFetchUrl(); }}
                                style={{ flex: 1, padding: '0.5rem', borderRadius: '6px', border: '1px solid var(--border-color)', background: 'var(--bg-primary)', color: 'var(--text-primary)' }}
                            />
                            <button
                                onClick={handleFetchUrl}
                                disabled={fetchingUrl || !fetchUrl.trim()}
                                style={{
                                    padding: '0.5rem 1rem', borderRadius: '6px', border: 'none',
                                    background: 'var(--accent-primary)', color: 'white', cursor: 'pointer',
                                    opacity: fetchingUrl || !fetchUrl.trim() ? 0.6 : 1,
                                    display: 'flex', alignItems: 'center', gap: '4px', fontSize: '0.85rem',
                                }}
                            >
                                {fetchingUrl ? <Loader2 size={14} className="animate-spin" /> : <Plus size={14} />}
                            </button>
                        </div>
                    </div>

                    {/* Crawl */}
                    <div style={{ marginBottom: '1.5rem' }}>
                        <h3 style={{ margin: '0 0 0.5rem', fontSize: '0.9rem', display: 'flex', alignItems: 'center', gap: '6px' }}>
                            <Globe size={16} /> {t('crawl')}
                        </h3>
                        <p style={{ margin: '0 0 0.5rem', fontSize: '0.8rem', color: 'var(--text-secondary)' }}>{t('crawlDesc')}</p>
                        <div style={{ display: 'flex', gap: '0.5rem', marginBottom: '0.5rem' }}>
                            <input
                                type="url"
                                placeholder="https://example.com"
                                value={crawlUrl}
                                onChange={(e) => setCrawlUrl(e.target.value)}
                                onKeyDown={(e) => { if (e.key === 'Enter' && !crawling) handleCrawl(); }}
                                style={{ flex: 1, padding: '0.5rem', borderRadius: '6px', border: '1px solid var(--border-color)', background: 'var(--bg-primary)', color: 'var(--text-primary)' }}
                            />
                            <button
                                onClick={handleCrawl}
                                disabled={crawling || !crawlUrl.trim()}
                                style={{
                                    padding: '0.5rem 1rem', borderRadius: '6px', border: 'none',
                                    background: 'var(--accent-primary)', color: 'white', cursor: 'pointer',
                                    opacity: crawling || !crawlUrl.trim() ? 0.6 : 1,
                                    display: 'flex', alignItems: 'center', gap: '4px', fontSize: '0.85rem',
                                }}
                            >
                                {crawling ? <Loader2 size={14} className="animate-spin" /> : <Search size={14} />}
                                {t('startCrawl')}
                            </button>
                        </div>
                        <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', fontSize: '0.8rem', color: 'var(--text-secondary)' }}>
                            <label htmlFor="gkb-crawl-max">{t('maxPages')}: {crawlMaxPages}</label>
                            <input
                                id="gkb-crawl-max"
                                type="range" min="1" max="20" step="1"
                                value={crawlMaxPages}
                                onChange={(e) => setCrawlMaxPages(parseInt(e.target.value, 10))}
                                style={{ flex: 1 }}
                            />
                        </div>
                    </div>

                    {/* RSS */}
                    <div style={{ marginBottom: confluenceEnabled ? '1.5rem' : 0 }}>
                        <h3 style={{ margin: '0 0 0.5rem', fontSize: '0.9rem', display: 'flex', alignItems: 'center', gap: '6px' }}>
                            <Rss size={16} /> {t('rss')}
                        </h3>
                        <p style={{ margin: '0 0 0.5rem', fontSize: '0.8rem', color: 'var(--text-secondary)' }}>{t('rssDesc')}</p>
                        <div style={{ display: 'flex', gap: '0.5rem', marginBottom: '0.5rem' }}>
                            <input
                                type="url"
                                placeholder={t('enterFeedUrl')}
                                value={rssUrl}
                                onChange={(e) => setRssUrl(e.target.value)}
                                onKeyDown={(e) => { if (e.key === 'Enter') handleAddRssFeed(); }}
                                style={{ flex: 1, padding: '0.5rem', borderRadius: '6px', border: '1px solid var(--border-color)', background: 'var(--bg-primary)', color: 'var(--text-primary)' }}
                            />
                            <button
                                onClick={handleAddRssFeed}
                                disabled={rssLoading || !rssUrl.trim()}
                                style={{
                                    padding: '0.5rem 1rem', borderRadius: '6px', border: 'none',
                                    background: 'var(--accent-primary)', color: 'white', cursor: 'pointer',
                                    opacity: rssLoading || !rssUrl.trim() ? 0.6 : 1,
                                    display: 'flex', alignItems: 'center', gap: '4px', fontSize: '0.85rem',
                                }}
                            >
                                {rssLoading ? <Loader2 size={14} className="animate-spin" /> : <Plus size={14} />}
                                {t('subscribe')}
                            </button>
                        </div>
                        <div style={{ display: 'flex', alignItems: 'center', gap: '0.5rem', fontSize: '0.8rem', color: 'var(--text-secondary)', marginBottom: '0.5rem' }}>
                            <label htmlFor="gkb-rss-interval">{t('pollInterval')}</label>
                            <select
                                id="gkb-rss-interval"
                                value={rssPollInterval}
                                onChange={(e) => setRssPollInterval(parseInt(e.target.value, 10))}
                                style={{ padding: '0.25rem 0.5rem', borderRadius: '4px', border: '1px solid var(--border-color)', background: 'var(--bg-primary)', color: 'var(--text-primary)', fontSize: '0.8rem' }}
                            >
                                <option value="15">15 {t('minutes')}</option>
                                <option value="30">30 {t('minutes')}</option>
                                <option value="60">1 {t('hours')}</option>
                                <option value="360">6 {t('hours')}</option>
                                <option value="720">12 {t('hours')}</option>
                                <option value="1440">24 {t('hours')}</option>
                            </select>
                        </div>
                        <label style={{ display: 'flex', alignItems: 'flex-start', gap: '0.5rem', fontSize: '0.8rem', color: 'var(--text-secondary)', marginBottom: '0.5rem' }}>
                            <input
                                type="checkbox"
                                checked={rssFetchFullText}
                                onChange={(e) => setRssFetchFullText(e.target.checked)}
                            />
                            <span>
                                {t('rssFetchFullText')}
                                <span style={{ display: 'block', fontSize: '0.7rem' }}>{t('rssFetchFullTextHint')}</span>
                            </span>
                        </label>
                        {rssFeeds.length > 0 && (
                            <div style={{ display: 'flex', flexDirection: 'column', gap: '0.25rem' }}>
                                {rssFeeds.map(feed => (
                                    <div key={feed.id} style={{
                                        display: 'flex', justifyContent: 'space-between', alignItems: 'center',
                                        padding: '0.4rem 0.6rem', borderRadius: '6px',
                                        background: 'var(--bg-primary)', border: '1px solid var(--border-color)',
                                    }}>
                                        <div style={{ flex: 1, minWidth: 0 }}>
                                            <div style={{ fontSize: '0.8rem', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{feed.title || feed.url}</div>
                                            <div style={{ fontSize: '0.7rem', color: 'var(--text-secondary)' }}>{feed.status} · {feed.itemCount} {t('rssEntries')}</div>
                                        </div>
                                        <button
                                            onClick={() => handleDeleteRssFeed(feed.id)}
                                            style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text-secondary)', padding: '4px' }}
                                        >
                                            <Trash2 size={14} />
                                        </button>
                                    </div>
                                ))}
                            </div>
                        )}
                    </div>

                    {/* Confluence */}
                    {confluenceEnabled && (
                        <div>
                            <h3 style={{ margin: '0 0 0.5rem', fontSize: '0.9rem', display: 'flex', alignItems: 'center', gap: '6px' }}>
                                <BookOpen size={16} /> {t('confluence')}
                            </h3>
                            <button
                                onClick={() => setShowConfluenceModal(true)}
                                style={{
                                    padding: '0.5rem 1rem', borderRadius: '8px',
                                    border: '1px dashed var(--border-color)',
                                    background: 'var(--bg-primary)', cursor: 'pointer',
                                    color: 'var(--accent-primary)', fontSize: '0.85rem',
                                    display: 'inline-flex', alignItems: 'center', gap: '6px',
                                }}
                            >
                                <Plus size={14} /> {t('addConfluenceSource')}
                            </button>
                            {confluenceSources.length > 0 && (
                                <div style={{ display: 'flex', flexDirection: 'column', gap: '0.25rem', marginTop: '0.5rem' }}>
                                    {confluenceSources.map(source => (
                                        <div key={source.id} style={{
                                            display: 'flex', justifyContent: 'space-between', alignItems: 'center',
                                            padding: '0.4rem 0.6rem', borderRadius: '6px',
                                            background: 'var(--bg-primary)', border: '1px solid var(--border-color)',
                                        }}>
                                            <div style={{ flex: 1, minWidth: 0 }}>
                                                <div style={{ fontSize: '0.8rem', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                                                    {source.spaceKey}{source.rootPageTitle ? ` / ${source.rootPageTitle}` : ''}
                                                </div>
                                                <div style={{ fontSize: '0.7rem', color: 'var(--text-secondary)' }}>{source.status} · {source.pageCount} pages</div>
                                            </div>
                                            <div style={{ display: 'flex', gap: '4px' }}>
                                                <button
                                                    onClick={() => handleSyncConfluenceNow(source.id)}
                                                    style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--accent-primary)', padding: '4px', fontSize: '0.7rem' }}
                                                    title={t('confluenceSyncTriggered')}
                                                >
                                                    Sync
                                                </button>
                                                <button
                                                    onClick={() => handleDeleteConfluenceSource(source.id)}
                                                    style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text-secondary)', padding: '4px' }}
                                                >
                                                    <Trash2 size={14} />
                                                </button>
                                            </div>
                                        </div>
                                    ))}
                                </div>
                            )}
                        </div>
                    )}
                </section>

                {/* Files list */}
                <section style={{ background: 'var(--bg-secondary)', borderRadius: '12px', padding: '1.5rem', border: '1px solid var(--border-color)' }}>
                    <h2 style={{ margin: '0 0 1rem', fontSize: '1rem', display: 'flex', alignItems: 'center', gap: '8px' }}>
                        <FileText size={18} /> {t('fileManagement')}
                        <span style={{ fontSize: '0.75rem', color: 'var(--text-secondary)', fontWeight: 400 }}>
                            ({files.length})
                        </span>
                    </h2>
                    <div style={{ maxHeight: '300px', overflowY: 'auto', display: 'flex', flexDirection: 'column', gap: '0.5rem' }}>
                        {files.map(file => (
                            <div key={file.id} style={{
                                display: 'flex', justifyContent: 'space-between', alignItems: 'center',
                                padding: '0.5rem 0.75rem', borderRadius: '6px',
                                background: 'var(--bg-primary)', border: '1px solid var(--border-color)'
                            }}>
                                <div style={{ flex: 1, minWidth: 0 }}>
                                    <div style={{ fontSize: '0.85rem', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{file.name}</div>
                                    <div style={{ fontSize: '0.75rem', color: 'var(--text-secondary)' }}>
                                        {getStatusLabel(file)}
                                    </div>
                                </div>
                                <button
                                    onClick={() => handleDeleteFile(file.id)}
                                    style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text-secondary)', padding: '4px' }}
                                >
                                    <Trash2 size={14} />
                                </button>
                            </div>
                        ))}
                        {files.length === 0 && (
                            <div style={{ textAlign: 'center', padding: '1rem', color: 'var(--text-secondary)', fontSize: '0.85rem' }}>
                                {t('noFiles')}
                            </div>
                        )}
                    </div>
                </section>

                {/* Editors */}
                <section style={{ background: 'var(--bg-secondary)', borderRadius: '12px', padding: '1.5rem', border: '1px solid var(--border-color)' }}>
                    <h2 style={{ margin: '0 0 1rem', fontSize: '1rem', display: 'flex', alignItems: 'center', gap: '8px' }}>
                        <Users size={18} /> {t('editorsLabel')}
                    </h2>
                    <p style={{ fontSize: '0.8rem', color: 'var(--text-secondary)', margin: '0 0 0.75rem' }}>{t('editorsDescription')}</p>

                    <div style={{ display: 'flex', gap: '0.5rem', marginBottom: '1rem' }}>
                        <input
                            type="text"
                            value={editorUsername}
                            onChange={(e) => { setEditorUsername(e.target.value); setEditorLookup(null); }}
                            placeholder={t('editorUsernamePlaceholder')}
                            onKeyDown={(e) => e.key === 'Enter' && lookupEditor()}
                            style={{ flex: 1, padding: '0.5rem', borderRadius: '6px', border: '1px solid var(--border-color)', background: 'var(--bg-primary)', color: 'var(--text-primary)' }}
                        />
                        <button
                            onClick={lookupEditor}
                            disabled={editorLoading || !editorUsername.trim()}
                            style={{
                                padding: '0.5rem 1rem', borderRadius: '6px', border: '1px solid var(--border-color)',
                                background: 'var(--bg-primary)', color: 'var(--text-primary)', cursor: 'pointer'
                            }}
                        >
                            {editorLoading ? <Loader2 size={16} className="animate-spin" /> : t('search')}
                        </button>
                    </div>

                    {editorLookup && (
                        <div style={{
                            display: 'flex', justifyContent: 'space-between', alignItems: 'center',
                            padding: '0.75rem', borderRadius: '8px', marginBottom: '1rem',
                            background: 'var(--tag-bg)', border: '1px solid var(--accent-primary)'
                        }}>
                            <span style={{ fontSize: '0.85rem' }}>
                                @{editorLookup.username}
                                {editorLookup.firstName && ` (${editorLookup.firstName} ${editorLookup.lastName || ''})`}
                            </span>
                            <button
                                onClick={handleAddEditor}
                                style={{
                                    padding: '0.25rem 0.75rem', borderRadius: '6px', border: 'none',
                                    background: 'var(--accent-primary)', color: 'white', cursor: 'pointer', fontSize: '0.8rem'
                                }}
                            >
                                {t('addEditor')}
                            </button>
                        </div>
                    )}

                    <div style={{ display: 'flex', flexDirection: 'column', gap: '0.5rem' }}>
                        {editors.map(editor => (
                            <div key={editor.id} style={{
                                display: 'flex', justifyContent: 'space-between', alignItems: 'center',
                                padding: '0.5rem 0.75rem', borderRadius: '6px',
                                background: 'var(--bg-primary)', border: '1px solid var(--border-color)'
                            }}>
                                <div>
                                    <span style={{ fontSize: '0.85rem', fontWeight: 500 }}>@{editor.username}</span>
                                    {editor.firstName && (
                                        <span style={{ fontSize: '0.8rem', color: 'var(--text-secondary)', marginLeft: '0.5rem' }}>
                                            ({editor.firstName} {editor.lastName || ''})
                                        </span>
                                    )}
                                </div>
                                <button
                                    onClick={() => handleRemoveEditor(editor.id)}
                                    style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text-secondary)', padding: '4px' }}
                                    title={t('removeEditor')}
                                >
                                    <X size={16} />
                                </button>
                            </div>
                        ))}
                        {editors.length === 0 && (
                            <div style={{ textAlign: 'center', padding: '1rem', color: 'var(--text-secondary)', fontSize: '0.85rem' }}>
                                {t('noEditors')}
                            </div>
                        )}
                    </div>
                </section>

                {/* AI Configuration */}
                <section style={{ background: 'var(--bg-secondary)', borderRadius: '12px', padding: '1.5rem', border: '1px solid var(--border-color)' }}>
                    <h2 style={{ margin: '0 0 1rem', fontSize: '1rem', display: 'flex', alignItems: 'center', gap: '8px' }}>
                        <Sparkles size={18} /> {t('aiConfiguration')}
                    </h2>
                    <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(200px, 1fr))', gap: '1rem' }}>
                        <div>
                            <label htmlFor="gkb-ai-provider" style={{ display: 'block', fontSize: '0.75rem', fontWeight: 600, color: 'var(--text-secondary)', marginBottom: '0.25rem' }}>KI-Provider</label>
                            <select
                                id="gkb-ai-provider"
                                value={aiConfigId}
                                onChange={(e) => { setAiConfigId(e.target.value); setChatModel(''); setEmbeddingModel(''); setRerankModel(''); setTtsModel(''); }}
                                style={{ width: '100%', padding: '0.5rem', borderRadius: '6px', border: '1px solid var(--border-color)', background: 'var(--bg-primary)', color: 'var(--text-primary)' }}
                            >
                                <option value="">{t('defaultProvider')}</option>
                                {availableConfigs.map(c => (
                                    <option key={c.id} value={c.id}>{c.name} ({c.provider})</option>
                                ))}
                            </select>
                        </div>
                        {selectedConfig && (
                            <>
                                <div>
                                    <label htmlFor="gkb-chat-model" style={{ display: 'block', fontSize: '0.75rem', fontWeight: 600, color: 'var(--text-secondary)', marginBottom: '0.25rem' }}>Chat-Modell</label>
                                    <select id="gkb-chat-model" value={chatModel} onChange={(e) => setChatModel(e.target.value)} style={{ width: '100%', padding: '0.5rem', borderRadius: '6px', border: '1px solid var(--border-color)', background: 'var(--bg-primary)', color: 'var(--text-primary)' }}>
                                        <option value="">Standard</option>
                                        {selectedConfig.chat_models.map(m => <option key={m} value={m}>{m}</option>)}
                                    </select>
                                </div>
                                <div>
                                    <label htmlFor="gkb-embedding-model" style={{ display: 'block', fontSize: '0.75rem', fontWeight: 600, color: 'var(--text-secondary)', marginBottom: '0.25rem' }}>Embedding-Modell</label>
                                    <select id="gkb-embedding-model" value={embeddingModel} onChange={(e) => setEmbeddingModel(e.target.value)} style={{ width: '100%', padding: '0.5rem', borderRadius: '6px', border: '1px solid var(--border-color)', background: 'var(--bg-primary)', color: 'var(--text-primary)' }}>
                                        <option value="">Standard</option>
                                        {selectedConfig.embedding_models.map(m => <option key={m} value={m}>{m}</option>)}
                                    </select>
                                </div>
                                <div>
                                    <label htmlFor="gkb-rerank-model" style={{ display: 'block', fontSize: '0.75rem', fontWeight: 600, color: 'var(--text-secondary)', marginBottom: '0.25rem' }}>Rerank-Modell</label>
                                    <select id="gkb-rerank-model" value={rerankModel} onChange={(e) => setRerankModel(e.target.value)} style={{ width: '100%', padding: '0.5rem', borderRadius: '6px', border: '1px solid var(--border-color)', background: 'var(--bg-primary)', color: 'var(--text-primary)' }}>
                                        <option value="">Standard</option>
                                        {selectedConfig.rerank_models.map(m => <option key={m} value={m}>{m}</option>)}
                                    </select>
                                </div>
                                <div>
                                    <label htmlFor="gkb-tts-model" style={{ display: 'block', fontSize: '0.75rem', fontWeight: 600, color: 'var(--text-secondary)', marginBottom: '0.25rem' }}>TTS-Modell</label>
                                    <select id="gkb-tts-model" value={ttsModel} onChange={(e) => setTtsModel(e.target.value)} style={{ width: '100%', padding: '0.5rem', borderRadius: '6px', border: '1px solid var(--border-color)', background: 'var(--bg-primary)', color: 'var(--text-primary)' }}>
                                        <option value="">Keines</option>
                                        {selectedConfig.tts_models.map(m => <option key={m} value={m}>{m}</option>)}
                                    </select>
                                </div>
                            </>
                        )}
                    </div>
                </section>

            </div>

            <ConfluenceModal
                show={showConfluenceModal}
                onClose={() => setShowConfluenceModal(false)}
                confluenceConnection={confluenceConnection}
                confluenceLoading={confluenceLoading}
                onSaveConnection={handleSaveConfluenceConnection}
                onAddSource={(data) => {
                    if (confluenceConnection?.connection?.id) {
                        handleAddConfluenceSource({ ...data, connectionId: confluenceConnection.connection.id });
                    }
                }}
                fetchSpaces={handleFetchConfluenceSpaces}
                fetchSpacePages={handleFetchSpacePages}
                fetchPageChildren={handleFetchPageChildren}
                fetchAllSpacePages={handleFetchAllSpacePages}
            />
        </div >
    );
};
