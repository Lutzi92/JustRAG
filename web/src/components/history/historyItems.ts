import type { ChatEntry, GeneratedContent } from '../../types';

export type HistoryKind = 'chat' | 'artifact' | 'research' | 'academic';

export interface HistoryItem {
  id: string;
  kind: HistoryKind;
  title: string;
  createdAt: string;
  /** Nur bei kind === 'artifact': der Artefakttyp für Icon und Label. */
  artifactType?: GeneratedContent['type'];
  /** Die Rohdaten für den Klick-Handler. */
  source: ChatEntry | GeneratedContent;
}

function chatKind(type: string | undefined): HistoryKind {
  if (type === 'research') return 'research';
  if (type === 'academic_research') return 'academic';
  return 'chat';
}

/**
 * Führt die vier Verlaufsquellen zu einer chronologischen Liste zusammen.
 *
 * Bis 2026-08 standen Artefakte und Research-Sessions in der oberen Hälfte der
 * rechten Seitenleiste und Chats in der unteren — zwei Listen, die man nicht
 * gegeneinander lesen konnte. Der Klick-Zielreiter unterscheidet sich weiter
 * je Art (siehe HistoryPanel), die Darstellung nicht mehr.
 */
export function buildHistoryItems(input: {
  chats: ChatEntry[];
  generatedContent: GeneratedContent[];
}): HistoryItem[] {
  const items: HistoryItem[] = [
    ...input.chats.map(c => ({
      id: c.id,
      kind: chatKind((c as { type?: string }).type),
      title: c.title,
      createdAt: c.createdAt,
      source: c,
    })),
    ...input.generatedContent.map(g => ({
      id: g.id,
      kind: 'artifact' as const,
      title: g.title,
      createdAt: g.createdAt,
      artifactType: g.type,
      source: g,
    })),
  ];
  return items.sort(
    (a, b) => new Date(b.createdAt).getTime() - new Date(a.createdAt).getTime(),
  );
}
