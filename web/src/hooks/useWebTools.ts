import { useState, useRef, useCallback, useMemo, useEffect } from 'react';
import axios from 'axios';
import type { KnowledgeBase } from '../types';
import { API_BASE_URL, authFetch } from '../api';
import { useTheme } from '../contexts/ThemeContext';
import { useToast } from '../contexts/ToastContext';
import { parseSseStream } from '../utils/sseParser';
import { getApiErrorMessage } from '../utils/apiError';

interface UseWebToolsParams {
  currentKb: KnowledgeBase | null;
  fetchFiles: (kbId: string) => Promise<void>;
}

export function useWebTools({ currentKb, fetchFiles }: UseWebToolsParams) {
  const { language, t } = useTheme();
  const toast = useToast();
  const [toolTab, setToolTab] = useState<'websearch' | 'crawl' | 'research' | 'rss'>('websearch');
  const [toolInput, setToolInput] = useState('');
  const [crawlMaxPages, setCrawlMaxPages] = useState(1);
  const [searchResultsCount, setSearchResultsCount] = useState(5);
  const [crawlResults, setCrawlResults] = useState<{ title: string; content: string; url: string; isEdited?: boolean }[]>([]);
  const [searchResults, setSearchResults] = useState<{ title: string; snippet: string; url: string; content?: string; isEdited?: boolean }[]>([]);
  const [selectedPages, setSelectedPages] = useState<Set<string>>(new Set());
  const [previewPage, setPreviewPage] = useState<{ title: string; content: string; url: string } | null>(null);
  const [showPreviewModal, setShowPreviewModal] = useState(false);
  const [pdfPreview, setPdfPreview] = useState<{ fileId: string; fileName: string; page: number } | null>(null);
  const [showPdfPreview, setShowPdfPreview] = useState(false);
  const [toolLoading, setToolLoading] = useState(false);
  const [showWebWorkspace, setShowWebWorkspace] = useState(false);
  const [sourcesAddedCount, setSourcesAddedCount] = useState(0);

  const [webResearchRunning, setWebResearchRunning] = useState(false);
  const [webResearchStatus, setWebResearchStatus] = useState<string | null>(null);
  const [webResearchProgress, setWebResearchProgress] = useState<{ step: number; total: number }>({ step: 0, total: 10 });
  const webResearchAbortRef = useRef<AbortController | null>(null);
  const statusClearTimerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

  // Cleanup timer on unmount
  useEffect(() => {
    return () => {
      clearTimeout(statusClearTimerRef.current);
    };
  }, []);

  const handleWebResearch = useCallback(async () => {
    if (!currentKb || !toolInput.trim()) return;

    const abortController = new AbortController();
    webResearchAbortRef.current = abortController;
    setWebResearchRunning(true);
    setWebResearchStatus(t('researchSearching'));
    setWebResearchProgress({ step: 0, total: 10 });

    try {
      const response = await authFetch(`${API_BASE_URL}/api/kb/${currentKb.id}/web-research?stream=true`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
        },
        body: JSON.stringify({
          topic: toolInput.trim(),
          language,
          maxSteps: 10,
        }),
        signal: abortController.signal,
      });

      if (!response.ok) {
        const err = await response.json().catch(() => ({ error: 'Request failed' }));
        throw new Error(err.error || `HTTP ${response.status}`);
      }

      const reader = response.body?.getReader();
      if (!reader) throw new Error('No response body');

      await parseSseStream(reader, {
        onEvent: (data: unknown) => {
          const event = data as Record<string, unknown>;

          if (event.stepNumber !== undefined && event.totalSteps !== undefined) {
            setWebResearchProgress({ step: event.stepNumber as number, total: event.totalSteps as number });
          }

          switch (event.type) {
            case 'searching':
              setWebResearchStatus(t('researchSearching'));
              break;
            case 'web_fetching':
            case 'reading':
              setWebResearchStatus(t('researchReading'));
              break;
            case 'synthesizing':
              setWebResearchStatus(t('researchSynthesizing'));
              break;
            case 'generating_report':
              setWebResearchStatus(t('researchGenerating'));
              break;
            case 'complete':
              setWebResearchStatus(t('researchComplete'));
              break;
            case 'file_created':
              fetchFiles(currentKb.id);
              break;
            case 'error':
              setWebResearchStatus((event.message as string) || 'Error');
              break;
          }
        },
      });

      setToolInput('');
    } catch (err: unknown) {
      if (err instanceof DOMException && err.name === 'AbortError') {
        setWebResearchStatus(t('researchCancelled'));
      } else {
        console.error('Web research failed:', err);
        const msg = err instanceof Error ? err.message : 'Error';
        setWebResearchStatus(msg);
        toast.error(msg || t('taskFailed'));
      }
    } finally {
      setWebResearchRunning(false);
      webResearchAbortRef.current = null;
      clearTimeout(statusClearTimerRef.current);
      statusClearTimerRef.current = setTimeout(() => {
        setWebResearchStatus(null);
        statusClearTimerRef.current = undefined;
      }, 3000);
    }
  }, [currentKb, toolInput, language, t, toast, fetchFiles]);

  const handleCancelWebResearch = useCallback(() => {
    if (webResearchAbortRef.current) {
      webResearchAbortRef.current.abort();
    }
  }, []);

  const handleToolSubmit = useCallback(async (tabOverride?: 'websearch' | 'crawl' | 'research' | 'rss', inputOverride?: string) => {
    const input = inputOverride ?? toolInput;
    if (!currentKb || !input.trim()) return;
    const tab = tabOverride ?? toolTab;

    if (tab === 'research') {
      handleWebResearch();
      return;
    }

    setToolLoading(true);

    try {
      if (tab === 'websearch') {
        const res = await axios.post(`${API_BASE_URL}/api/kb/${currentKb.id}/websearch`, {
          query: input, limit: searchResultsCount, language: language,
        });
        const results = res.data.results || [];
        setSearchResults(results);
        setSelectedPages(new Set(results.map((r: { url: string }) => r.url)));
        setShowWebWorkspace(true);
      } else if (tab === 'crawl') {
        // Async crawl: enqueue a job, then poll the status endpoint until
        // it reaches a terminal state. The crawl runs in a worker (not
        // the HTTP server) so Chromium doesn't block the request goroutine.
        const enqueueRes = await axios.post(`${API_BASE_URL}/api/kb/${currentKb.id}/crawl`, {
          url: input, maxPages: crawlMaxPages,
        });
        const jobId: string | undefined = enqueueRes.data?.jobId;
        if (!jobId) {
          throw new Error('crawl: no jobId returned');
        }

        // Poll every 1.5s, up to 5 minutes. The worker writes a
        // "completed" state plus the pages list to Redis when done.
        type CrawlStatus = {
          state: 'active' | 'completed' | 'failed';
          progress?: { step?: string; current?: number; total?: number; message?: string };
          pages?: { title: string; url: string; content: string }[];
          error?: string;
        };

        const deadline = Date.now() + 5 * 60 * 1000;
        const pollInterval = 1500;
        let pages: { title: string; content: string; url: string }[] = [];

        while (Date.now() < deadline) {
          await new Promise(r => setTimeout(r, pollInterval));
          const statusRes = await axios.get<CrawlStatus>(
            `${API_BASE_URL}/api/kb/${currentKb.id}/crawl/status/${jobId}`,
          );
          const status = statusRes.data;
          if (status.state === 'completed') {
            pages = status.pages || [];
            break;
          }
          if (status.state === 'failed') {
            throw new Error(status.error || 'crawl failed');
          }
          // else: still active — loop
        }

        if (pages.length === 0 && Date.now() >= deadline) {
          throw new Error('crawl timed out after 5 minutes');
        }

        setCrawlResults(pages);
        setSelectedPages(new Set(pages.map(p => p.url)));
        setShowWebWorkspace(true);
      }
    } catch (err: unknown) {
      console.error(`${tab} failed:`, err);
      toast.error(getApiErrorMessage(err, t('taskFailed')));
    } finally {
      setToolLoading(false);
    }
  }, [currentKb, toolInput, toolTab, handleWebResearch, searchResultsCount, language, crawlMaxPages, toast, t]);

  const handleAddSources = useCallback(async () => {
    if (!currentKb || (selectedPages.size === 0 && toolTab !== 'websearch')) return;
    if (toolTab === 'websearch' && selectedPages.size === 0) return;

    setToolLoading(true);

    try {
      let hadPartialFailure = false;
      if (toolTab === 'websearch') {
        const selectedUrls = searchResults.filter(r => selectedPages.has(r.url));
        const editedResults = selectedUrls.filter(r => r.content);
        const resultsToFetch = selectedUrls.filter(r => !r.content);

        const requests = [];
        if (editedResults.length > 0) {
          requests.push(axios.post(`${API_BASE_URL}/api/kb/${currentKb.id}/add-sources`, { pages: editedResults }));
        }
        resultsToFetch.forEach(res => {
          requests.push(axios.post(`${API_BASE_URL}/api/kb/${currentKb.id}/fetch-url`, { url: res.url }));
        });

        if (requests.length > 0) {
          const results = await Promise.allSettled(requests);
          const failures = results.filter(r => r.status === 'rejected').length;
          if (failures === requests.length) {
            // All failed — treat as total failure, preserve selection
            const firstErr = results.find(r => r.status === 'rejected') as PromiseRejectedResult;
            throw firstErr.reason;
          }
          if (failures > 0) {
            hadPartialFailure = true;
            toast.warning(t('addSourcesPartialFail').replace('{count}', String(failures)));
          }
        }
        setSearchResults([]);
      } else {
        const pagesToAdd = crawlResults.filter(p => selectedPages.has(p.url));
        await axios.post(`${API_BASE_URL}/api/kb/${currentKb.id}/add-sources`, { pages: pagesToAdd });
        setCrawlResults([]);
      }
      setSelectedPages(new Set());
      setToolInput('');
      setShowWebWorkspace(false);
      fetchFiles(currentKb.id);
      if (!hadPartialFailure) toast.success(t('sourcesAddedProcessing'));
      setSourcesAddedCount(c => c + 1);
    } catch (err: unknown) {
      console.error('Add sources failed:', err);
      toast.error(getApiErrorMessage(err, t('addSourcesFailed')));
    } finally {
      setToolLoading(false);
    }
  }, [currentKb, selectedPages, toolTab, searchResults, crawlResults, fetchFiles, t, toast]);

  const handleUpdateWebResult = useCallback(async (url: string, newContent: string) => {
    if (toolTab === 'websearch') {
      setSearchResults(prev => prev.map(r => r.url === url ? { ...r, content: newContent, isEdited: true } : r));
    } else {
      setCrawlResults(prev => prev.map(r => r.url === url ? { ...r, content: newContent, isEdited: true } : r));
    }
  }, [toolTab]);

  const handleStartEditWebResult = useCallback(async (url: string) => {
    const list = toolTab === 'websearch' ? searchResults : crawlResults;
    const item = list.find(r => r.url === url);
    if (!item) return;

    if (toolTab === 'websearch' && !item.content && currentKb) {
      setToolLoading(true);
      const searchItem = item as { title: string; snippet: string; url: string; content?: string; isEdited?: boolean };
      try {
        const res = await axios.post(`${API_BASE_URL}/api/kb/${currentKb.id}/fetch-url`, { url, previewOnly: true });
        const content = res.data.content || searchItem.snippet || '';
        setSearchResults(prev => prev.map(r => r.url === url ? { ...r, content } : r));
      } catch (err: unknown) {
        console.error('Failed to fetch full content, falling back to snippet:', err);
        const fallbackContent = searchItem.snippet || '';
        setSearchResults(prev => prev.map(r => r.url === url ? { ...r, content: fallbackContent } : r));
        toast.warning(t('contentFetchFallback'));
      } finally {
        setToolLoading(false);
      }
    }
  }, [toolTab, searchResults, crawlResults, currentKb, t, toast]);

  const handlePdfSourceOpen = useCallback((fileId: string, fileName: string, page: number) => {
    setPdfPreview({ fileId, fileName, page });
    setShowPdfPreview(true);
  }, []);

  const fetchFileContent = useCallback(async (fileId: string, fileName: string) => {
    try {
      const res = await axios.get(`${API_BASE_URL}/api/files/${fileId}/download`, {
        responseType: 'text'
      });
      setPreviewPage({ title: fileName, content: res.data, url: '' });
      setShowPreviewModal(true);
    } catch (err: unknown) {
      console.error('Failed to fetch file content:', err);
      toast.error(t('previewLoadError'));
    }
  }, [t, toast]);

  const handlePreviewSource = useCallback((fileOrId: import('../types').FileEntry | string, fileName?: string) => {
    const id = typeof fileOrId === 'string' ? fileOrId : fileOrId.id;
    const name = typeof fileOrId === 'string' ? fileName! : fileOrId.name;
    const type = typeof fileOrId === 'string' ? undefined : fileOrId.type;

    const lowerName = name.toLowerCase();
    const isPdf = lowerName.endsWith('.pdf') || type === 'application/pdf';
    const isTextOrMd = lowerName.endsWith('.md') || lowerName.endsWith('.txt') || type === 'text/markdown' || type === 'text/plain';

    if (isPdf) {
      handlePdfSourceOpen(id, name, 1);
    } else if (isTextOrMd) {
      fetchFileContent(id, name);
    } else {
      toast.info(t('previewNotSupported'));
    }
  }, [handlePdfSourceOpen, fetchFileContent, t, toast]);

  return useMemo(() => ({
    toolTab, setToolTab, toolInput, setToolInput,
    crawlMaxPages, setCrawlMaxPages, searchResultsCount, setSearchResultsCount,
    crawlResults, searchResults, selectedPages, setSelectedPages, sourcesAddedCount,
    previewPage, showPreviewModal, setShowPreviewModal,
    pdfPreview, showPdfPreview, setShowPdfPreview,
    toolLoading, showWebWorkspace, setShowWebWorkspace,
    webResearchRunning, webResearchStatus, webResearchProgress,
    handleWebResearch, handleCancelWebResearch, handleToolSubmit,
    handleAddSources, handleUpdateWebResult, handleStartEditWebResult,
    handlePdfSourceOpen, fetchFileContent, handlePreviewSource,
  }), [
    toolTab, toolInput, crawlMaxPages, searchResultsCount,
    crawlResults, searchResults, selectedPages, sourcesAddedCount,
    previewPage, showPreviewModal,
    pdfPreview, showPdfPreview,
    toolLoading, showWebWorkspace,
    webResearchRunning, webResearchStatus, webResearchProgress,
    handleWebResearch, handleCancelWebResearch, handleToolSubmit,
    handleAddSources, handleUpdateWebResult, handleStartEditWebResult,
    handlePdfSourceOpen, fetchFileContent, handlePreviewSource
  ]);
}
