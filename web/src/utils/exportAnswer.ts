import DOMPurify from 'dompurify';
import type { Message } from '../types';
import { formatPageRanges } from './citations';
import { copyToClipboard } from './clipboard';
import { API_BASE_URL, authFetch } from '../api';

export interface BuildAnswerMarkdownOpts {
  sourcesHeading?: string;
}

// buildAnswerMarkdown is the single source of truth for "answer + citations"
// export content, shared by clipboard copy, .md download, and DOCX export.
export function buildAnswerMarkdown(message: Message, opts: BuildAnswerMarkdownOpts = {}): string {
  const body = (message.content ?? '').trim();
  const sources = message.sources ?? [];
  if (sources.length === 0) {
    return body;
  }
  const heading = opts.sourcesHeading || 'Sources';
  const ordered = sources.every((s) => typeof s.index === 'number')
    ? [...sources].sort((a, b) => (a.index as number) - (b.index as number))
    : sources;
  const lines = ordered.map((s, i) => {
    const n = typeof s.index === 'number' ? s.index : i + 1;
    const pages = s.pages && s.pages.length > 0 ? ` (p. ${formatPageRanges(s.pages)})` : '';
    return `[${n}] ${s.fileName}${pages}`;
  });
  return `${body}\n\n## ${heading}\n${lines.join('\n')}`;
}

export function todayStamp(): string {
  return new Date().toISOString().split('T')[0];
}

export function triggerDownload(blob: Blob, filename: string): void {
  const url = window.URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  setTimeout(() => window.URL.revokeObjectURL(url), 0);
}

export async function copyAnswerWithCitations(message: Message, sourcesHeading?: string): Promise<boolean> {
  return copyToClipboard(buildAnswerMarkdown(message, { sourcesHeading }));
}

export function downloadAnswerMarkdown(message: Message, sourcesHeading?: string): void {
  const md = buildAnswerMarkdown(message, { sourcesHeading });
  triggerDownload(new Blob([md], { type: 'text/markdown;charset=utf-8' }), `answer-${todayStamp()}.md`);
}

export async function exportAnswerDocx(
  kbId: string,
  message: Message,
  question: string,
  sourcesHeading?: string,
): Promise<void> {
  const report = buildAnswerMarkdown(message, { sourcesHeading });
  const response = await authFetch(`${API_BASE_URL}/api/kb/${kbId}/export/docx`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ report, goal: question || 'Answer' }),
  });
  if (!response.ok) {
    throw new Error('docx export failed');
  }
  const blob = await response.blob();
  triggerDownload(blob, `answer-${todayStamp()}.docx`);
}

// printAnswerPdf prints the already-rendered answer DOM node so the PDF matches
// exactly what the user sees. Mirrors AcademicResearchMode.downloadPdf's
// hidden-iframe + window.print() pattern.
export function printAnswerPdf(node: HTMLElement | null, title: string): void {
  if (!node) return;
  const iframe = document.createElement('iframe');
  iframe.style.position = 'fixed';
  iframe.style.right = '0';
  iframe.style.bottom = '0';
  iframe.style.width = '0';
  iframe.style.height = '0';
  iframe.style.border = 'none';
  document.body.appendChild(iframe);

  const doc = iframe.contentDocument || iframe.contentWindow?.document;
  if (!doc) {
    document.body.removeChild(iframe);
    return;
  }
  doc.open();
  // Escape the title (user-supplied question) and DOMPurify the body before
  // re-injecting into the iframe — mirrors AcademicResearchMode.downloadPdf.
  // The body is already rehype-sanitized in MessageContent; DOMPurify here is
  // defense-in-depth.
  doc.write(`<!DOCTYPE html><html><head><title>${title.replace(/</g, '&lt;')}</title>
<style>
  body { font-family: -apple-system, system-ui, sans-serif; line-height: 1.6; padding: 24px; color: #111; }
  h1, h2, h3 { line-height: 1.3; }
  pre, code { font-family: ui-monospace, monospace; white-space: pre-wrap; }
  table { border-collapse: collapse; }
  td, th { border: 1px solid #ccc; padding: 4px 8px; }
</style></head><body>${DOMPurify.sanitize(node.innerHTML)}</body></html>`);
  doc.close();

  const win = iframe.contentWindow;
  if (!win) {
    document.body.removeChild(iframe);
    return;
  }
  win.focus();
  win.print();
  setTimeout(() => document.body.removeChild(iframe), 1000);
}
