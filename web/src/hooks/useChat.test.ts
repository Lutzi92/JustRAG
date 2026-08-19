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
// useKbRemoval.test.ts) rather than skipping hook-level coverage entirely.
//
// Fix round 1: the selection now travels through `opts.agentSelection` on
// the outgoing send, NOT (only) through `setAgentSelection` state —
// `useChatStream.handleSendMessage` builds its request synchronously from a
// closure captured before any `setAgentSelection` call can take effect, so
// the state write alone silently sent the *previous* selection for the
// comparison turn itself. `setAgentSelection` is still called too, but only
// to make the choice stick for FOLLOW-UP turns in the new chat — see the
// comment on that call in `useChat.ts`. The test below asserts on the send,
// not just the state write, because the state-write-only assertion is
// exactly what let the original (wrong) fix pass its own test.
//
// `buildComparisonSend` (below) still gets its own pure-function tests: it's
// the one piece of `startComparison`'s logic dense enough to be worth testing
// in isolation from the hook plumbing.

const setAgentSelection = vi.fn();
const streamSend = vi.fn();
const uploadMock = vi.fn();
const clearMock = vi.fn();
const toastError = vi.fn();
// A real ref-like object (not a vi.fn()) — `startComparison` reads
// `errorRef.current` synchronously right after `await upload()` resolves.
// See useChatAttachment.ts's doc comment on why `error` state can't do this.
const attachmentErrorRef: { current: string | null } = { current: null };

vi.mock('../contexts/ThemeContext', () => ({
  useTheme: () => ({ language: 'de', t: (key: string) => key }),
}));
vi.mock('../contexts/ModalContext', () => ({
  useModalContext: () => ({ showConfirm: vi.fn() }),
}));
vi.mock('../contexts/ToastContext', () => ({
  useToast: () => ({ success: vi.fn(), error: toastError, warning: vi.fn(), info: vi.fn() }),
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
    errorRef: attachmentErrorRef,
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
  attachmentErrorRef.current = null;
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

    // Fix round 1: the FIRST fix (setAgentSelection alone) shipped a test that
    // only asserted setAgentSelection was called — that would have passed
    // against the broken version too, since setAgentSelection was always
    // called; it just didn't affect *this* request. The real guard is on the
    // outgoing send: `streamSend`'s opts must carry the selection, because
    // `useChatStream` reads it from `opts.agentSelection` (falling back to
    // its own closure state) to build the request that goes out THIS turn.
    expect(streamSend).toHaveBeenCalledWith(
      expect.anything(),
      'Fasse die Abweichungen zusammen.',
      null,
      undefined,
      expect.objectContaining({
        attachmentId: 'att1',
        comparisonModes: ['contradiction'],
        agentSelection: { teamId: 't1' },
      }),
    );
    // Still expected — this is the SEPARATE write that makes the choice
    // stick for follow-up questions in the chat handleNewChat() just
    // created, not the one that governs this turn's own request.
    expect(setAgentSelection).toHaveBeenCalledWith({ teamId: 't1' });
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

  // Fix wave item 3: before this fix, a failed upload closed the dialog and
  // switched to Chat with zero user-visible feedback — attachmentState.error
  // had no consumer. Now startComparison must (a) surface the error via a
  // toast and (b) report failure back to the caller so the Workspace dialog
  // stays open instead of closing on a silent failure.
  it('zeigt einen Toast mit der Upload-Fehlermeldung und meldet false zurück', async () => {
    uploadMock.mockImplementationOnce(async () => {
      attachmentErrorRef.current = 'Datei zu groß';
      return null;
    });
    const { result } = renderUseChat();

    let started: boolean | undefined;
    await act(async () => {
      started = await result.current.startComparison({
        file: new File(['x'], 'zu-gross.docx'),
        modes: ['contradiction'],
        instruction: 'x',
        agentSelection: { teamId: 't1' },
      });
    });

    expect(started).toBe(false);
    expect(toastError).toHaveBeenCalledWith('Datei zu groß');
    expect(streamSend).not.toHaveBeenCalled();
  });

  it('meldet true zurück, wenn der Vergleich tatsächlich gestartet wurde', async () => {
    uploadMock.mockResolvedValueOnce({ attachmentId: 'att1', filename: 'x.docx', sectionCount: 1, charCount: 10 });
    const { result } = renderUseChat();

    let started: boolean | undefined;
    await act(async () => {
      started = await result.current.startComparison({
        file: new File(['x'], 'x.docx'),
        modes: ['contradiction'],
        instruction: 'x',
        agentSelection: {},
      });
    });

    expect(started).toBe(true);
  });
});

describe('buildComparisonSend', () => {
  const attachment: ChatAttachment = { attachmentId: 'att1', filename: 'x.docx', sectionCount: 2, charCount: 50 };

  it('verwendet die getrimmte Instruktion als Nachricht', () => {
    const result = buildComparisonSend(attachment, ['contradiction', 'formal'], '  Fasse zusammen.  ', 'Fallback', { teamId: 't1' });
    expect(result).toEqual({
      message: 'Fasse zusammen.',
      opts: { attachmentId: 'att1', comparisonModes: ['contradiction', 'formal'], agentSelection: { teamId: 't1' } },
    });
  });

  it('fällt bei leerer (oder nur Leerzeichen-) Instruktion auf die Standardnachricht zurück', () => {
    const result = buildComparisonSend(attachment, ['completeness'], '   ', 'Vergleiche dieses Dokument mit der Wissensbasis', {});
    expect(result.message).toBe('Vergleiche dieses Dokument mit der Wissensbasis');
  });

  it('gibt die Agent-/Team-Auswahl unverändert in opts zurück (statt sie zu verwerfen)', () => {
    const result = buildComparisonSend(attachment, ['formal'], 'x', 'Fallback', { agentId: 'a9' });
    expect(result.opts.agentSelection).toEqual({ agentId: 'a9' });
  });
});
