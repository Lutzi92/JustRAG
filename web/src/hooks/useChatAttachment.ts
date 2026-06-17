import { useState, useCallback } from 'react';
import axios from 'axios';
import { API_BASE_URL } from '../api';
import { getApiErrorMessage } from '../utils/apiError';

export interface ChatAttachment {
  attachmentId: string;
  filename: string;
  sectionCount: number;
  charCount: number;
}

/**
 * Optional translator (the `t` from `useTheme()`) so status-specific upload
 * errors are localized. When omitted, English fallbacks are used.
 */
type Translator = (key: string) => string;

/**
 * Manages uploading a single comparison attachment to
 * `POST /api/kb/{kbId}/chat/attachment` (multipart, field `file`) plus the
 * resulting attachment state for a given KB.
 *
 * Auth is handled globally by axios (`axios.defaults.headers.common.Authorization`
 * set in App.tsx), matching the rest of the file-upload hooks.
 */
export function useChatAttachment(kbId: string, t?: Translator) {
  const [attachment, setAttachment] = useState<ChatAttachment | null>(null);
  const [uploading, setUploading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const upload = useCallback(async (file: File) => {
    setUploading(true);
    setError(null);
    try {
      const fd = new FormData();
      fd.append('file', file);
      const res = await axios.post<ChatAttachment>(
        `${API_BASE_URL}/api/kb/${kbId}/chat/attachment`,
        fd,
      );
      setAttachment(res.data);
    } catch (err: unknown) {
      const tr = (key: string, fallback: string) => (t ? t(key) : fallback);
      let msg = tr('comparisonUploadFailed', 'Upload failed');
      if (axios.isAxiosError(err)) {
        switch (err.response?.status) {
          case 503:
            msg = tr('comparisonDisabled', 'Document comparison is disabled');
            break;
          case 413:
            msg = tr('comparisonFileTooLarge', 'File too large');
            break;
          case 415:
            msg = tr('comparisonUnsupportedType', 'Unsupported file type');
            break;
          case 422:
            msg = tr('comparisonNoText', 'Could not extract text from file');
            break;
          default:
            msg = getApiErrorMessage(err, msg);
        }
      } else {
        msg = getApiErrorMessage(err, msg);
      }
      console.error('Attachment upload failed:', err);
      setError(msg);
    } finally {
      setUploading(false);
    }
  }, [kbId, t]);

  const clear = useCallback(() => {
    setAttachment(null);
    setError(null);
  }, []);

  return { attachment, uploading, error, upload, clear };
}
