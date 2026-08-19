import { describe, it, expect, vi, beforeEach } from 'vitest';
import { renderHook, act } from '@testing-library/react';
import { useState, useRef, useEffect } from 'react';
import { useChatStream } from './useChatStream';
import type { KnowledgeBase, FileEntry, Message } from '../types';
import { getBranchInfo } from '../utils/messageTree';

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

  return { stream, messageTree, setMessageTree };
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

// The comparison round trip: a user uploads a document, gets findings, then
// asks a follow-up about that same document. Both halves were only ever tested
// in pieces (the dialog, the outgoing call, the backend stages), and the
// follow-up half was silently broken once already — `handleNewChat` cleared the
// attachment, so the second turn arrived without it and the backend had nothing
// to inject. This covers the SSE stretch: findings must land ON the message
// (that is what MessageBubble gates its findings panel on), and a follow-up
// turn must still carry the attachment id while NOT re-running the comparison.
describe('useChatStream — Vergleichs-Rundlauf', () => {
  const findings = [
    { mode: 'contradiction', severity: 'high', issue: 'Frist weicht ab', uploadQuote: 'vier Wochen', citedQuote: 'sechs Wochen', citedFileIds: ['f1'], sectionIdx: 0 },
  ];

  it('legt die Findings des Vergleichs-Turns auf der KI-Nachricht ab', async () => {
    authFetchMock.mockResolvedValueOnce(okResponse([
      { aiMessageId: 'ai-cmp' },
      { comparisonFindings: findings },
      { content: 'Ich habe einen Widerspruch gefunden.' },
    ]));
    const { result } = renderHook(() => useHarness());

    await act(async () => {
      await result.current.stream.handleSendMessage(
        { preventDefault: () => {} } as React.FormEvent,
        'Vergleiche das Dokument.',
        null,
        undefined,
        { attachmentId: 'att1', comparisonModes: ['contradiction'] },
      );
    });

    const aiMsg = result.current.messageTree.get('ai-cmp');
    // MessageBubble renders <ComparisonFindings> exactly on this field being a
    // non-empty array; an event that arrives but never reaches the message
    // leaves the user with an answer and no findings panel.
    expect(aiMsg?.comparisonFindings).toHaveLength(1);
    expect(aiMsg?.comparisonFindings?.[0].issue).toBe('Frist weicht ab');
    expect(aiMsg?.content).toContain('Widerspruch');
  });

  it('schickt bei der Folgefrage den Anhang weiter, ohne den Vergleich erneut zu starten', async () => {
    authFetchMock.mockResolvedValueOnce(okResponse([
      { aiMessageId: 'ai-follow' },
      { content: 'Die Frist im Dokument sind vier Wochen.' },
    ]));
    const { result } = renderHook(() => useHarness());

    await act(async () => {
      await result.current.stream.handleSendMessage(
        { preventDefault: () => {} } as React.FormEvent,
        'Welche Frist steht im Dokument?',
        null,
        undefined,
        { attachmentId: 'att1' },
      );
    });

    const [, init] = authFetchMock.mock.calls[0] as [string, RequestInit];
    const body = JSON.parse(init.body as string);
    // Both halves matter: without attachmentId the backend has no document to
    // inject; with comparisonModes it would re-run the whole comparison
    // instead of answering the question.
    expect(body.attachmentId).toBe('att1');
    expect(body.comparisonModes).toBeUndefined();
    expect(result.current.messageTree.get('ai-follow')?.content).toContain('vier Wochen');
  });
});

// "Antwort neu generieren" used to re-send the question, which put a second
// copy of the user's prompt into the thread. A regenerate now carries no
// question of its own: the new answer hangs under the EXISTING user message
// as a sibling, so the prompt stays on screen exactly once and the ‹1/2›
// switcher lands on the answers.
describe('useChatStream.handleSendMessage — regenerate', () => {
  const regenerateOf = { aiMessageId: 'ai-old', userMessageId: 'u1' };

  /** The answered turn a regenerate starts from: u1 -> ai-old. */
  function seedAnsweredTurn(result: { current: ReturnType<typeof useHarness> }) {
    act(() => {
      result.current.setMessageTree(new Map<string, Message>([
        ['u1', { id: 'u1', role: 'user', content: 'Wie funktioniert X?', childIds: ['ai-old'] }],
        ['ai-old', { id: 'ai-old', role: 'ai', content: 'Antwort 1', parentMessageId: 'u1', childIds: [] }],
      ]));
    });
  }

  it('macht die neue Antwort zu einem Geschwister der alten', async () => {
    authFetchMock.mockResolvedValueOnce(okResponse([{ aiMessageId: 'ai-new' }]));
    const { result } = renderHook(() => useHarness());
    seedAnsweredTurn(result);

    await act(async () => {
      await result.current.stream.handleSendMessage(
        { preventDefault: () => {} } as React.FormEvent,
        'Wie funktioniert X?',
        'ai-old',
        undefined,
        { regenerateOf },
      );
    });

    // This is requirement "‹1/2› sits on the answers": both answers are
    // children of the one question, so getBranchInfo reports two siblings.
    const info = getBranchInfo(result.current.messageTree, 'ai-new');
    expect(info?.total).toBe(2);
    expect(info?.siblingIds).toEqual(['ai-old', 'ai-new']);
  });

  it('legt keine zweite Frage im Nachrichtenbaum an', async () => {
    authFetchMock.mockResolvedValueOnce(okResponse([{ aiMessageId: 'ai-new' }]));
    const { result } = renderHook(() => useHarness());

    await act(async () => {
      await result.current.stream.handleSendMessage(
        { preventDefault: () => {} } as React.FormEvent,
        'Wie funktioniert X?',
        'ai-old',
        undefined,
        { regenerateOf },
      );
    });

    const roles = [...result.current.messageTree.values()].map(m => m.role);
    expect(roles).not.toContain('user');
  });

  it('hängt die neue Antwort unter die bestehende Frage', async () => {
    authFetchMock.mockResolvedValueOnce(okResponse([{ aiMessageId: 'ai-new' }]));
    const { result } = renderHook(() => useHarness());

    await act(async () => {
      await result.current.stream.handleSendMessage(
        { preventDefault: () => {} } as React.FormEvent,
        'Wie funktioniert X?',
        'ai-old',
        undefined,
        { regenerateOf },
      );
    });

    expect(result.current.messageTree.get('ai-new')?.parentMessageId).toBe('u1');
  });

  it('schickt regenerateOfMessageId statt eines parentMessageId', async () => {
    authFetchMock.mockResolvedValueOnce(okResponse([{ aiMessageId: 'ai-new' }]));
    const { result } = renderHook(() => useHarness());

    await act(async () => {
      await result.current.stream.handleSendMessage(
        { preventDefault: () => {} } as React.FormEvent,
        'Wie funktioniert X?',
        'ai-old',
        undefined,
        { regenerateOf },
      );
    });

    const [, init] = authFetchMock.mock.calls[0] as [string, RequestInit];
    const body = JSON.parse(init.body as string);
    expect(body.regenerateOfMessageId).toBe('ai-old');
    // Sending the active leaf as a parent is what appended the duplicate
    // prompt: the backend would insert a fresh question under the old answer.
    expect(body.parentMessageId).toBeUndefined();
  });

  it('hängt eine fehlgeschlagene Regeneration an die bestehende Frage, nicht an eine Kopie', async () => {
    authFetchMock.mockResolvedValueOnce({ ok: false, json: async () => ({}) } as unknown as Response);
    const { result } = renderHook(() => useHarness());

    await act(async () => {
      await result.current.stream.handleSendMessage(
        { preventDefault: () => {} } as React.FormEvent,
        'Wie funktioniert X?',
        'ai-old',
        undefined,
        { regenerateOf },
      );
    });

    const errorNode = [...result.current.messageTree.values()].find(m => m.id?.startsWith('temp-error-'));
    expect(errorNode?.parentMessageId).toBe('u1');
  });
});

// The edit path shares the parent-resolution code below. `editParentId` used
// to be `string | undefined`, so "this question is the root of the chat"
// (no parent) was indistinguishable from "no parent given" and fell through
// to the active leaf — appending the edited question to the very answer it
// was meant to replace.
describe('useChatStream.handleSendMessage — editing the first question of a chat', () => {
  it('verzweigt an der Wurzel statt an das aktive Blatt anzuhängen', async () => {
    authFetchMock.mockResolvedValueOnce(okResponse([{ userMessageId: 'u-new' }, { aiMessageId: 'ai-new' }]));
    const { result } = renderHook(() => useHarness());

    await act(async () => {
      await result.current.stream.handleSendMessage(
        { preventDefault: () => {} } as React.FormEvent,
        'Neu formulierte Frage',
        'ai-old',
        null,
      );
    });

    const [, init] = authFetchMock.mock.calls[0] as [string, RequestInit];
    const body = JSON.parse(init.body as string);
    expect(body.parentMessageId).toBeUndefined();
    expect(result.current.messageTree.get('u-new')?.parentMessageId).toBeUndefined();
  });
});
