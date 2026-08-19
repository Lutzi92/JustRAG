import { describe, it, expect, vi, beforeEach } from 'vitest';
import { renderHook, act } from '@testing-library/react';
import { useChat, buildComparisonSend } from './useChat';
import type { KnowledgeBase, FileEntry } from '../types';
import type { ChatAttachment } from './useChatAttachment';

// No pre-existing test harness for useChat (checked: `ls hooks/*.test.ts*`
// before writing this file — no `useChat.test.ts`/`.tsx` in the repo). The
// hook pulls in ThemeContext/ModalContext/ToastContext plus the useChatStream
// and useChatAttachment sub-hooks, so a *minimal* harness is built here
// (mirrors the vi.mock pattern already used in useKnowledgeBases.test.ts /
// useKbRemoval.test.ts) rather than skipping hook-level coverage entirely —
// the `setAgentSelection` wiring the controller ruling calls for is a
// behavioral effect (a callback invocation), not something a pure function
// extraction alone could prove.
//
// `buildComparisonSend` (below) still gets its own pure-function tests: it's
// the one piece of `startComparison`'s logic dense enough to be worth testing
// in isolation from the hook plumbing.

const setAgentSelection = vi.fn();
const streamSend = vi.fn();
const uploadMock = vi.fn();
const clearMock = vi.fn();

vi.mock('../contexts/ThemeContext', () => ({
  useTheme: () => ({ language: 'de', t: (key: string) => key }),
}));
vi.mock('../contexts/ModalContext', () => ({
  useModalContext: () => ({ showConfirm: vi.fn() }),
}));
vi.mock('../contexts/ToastContext', () => ({
  useToast: () => ({ success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() }),
}));
vi.mock('./useChatStream', () => ({
  useChatStream: () => ({
    loading: false,
    handleSendMessage: streamSend,
    cancelStream: vi.fn(),
    chatAbortRef: { current: null },
  }),
}));
vi.mock('./useChatAttachment', () => ({
  useChatAttachment: () => ({
    attachment: null,
    uploading: false,
    error: null,
    upload: uploadMock,
    clear: clearMock,
  }),
}));

const kb = { id: 'kb1', name: 'Handbuch' } as KnowledgeBase;

function renderUseChat() {
  return renderHook(() => useChat({
    currentKb: kb,
    files: [] as FileEntry[],
    enhance: null,
    reasoningEnabled: false,
    reasoningLevel: 'low',
    agentSelection: {},
    setAgentSelection,
  }));
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe('useChat.startComparison', () => {
  it('wendet die im Dialog gewählte Agent-/Team-Auswahl an und sendet Anhang plus Modi', async () => {
    uploadMock.mockResolvedValueOnce({ attachmentId: 'att1', filename: 'entwurf.docx', sectionCount: 3, charCount: 120 });
    const { result } = renderUseChat();

    await act(async () => {
      await result.current.startComparison({
        file: new File(['x'], 'entwurf.docx'),
        modes: ['contradiction'],
        instruction: 'Fasse die Abweichungen zusammen.',
        agentSelection: { teamId: 't1' },
      });
    });

    // The controller ruling: without this call, useChatStream keeps sending
    // whatever team/agent was selected before, silently dropping the
    // dialog's choice.
    expect(setAgentSelection).toHaveBeenCalledWith({ teamId: 't1' });
    expect(streamSend).toHaveBeenCalledWith(
      expect.anything(),
      'Fasse die Abweichungen zusammen.',
      null,
      undefined,
      expect.objectContaining({ attachmentId: 'att1', comparisonModes: ['contradiction'] }),
    );
  });

  it('sendet nicht und wendet keine Agent-Auswahl an, wenn der Upload fehlschlägt', async () => {
    uploadMock.mockResolvedValueOnce(null);
    const { result } = renderUseChat();

    await act(async () => {
      await result.current.startComparison({
        file: new File(['x'], 'zu-gross.docx'),
        modes: ['contradiction'],
        instruction: 'x',
        agentSelection: { teamId: 't1' },
      });
    });

    expect(streamSend).not.toHaveBeenCalled();
    expect(setAgentSelection).not.toHaveBeenCalled();
  });
});

describe('buildComparisonSend', () => {
  const attachment: ChatAttachment = { attachmentId: 'att1', filename: 'x.docx', sectionCount: 2, charCount: 50 };

  it('verwendet die getrimmte Instruktion als Nachricht', () => {
    const result = buildComparisonSend(attachment, ['contradiction', 'formal'], '  Fasse zusammen.  ', 'Fallback');
    expect(result).toEqual({
      message: 'Fasse zusammen.',
      opts: { attachmentId: 'att1', comparisonModes: ['contradiction', 'formal'] },
    });
  });

  it('fällt bei leerer (oder nur Leerzeichen-) Instruktion auf die Standardnachricht zurück', () => {
    const result = buildComparisonSend(attachment, ['completeness'], '   ', 'Vergleiche dieses Dokument mit der Wissensbasis');
    expect(result.message).toBe('Vergleiche dieses Dokument mit der Wissensbasis');
  });
});
