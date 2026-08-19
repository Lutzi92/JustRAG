import { renderHook, act, waitFor } from '@testing-library/react';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import axios from 'axios';
import { useGeneratedContent } from './useGeneratedContent';
import type { FileEntry, KnowledgeBase } from '../types';

vi.mock('axios');
vi.mock('../contexts/ThemeContext', () => ({ useTheme: () => ({ t: (k: string) => k, language: 'de' }) }));
vi.mock('../contexts/ToastContext', () => ({ useToast: () => ({ error: vi.fn(), success: vi.fn() }) }));

const showPrompt = vi.fn();
vi.mock('../contexts/ModalContext', () => ({
  useModalContext: () => ({ showPrompt: (...a: unknown[]) => showPrompt(...a), showConfirm: vi.fn() }),
}));

const kb = { id: 'kb1', name: 'KB' } as KnowledgeBase;
const files = [{ id: 'f1', name: 'a.pdf', status: 'completed' }] as FileEntry[];

const faqRecord = {
  id: 'g-faq', kbId: 'kb1', userId: 'u1', title: 'FAQ zu X',
  type: 'faq', content: { text: '# FAQ' }, createdAt: '2026-08-19T10:00:00Z',
};

beforeEach(() => {
  vi.clearAllMocks();
  showPrompt.mockResolvedValue('Mein Thema');
});

describe('useGeneratedContent: erzeugter Inhalt wird geöffnet', () => {
  it('wählt das erzeugte Artefakt aus, sobald die Generierung fertig ist', async () => {
    vi.mocked(axios.post).mockResolvedValue({ data: faqRecord });
    vi.mocked(axios.get).mockResolvedValue({ data: [faqRecord] });

    const { result } = renderHook(() => useGeneratedContent({ currentKb: kb, files }));
    await act(async () => { await result.current.handleGenerate('faq'); });

    await waitFor(() => {
      expect(result.current.selectedContent).toEqual(expect.objectContaining({ id: 'g-faq' }));
    });
  });

  it('lässt die Auswahl unangetastet, wenn die Generierung fehlschlägt', async () => {
    vi.mocked(axios.post).mockRejectedValue(new Error('boom'));
    vi.mocked(axios.get).mockResolvedValue({ data: [] });

    const { result } = renderHook(() => useGeneratedContent({ currentKb: kb, files }));
    await act(async () => { await result.current.handleGenerate('faq'); });

    expect(result.current.selectedContent).toBeNull();
  });
});

describe('useGeneratedContent: Podcast', () => {
  it('öffnet den fertigen Podcast, obwohl der Status-Endpoint keinen Datensatz liefert', async () => {
    vi.useFakeTimers();
    const podcast = {
      id: 'g-pod', kbId: 'kb1', userId: 'u1', title: 'Podcast',
      type: 'podcast', content: {}, createdAt: '2026-08-19T12:00:00Z',
    };
    const alt = { ...podcast, id: 'g-old', createdAt: '2026-08-01T12:00:00Z' };
    vi.mocked(axios.post).mockResolvedValue({ data: { jobId: 'j1' } });
    vi.mocked(axios.get).mockImplementation((url: string) =>
      url.includes('/status/')
        ? Promise.resolve({ data: { state: 'completed', progress: null } })
        : Promise.resolve({ data: [alt, podcast] }),
    );

    const { result } = renderHook(() => useGeneratedContent({ currentKb: kb, files }));
    await act(async () => { await result.current.handleGenerate('podcast'); });
    await act(async () => { await vi.advanceTimersByTimeAsync(3100); });

    // The newest podcast wins — not simply the first entry of the list.
    expect(result.current.selectedContent).toEqual(expect.objectContaining({ id: 'g-pod' }));
    vi.useRealTimers();
  });
});
