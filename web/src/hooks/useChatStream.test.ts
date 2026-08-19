import { describe, it, expect, vi, beforeEach } from 'vitest';
import { renderHook, act } from '@testing-library/react';
import { useState, useRef, useEffect } from 'react';
import { useChatStream } from './useChatStream';
import type { KnowledgeBase, FileEntry, Message } from '../types';

// Fix round 1: `useChatStream.handleSendMessage` builds its outgoing request
// (and the attribution it later stamps onto the AI message) from a closure
// over `agentSelection` — a plain hook parameter, not a ref. Calling
// `setAgentSelection` right before `await stream.handleSendMessage(...)`
// (the original, WRONG fix in useChat.ts) cannot affect that closure: the
// state update only takes effect on the *next* render, but the request is
// built synchronously, before any re-render happens. The corrected fix has
// `handleSendMessage` prefer `opts.agentSelection` over the closure value
// (`effectiveSelection = opts?.agentSelection ?? agentSelection`) so a caller
// can override the selection for a single turn without waiting on state.
//
// No pre-existing test harness for `useChatStream` (checked:
// `find . -iname "useChatStream*test*"` — nothing). `authFetch` is mocked to
// return a tiny fake SSE stream; `parseSseStream`, `messageTree` utils and
// `triggerHaptic` are the real implementations (all pure / environment-safe
// in jsdom).

const authFetchMock = vi.fn();
vi.mock('../api', () => ({
  API_BASE_URL: 'http://test',
  authFetch: (...args: unknown[]) => authFetchMock(...args),
}));

function sseReader(events: unknown[]): ReadableStreamDefaultReader<Uint8Array> {
  const encoder = new TextEncoder();
  const chunks = [...events.map(e => encoder.encode(`data: ${JSON.stringify(e)}\n`)), encoder.encode('data: [DONE]\n')];
  let i = 0;
  return {
    read: async () => (i < chunks.length ? { done: false, value: chunks[i++] } : { done: true, value: undefined }),
    releaseLock: () => {},
    cancel: async () => {},
  } as unknown as ReadableStreamDefaultReader<Uint8Array>;
}

function okResponse(events: unknown[]) {
  return { ok: true, body: { getReader: () => sseReader(events) } } as unknown as Response;
}

const kb = { id: 'kb1', name: 'Handbuch' } as KnowledgeBase;
const files = [{ id: 'f1', selected: true }] as FileEntry[];

/** Minimal stand-in for the tree-state slice `useMessageTree` normally owns,
 * so `result.current.messageTree` reflects what `handleSendMessage` wrote. */
function useHarness(overrides: { agentSelection?: { teamId?: string; agentId?: string } } = {}) {
  const [messageTree, setMessageTree] = useState<Map<string, Message>>(new Map());
  const messageTreeRef = useRef(messageTree);
  useEffect(() => { messageTreeRef.current = messageTree; }, [messageTree]);
  const activeChatIdRef = useRef<string | null>(null);
  const setActiveChatId = (id: string | null) => { activeChatIdRef.current = id; };

  const stream = useChatStream({
    currentKb: kb,
    files,
    enhance: null,
    reasoningEnabled: false,
    reasoningLevel: 'low',
    // The "stale" selection: what a plain state closure would still hold if
    // a caller only called setAgentSelection() (which lands on the NEXT
    // render) instead of passing opts.agentSelection.
    agentSelection: overrides.agentSelection ?? { agentId: 'stale-agent' },
    language: 'de',
    setMessageTree, messageTreeRef, setActiveLeafId: () => {},
    // `activeChatId` itself is unused inside useChatStream (only
    // `activeChatIdRef` is read); a static value avoids reading a ref
    // during render, which the lint rule (rightly) flags.
    activeChatIdRef, activeChatId: null, setActiveChatId,
    setChats: () => {}, fetchChats: async () => {}, fetchChatsTimerRef: { current: undefined },
    t: (k: string) => k,
  });

  return { stream, messageTree };
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe('useChatStream.handleSendMessage — opts.agentSelection precedence', () => {
  it('sendet die Team-/Agent-Auswahl aus opts, nicht die aus dem Hook-State', async () => {
    authFetchMock.mockResolvedValueOnce(okResponse([{ aiMessageId: 'ai1' }]));
    const { result } = renderHook(() => useHarness());

    await act(async () => {
      await result.current.stream.handleSendMessage(
        { preventDefault: () => {} } as React.FormEvent,
        'Vergleiche das.',
        null,
        undefined,
        { attachmentId: 'att1', comparisonModes: ['contradiction'], agentSelection: { teamId: 't1' } },
      );
    });

    expect(authFetchMock).toHaveBeenCalledTimes(1);
    const [, init] = authFetchMock.mock.calls[0] as [string, RequestInit];
    const body = JSON.parse(init.body as string);
    expect(body.teamId).toBe('t1');
    expect(body.agentId).toBeUndefined();
  });

  it('schreibt die opts-Auswahl als Attribution auf die KI-Nachricht, nicht den alten Hook-State', async () => {
    authFetchMock.mockResolvedValueOnce(okResponse([{ aiMessageId: 'ai1' }]));
    const { result } = renderHook(() => useHarness());

    await act(async () => {
      await result.current.stream.handleSendMessage(
        { preventDefault: () => {} } as React.FormEvent,
        'Vergleiche das.',
        null,
        undefined,
        { attachmentId: 'att1', comparisonModes: ['contradiction'], agentSelection: { teamId: 't1' } },
      );
    });

    const aiMsg = result.current.messageTree.get('ai1');
    expect(aiMsg?.teamId).toBe('t1');
    expect(aiMsg?.agentId ?? null).toBe(null);
  });

  it('fällt ohne opts.agentSelection auf den Hook-State zurück (normaler Chat-Turn)', async () => {
    authFetchMock.mockResolvedValueOnce(okResponse([{ aiMessageId: 'ai2' }]));
    const { result } = renderHook(() => useHarness({ agentSelection: { agentId: 'a-default' } }));

    await act(async () => {
      await result.current.stream.handleSendMessage(
        { preventDefault: () => {} } as React.FormEvent,
        'Normale Frage',
        null,
        undefined,
      );
    });

    const [, init] = authFetchMock.mock.calls[0] as [string, RequestInit];
    const body = JSON.parse(init.body as string);
    expect(body.agentId).toBe('a-default');
    expect(result.current.messageTree.get('ai2')?.agentId).toBe('a-default');
  });
});
