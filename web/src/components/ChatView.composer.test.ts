import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import path from 'node:path';
import { describe, it, expect } from 'vitest';

// ChatView.tsx enthält Nicht-UTF8-Bytes (deshalb 'latin1' statt 'utf8' —
// mit 'utf8' liefert Node Ersatzzeichen und die Suche ginge ins Leere).
//
// Absichtlich NICHT `new URL('./ChatView.tsx', import.meta.url)`: Vite
// erkennt genau dieses Muster statisch als Asset-URL und ersetzt es durch
// eine `http://localhost:.../ChatView.tsx`-Dev-Server-URL statt einer
// echten Datei-URL — `fileURLToPath` wirft dann "The URL must be of scheme
// file". `path.dirname(fileURLToPath(import.meta.url))` + `path.join`
// entgeht dieser Sonderbehandlung.
const here = path.dirname(fileURLToPath(import.meta.url));
const src = readFileSync(path.join(here, 'ChatView.tsx'), 'latin1');

describe('ChatView-Composer', () => {
  it('bietet keinen Datei-Anhang mehr an', () => {
    for (const gone of ['Paperclip', 'ChatComparisonControls', 'comparisonAttach', 'attachmentUploading']) {
      expect(src).not.toContain(gone);
    }
  });

  it('liest die Datei überhaupt (sonst wäre der Wächter vakuum-grün)', () => {
    expect(src).toContain('const ChatViewComp');
    expect(src.length).toBeGreaterThan(10_000);
  });
});

// Der Zurückknopf saß bis 2026-08 in der Quellenleiste. Seit die im
// UI-Organisationsrework nach rechts gewandert ist, stand er auf der falschen
// Bildschirmseite — weit weg von Symbol und Titel, zu denen er gehört. Er
// steht jetzt ganz links in der Kopfleiste der KB-Ansicht, auf Desktop wie auf
// Mobil (vorher war es ein Entweder-oder: Mobil Zurück, Desktop Symbol).
//
// Textwächter, kein Render-Test: ChatView zieht ein gutes Dutzend Kontexte und
// hat deshalb repo-weit keinen Render-Harness. Der zweite Test unten hält den
// Wächter davon ab, vakuum-grün zu werden.
const sourcesPanel = readFileSync(
  path.join(here, 'sources', 'SourcesPanel.tsx'),
  'utf8',
);

describe('Zurück-Knopf in der KB-Kopfleiste', () => {
  it('steht in der Kopfleiste und ist nicht mehr an isMobile gebunden', () => {
    // Der Knopf trägt backToOverview und ruft handleGoHome (nicht
    // handleViewHome — sonst bleibt kbView auf dem zuletzt gewählten Reiter
    // stehen und die KB öffnet sich beim nächsten Mal nicht im Chat).
    expect(src).toContain('ArrowLeft');
    expect(src).toContain("aria-label={t('backToOverview')}");
    expect(src).toContain('onClick={handleGoHome}');
    // Das Symbol ist die Desktop-Variante, der Knopf nicht. Die alte Form war
    // ein Ternär — Mobil Zurück ODER Desktop Symbol —, und genau die darf
    // nicht zurückkommen: sie ist der Zustand, in dem der Desktop keinen
    // Zurückknopf hat. `{!isMobile && (` als Wächter wäre wirkungslos, den
    // Ausdruck gibt es in dieser Datei an drei weiteren Stellen (per Mutation
    // geprüft: die Assertion blieb grün).
    expect(src).not.toContain('isMobile ? (');
    expect(src).not.toContain("aria-label={t('back')}");
  });

  it('ist aus der Quellenleiste verschwunden', () => {
    // Ein zweiter Knopf an der alten Stelle wäre für Nutzer kein Fehler, den
    // jemand meldet — er sähe nur nach Doppelung aus und bliebe stehen.
    expect(sourcesPanel).not.toContain('backToOverview');
    expect(sourcesPanel).not.toContain('handleViewHome');
    // Vakuum-Schutz für DIESE Datei: sie wird wirklich gelesen.
    expect(sourcesPanel).toContain('const SourcesPanelComp');
  });
});
