import { describe, it, expect } from 'vitest';
import { buildHistoryItems } from './historyItems';
import type { ChatEntry, GeneratedContent } from '../../types';

const chat = (id: string, createdAt: string, type?: string) =>
  ({ id, title: `chat-${id}`, createdAt, type } as unknown as ChatEntry);
const artifact = (id: string, createdAt: string, type: string) =>
  ({ id, title: `art-${id}`, createdAt, type, content: { text: '' } } as unknown as GeneratedContent);

describe('buildHistoryItems', () => {
  it('mischt alle vier Arten streng chronologisch absteigend', () => {
    const items = buildHistoryItems({
      chats: [
        chat('c1', '2026-08-10T00:00:00Z'),
        chat('r1', '2026-08-14T00:00:00Z', 'research'),
        chat('a1', '2026-08-12T00:00:00Z', 'academic_research'),
      ],
      generatedContent: [artifact('g1', '2026-08-16T00:00:00Z', 'analysis')],
    });
    expect(items.map(i => i.id)).toEqual(['g1', 'r1', 'a1', 'c1']);
    expect(items.map(i => i.kind)).toEqual(['artifact', 'research', 'academic', 'chat']);
  });

  it('leitet kind aus chats.type ab, nicht aus dem Titel', () => {
    const items = buildHistoryItems({
      chats: [chat('x', '2026-08-01T00:00:00Z', 'research')],
      generatedContent: [],
    });
    expect(items[0].kind).toBe('research');
  });

  it('trägt bei Artefakten den Typ für Icon und Label mit', () => {
    const items = buildHistoryItems({ chats: [], generatedContent: [artifact('g', '2026-08-01T00:00:00Z', 'faq')] });
    expect(items[0].artifactType).toBe('faq');
  });

  it('liefert eine leere Liste ohne Eingaben', () => {
    expect(buildHistoryItems({ chats: [], generatedContent: [] })).toEqual([]);
  });
});
