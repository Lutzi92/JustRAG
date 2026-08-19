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
