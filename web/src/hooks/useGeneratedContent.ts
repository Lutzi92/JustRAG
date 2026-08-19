import { useState, useCallback, useEffect, useRef, useMemo } from 'react';
import axios from 'axios';
import type { KnowledgeBase, FileEntry, GeneratedContent } from '../types';
import { API_BASE_URL } from '../api';
import { useTheme } from '../contexts/ThemeContext';
import { useModalContext } from '../contexts/ModalContext';
import { useToast } from '../contexts/ToastContext';
import { getApiErrorMessage } from '../utils/apiError';

interface PodcastProgress {
  step: 'waiting' | 'script' | 'audio' | 'done';
  current?: number;
  total?: number;
  message: string;
}

interface UseGeneratedContentParams {
  currentKb: KnowledgeBase | null;
  files: FileEntry[];
}

export function useGeneratedContent({
  currentKb, files,
}: UseGeneratedContentParams) {
  const { language, t } = useTheme();
  const { showPrompt, showConfirm } = useModalContext();
  const toast = useToast();
  const [generatedContent, setGeneratedContent] = useState<GeneratedContent[]>([]);
  const [generating, setGenerating] = useState(false);
  const [selectedContent, setSelectedContent] = useState<GeneratedContent | null>(null);

  const [showChartGenerationModal, setShowChartGenerationModal] = useState(false);
  const [chartPrompt, setChartPrompt] = useState('');
  const [selectedFileId, setSelectedFileId] = useState('');

  const [showAbstractModal, setShowAbstractModal] = useState(false);
  const [abstractFileId, setAbstractFileId] = useState('');
  const [abstractType, setAbstractType] = useState<'academic' | 'executive'>('academic');

  // Podcast async state
  const [podcastProgress, setPodcastProgress] = useState<PodcastProgress | null>(null);
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);

  // Clean up polling on KB change or unmount
  const currentKbId = currentKb?.id;
  useEffect(() => {
    return () => {
      if (pollRef.current) {
        clearInterval(pollRef.current);
        pollRef.current = null;
      }
      setPodcastProgress(null);
    };
  }, [currentKbId]);

  const fetchGeneratedContent = useCallback(async (kbId: string, signal?: AbortSignal) => {
    try {
      const res = await axios.get(`${API_BASE_URL}/api/kb/${kbId}/generated-content`, { signal });
      setGeneratedContent(res.data);
    } catch (err: unknown) {
      if (axios.isCancel(err)) return;
      console.error('Failed to fetch generated content:', err);
      toast.error(t('contentFetchError'));
    }
  }, [t, toast]);

  const startPodcastPolling = useCallback((kbId: string, jobId: string) => {
    setPodcastProgress({ step: 'waiting', message: t('podcastWaiting') });
    let consecutiveFailures = 0;

    pollRef.current = setInterval(async () => {
      try {
        const res = await axios.get(`${API_BASE_URL}/api/kb/${kbId}/generate/podcast/status/${jobId}`);
        consecutiveFailures = 0;
        const { state, progress, error } = res.data;

        if (progress) {
          setPodcastProgress(progress);
        }

        if (state === 'completed') {
          if (pollRef.current) clearInterval(pollRef.current);
          pollRef.current = null;
          setPodcastProgress(null);
          setGenerating(false);
          fetchGeneratedContent(kbId);
          toast.success(t('successGenerate'));
        } else if (state === 'failed') {
          if (pollRef.current) clearInterval(pollRef.current);
          pollRef.current = null;
          setPodcastProgress(null);
          setGenerating(false);
          toast.error(error || t('podcastFailed'));
        }
      } catch {
        consecutiveFailures++;
        if (consecutiveFailures >= 5) {
          if (pollRef.current) clearInterval(pollRef.current);
          pollRef.current = null;
          setPodcastProgress(null);
          setGenerating(false);
          toast.error(t('podcastPollingFailed'));
        }
      }
    }, 3000);
  }, [t, fetchGeneratedContent, toast]);

  const handleGenerate = useCallback(async (type: 'cards' | 'presentation' | 'podcast' | 'chart' | 'abstract' | 'briefing_doc' | 'faq' | 'study_guide' | 'timeline' | 'quiz') => {
    if (type === 'chart') {
      const dataFileExtensions = ['.xlsx', '.xls', '.csv', '.ods', '.json', '.parquet'];
      const dataFile = files.find(f => dataFileExtensions.some(ext => f.name.toLowerCase().endsWith(ext)));
      if (dataFile) {
        setSelectedFileId(dataFile.id);
      } else {
        const completedFile = files.find(f => f.status === 'completed');
        if (completedFile) setSelectedFileId(completedFile.id);
      }

      setShowChartGenerationModal(true);
      return;
    }

    if (type === 'abstract') {
      const completedFile = files.find(f => f.status === 'completed');
      if (completedFile) setAbstractFileId(completedFile.id);
      setShowAbstractModal(true);
      return;
    }

    const topic = await showPrompt(t('enterGenerationTopic'));
    if (!topic || !currentKb) return;

    setGenerating(true);
    try {
      if (type === 'podcast') {
        // Async: fire-and-forget, then poll (podcasts always in English for now - German TTS voices not available)
        const res = await axios.post(`${API_BASE_URL}/api/kb/${currentKb.id}/generate/podcast`, { topic, language: 'en' });
        const { jobId } = res.data;
        startPodcastPolling(currentKb.id, jobId);
      } else {
        // Sync: wait for completion
        await axios.post(`${API_BASE_URL}/api/kb/${currentKb.id}/generate/${type}`, { topic, language });
        fetchGeneratedContent(currentKb.id);
        toast.success(t('successGenerate'));
        setGenerating(false);
      }
    } catch (err: unknown) {
      console.error('Generation failed:', err);
      toast.error(t('errorGenerate'));
      setGenerating(false);
    }
  }, [currentKb, files, language, showPrompt, t, fetchGeneratedContent, startPodcastPolling, toast]);

  const submitChartGeneration = useCallback(async () => {
    if (!chartPrompt || !currentKb) return;
    setGenerating(true);
    setShowChartGenerationModal(false);
    try {
      await axios.post(`${API_BASE_URL}/api/kb/${currentKb.id}/generate/chart`, {
        topic: chartPrompt,
        fileId: selectedFileId || undefined,
        language
      });
      fetchGeneratedContent(currentKb.id);
      toast.success(t('chartSuccess'));
      setChartPrompt('');
      setSelectedFileId('');
    } catch (err: unknown) {
      console.error('Generation failed:', err);
      toast.error(getApiErrorMessage(err, t('errorGenerate')));
    } finally {
      setGenerating(false);
    }
  }, [chartPrompt, currentKb, selectedFileId, language, fetchGeneratedContent, t, toast]);

  const submitAbstractGeneration = useCallback(async () => {
    if (!abstractFileId || !currentKb) return;
    setGenerating(true);
    setShowAbstractModal(false);
    try {
      await axios.post(`${API_BASE_URL}/api/kb/${currentKb.id}/generate/abstract`, {
        fileId: abstractFileId,
        abstractType,
        language,
      });
      fetchGeneratedContent(currentKb.id);
      toast.success(t('abstractSuccess'));
      setAbstractFileId('');
      setAbstractType('academic');
    } catch (err: unknown) {
      console.error('Abstract generation failed:', err);
      toast.error(getApiErrorMessage(err, t('errorGenerate')));
    } finally {
      setGenerating(false);
    }
  }, [abstractFileId, abstractType, currentKb, language, fetchGeneratedContent, t, toast]);

  const handleDeleteGeneratedContent = useCallback(async (id: string, e: React.MouseEvent) => {
    e.stopPropagation();
    if (!await showConfirm(t('confirmDeleteContent'))) return;
    let snapshot: GeneratedContent[] = [];
    setGeneratedContent(prev => { snapshot = prev; return prev.filter(c => c.id !== id); });
    try {
      await axios.delete(`${API_BASE_URL}/api/generated-content/${id}`);
    } catch {
      setGeneratedContent(snapshot);
      toast.error(t('deleteContentError'));
    }
  }, [showConfirm, t, toast]);

  return useMemo(() => ({
    generatedContent, setGeneratedContent, generating,
    selectedContent, setSelectedContent,
    showChartGenerationModal, setShowChartGenerationModal,
    chartPrompt, setChartPrompt,
    selectedFileId, setSelectedFileId,
    fetchGeneratedContent, handleGenerate, submitChartGeneration, handleDeleteGeneratedContent,
    podcastProgress,
    showAbstractModal, setShowAbstractModal,
    abstractFileId, setAbstractFileId,
    abstractType, setAbstractType,
    submitAbstractGeneration,
  }), [
    generatedContent, generating,
    selectedContent,
    showChartGenerationModal, chartPrompt, selectedFileId,
    fetchGeneratedContent, handleGenerate, submitChartGeneration, handleDeleteGeneratedContent,
    podcastProgress,
    showAbstractModal, abstractFileId, abstractType,
    submitAbstractGeneration,
  ]);
}
