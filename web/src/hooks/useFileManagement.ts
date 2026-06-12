import { useState, useRef, useCallback, useMemo, useEffect } from 'react';
import axios from 'axios';
import type { KnowledgeBase, FileEntry } from '../types';
import { API_BASE_URL } from '../api';
import { MAX_FILES_PER_KB, MAX_FILES_PER_GLOBAL_KB } from '../constants';
import { useModalContext } from '../contexts/ModalContext';
import { useTheme } from '../contexts/ThemeContext';
import { useToast } from '../contexts/ToastContext';
import { getApiErrorMessage } from '../utils/apiError';

interface UseFileManagementParams {
  currentKb: KnowledgeBase | null;
}

export function useFileManagement({ currentKb }: UseFileManagementParams) {
  const { t } = useTheme();
  const { showConfirm } = useModalContext();
  const toast = useToast();
  const [files, setFiles] = useState<FileEntry[]>([]);
  const [uploading, setUploading] = useState(false);
  const [showUploadModal, setShowUploadModal] = useState(false);
  const [isDragging, setIsDragging] = useState(false);
  const [textSourceTitle, setTextSourceTitle] = useState('');
  const [textSourceContent, setTextSourceContent] = useState('');
  const fileInputRef = useRef<HTMLInputElement>(null);

  const hasFiles = useMemo(() => files.some(f => f.status === 'completed'), [files]);
  const selectedFileCount = useMemo(() => files.filter(f => f.selected !== false).length, [files]);

  const fetchFiles = useCallback(async (kbId: string, signal?: AbortSignal) => {
    try {
      // Request a high limit — the chat UI needs the full file list to show
      // accurate counts and let users select files. The backend defaults to
      // limit=50 which truncates KBs with large Confluence imports.
      const res = await axios.get(`${API_BASE_URL}/api/kb/${kbId}/files?limit=10000`, { signal });
      const filesWithSelection = res.data.map((file: FileEntry) => ({
        ...file,
        selected: file.selected !== undefined ? file.selected : true
      }));
      setFiles(filesWithSelection);
    } catch (err: unknown) {
      if (axios.isCancel(err)) return;
      console.error('Failed to fetch files:', err);
      toast.error(t('filesFetchError'));
    }
  }, [t, toast]);

  // Polling for processing files
  const hasProcessingFiles = files.some(f => f.status === 'processing' || f.status === 'pending');
  const currentKbId = currentKb?.id;
  useEffect(() => {
    if (hasProcessingFiles && currentKbId) {
      const interval = setInterval(() => {
        fetchFiles(currentKbId);
      }, 2000);
      return () => clearInterval(interval);
    }
  }, [hasProcessingFiles, currentKbId, fetchFiles]);

  const uploadFilesBatch = useCallback(async (filesToUpload: File[]) => {
    if (!currentKb || filesToUpload.length === 0) return;

    const fileLimit = currentKb.isGlobal ? MAX_FILES_PER_GLOBAL_KB : MAX_FILES_PER_KB;
    const remaining = Math.max(0, fileLimit - files.length);
    const eligible = filesToUpload.slice(0, remaining);

    if (eligible.length < filesToUpload.length) {
      toast.warning(`${t('fileLimitReached')} (${fileLimit})`);
    }
    if (eligible.length === 0) return;

    setUploading(true);
    try {
      const UPLOAD_CONCURRENCY = 3;
      let nextIndex = 0;
      let firstError: unknown = null;
      const worker = async () => {
        while (nextIndex < eligible.length) {
          const file = eligible[nextIndex++];
          const formData = new FormData();
          formData.append('file', file);
          try {
            await axios.post(`${API_BASE_URL}/api/kb/${currentKb.id}/files`, formData);
          } catch (err: unknown) {
            console.error('Upload failed:', err);
            if (firstError === null) firstError = err;
          }
        }
      };
      const workerCount = Math.min(UPLOAD_CONCURRENCY, eligible.length);
      await Promise.all(Array.from({ length: workerCount }, worker));
      if (firstError !== null) {
        toast.error(getApiErrorMessage(firstError, t('uploadFailed')));
      }
      await fetchFiles(currentKb.id);
    } finally {
      setUploading(false);
    }
  }, [currentKb, files.length, fetchFiles, t, toast]);

  const handleFileUpload = useCallback(async (e: React.ChangeEvent<HTMLInputElement>) => {
    const selectedFiles = Array.from(e.target.files || []);
    if (selectedFiles.length === 0 || !currentKb) return;
    setShowUploadModal(false);
    await uploadFilesBatch(selectedFiles);
    if (fileInputRef.current) fileInputRef.current.value = '';
  }, [currentKb, uploadFilesBatch]);

  const handleDeleteFile = useCallback(async (fileId: string, e: React.MouseEvent) => {
    e.stopPropagation();
    if (!await showConfirm(t('confirmDeleteFile'))) return;
    let snapshot: FileEntry[] = [];
    setFiles(prev => { snapshot = prev; return prev.filter(f => f.id !== fileId); });
    try {
      await axios.delete(`${API_BASE_URL}/api/files/${fileId}`);
    } catch {
      setFiles(snapshot);
      toast.error(t('deleteFileError'));
    }
  }, [showConfirm, t, toast]);

  const retryFile = useCallback(async (fileId: string) => {
    try {
      await axios.post(`${API_BASE_URL}/api/files/${fileId}/retry`);
      // Flip to pending locally — this re-arms the 2s polling effect above.
      setFiles(prev => prev.map(f =>
        f.id === fileId
          ? { ...f, status: 'pending' as const, progress: 0, errorStage: undefined, errorMessage: undefined }
          : f
      ));
    } catch (err: unknown) {
      console.error('Retry failed:', err);
      toast.error(getApiErrorMessage(err, t('retryRequestFailed')));
    }
  }, [t, toast]);

  const retryAllFailed = useCallback(async () => {
    if (!currentKb) return;
    try {
      await axios.post(`${API_BASE_URL}/api/kb/${currentKb.id}/files/retry-failed`);
      await fetchFiles(currentKb.id);
    } catch (err: unknown) {
      console.error('Retry all failed:', err);
      toast.error(getApiErrorMessage(err, t('retryRequestFailed')));
    }
  }, [currentKb, fetchFiles, t, toast]);

  const handleToggleFileSelection = useCallback((fileId: string, e: React.SyntheticEvent) => {
    e.stopPropagation();
    setFiles(prev => prev.map(f =>
      f.id === fileId ? { ...f, selected: !f.selected } : f
    ));
  }, []);

  const handleToggleFilesSelection = useCallback((fileIds: string[], selected: boolean) => {
    setFiles(prev => prev.map(f =>
      fileIds.includes(f.id) ? { ...f, selected } : f
    ));
  }, []);

  const handleDownloadFile = useCallback(async (fileId: string) => {
    try {
      const res = await axios.get(`${API_BASE_URL}/api/files/${fileId}/download`, {
        responseType: 'blob'
      });
      const url = window.URL.createObjectURL(new Blob([res.data]));
      const link = document.createElement('a');
      link.href = url;

      const contentDisposition = res.headers['content-disposition'];
      let fileName = files.find(f => f.id === fileId)?.name || 'download';
      if (contentDisposition) {
        const match = contentDisposition.match(/filename="(.+)"/);
        if (match && match[1]) fileName = match[1];
      }

      link.setAttribute('download', fileName);
      document.body.appendChild(link);
      link.click();
      link.remove();
      window.URL.revokeObjectURL(url);
    } catch (err: unknown) {
      console.error('Failed to download file:', err);
      toast.error(t('downloadFileError'));
    }
  }, [files, t, toast]);

  const openUploadModal = useCallback(() => {
    setTextSourceTitle('');
    setTextSourceContent('');
    setShowUploadModal(true);
  }, []);

  const handleDragOver = useCallback((e: React.DragEvent) => {
    e.preventDefault();
    e.stopPropagation();
  }, []);

  const handleDragEnter = useCallback((e: React.DragEvent) => {
    e.preventDefault();
    e.stopPropagation();
    setIsDragging(true);
  }, []);

  const handleDragLeave = useCallback((e: React.DragEvent) => {
    e.preventDefault();
    e.stopPropagation();
    setIsDragging(false);
  }, []);

  const handleDrop = useCallback(async (e: React.DragEvent) => {
    e.preventDefault();
    e.stopPropagation();
    setIsDragging(false);

    const droppedFiles = Array.from(e.dataTransfer.files);
    if (droppedFiles.length === 0 || !currentKb) return;
    setShowUploadModal(false);
    await uploadFilesBatch(droppedFiles);
  }, [currentKb, uploadFilesBatch]);

  const handleTextSourceAdd = useCallback(async () => {
    if (!currentKb || !textSourceContent.trim()) return;

    setUploading(true);
    setShowUploadModal(false);

    try {
      await axios.post(`${API_BASE_URL}/api/kb/${currentKb.id}/text`, {
        title: textSourceTitle.trim() || undefined,
        content: textSourceContent
      });
      await fetchFiles(currentKb.id);
      setTextSourceTitle('');
      setTextSourceContent('');
    } catch (err: unknown) {
      console.error('Text source add failed:', err);
      toast.error(getApiErrorMessage(err, t('addTextFailed')));
    } finally {
      setUploading(false);
    }
  }, [currentKb, textSourceTitle, textSourceContent, fetchFiles, t, toast]);

  return useMemo(() => ({
    files, uploading, hasFiles, selectedFileCount, fileInputRef,
    fetchFiles, handleFileUpload, handleDeleteFile, handleToggleFileSelection, handleToggleFilesSelection,
    handleDownloadFile, openUploadModal, retryFile, retryAllFailed,
    showUploadModal, setShowUploadModal,
    isDragging, textSourceTitle, setTextSourceTitle,
    textSourceContent, setTextSourceContent,
    handleDragOver, handleDragEnter, handleDragLeave, handleDrop, handleTextSourceAdd,
  }), [
    files, uploading, hasFiles, selectedFileCount,
    showUploadModal, isDragging, textSourceTitle, textSourceContent,
    fetchFiles, handleFileUpload, handleDeleteFile, handleToggleFileSelection, handleToggleFilesSelection,
    handleDownloadFile, openUploadModal, retryFile, retryAllFailed,
    handleDragOver, handleDragEnter, handleDragLeave, handleDrop, handleTextSourceAdd
  ]);
}
