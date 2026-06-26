import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { buildAnswerMarkdown, copyAnswerWithCitations, downloadAnswerMarkdown, exportAnswerDocx } from './exportAnswer';
import type { Message } from '../types';

vi.mock('./clipboard', () => ({
  copyToClipboard: vi.fn(async () => true),
}));
import { copyToClipboard } from './clipboard';

vi.mock('../api', () => ({
  API_BASE_URL: 'http://test',
  authFetch: vi.fn(),
}));
import { API_BASE_URL, authFetch } from '../api';

const aiMsg = (over: Partial<Message> = {}): Message => ({
  role: 'ai',
  content: 'The answer body.',
  ...over,
});

describe('buildAnswerMarkdown', () => {
  it('omits the Sources section when there are no sources', () => {
    expect(buildAnswerMarkdown(aiMsg())).toBe('The answer body.');
  });

  it('appends a Sources section with page ranges', () => {
    const md = buildAnswerMarkdown(aiMsg({
      sources: [
        { index: 1, fileName: 'q3.pdf', content: '', score: 0.9, pages: [4, 5, 6, 9] },
        { index: 2, fileName: 'memo.docx', content: '', score: 0.8 },
      ],
    }));
    expect(md).toBe(
      'The answer body.\n\n## Sources\n[1] q3.pdf (p. 4-6, 9)\n[2] memo.docx'
    );
  });

  it('orders sources by index when present', () => {
    const md = buildAnswerMarkdown(aiMsg({
      sources: [
        { index: 2, fileName: 'b.pdf', content: '', score: 0.5 },
        { index: 1, fileName: 'a.pdf', content: '', score: 0.5 },
      ],
    }));
    expect(md).toBe('The answer body.\n\n## Sources\n[1] a.pdf\n[2] b.pdf');
  });

  it('falls back to array order and 1-based numbering when index is absent', () => {
    const md = buildAnswerMarkdown(aiMsg({
      sources: [
        { fileName: 'first.pdf', content: '', score: 0.5 },
        { fileName: 'second.pdf', content: '', score: 0.5 },
      ],
    }));
    expect(md).toBe('The answer body.\n\n## Sources\n[1] first.pdf\n[2] second.pdf');
  });

  it('honors a custom sourcesHeading', () => {
    const md = buildAnswerMarkdown(
      aiMsg({ sources: [{ index: 1, fileName: 'x.pdf', content: '', score: 0.5 }] }),
      { sourcesHeading: 'Quellen' }
    );
    expect(md).toBe('The answer body.\n\n## Quellen\n[1] x.pdf');
  });
});

describe('copyAnswerWithCitations', () => {
  it('copies the built markdown to the clipboard', async () => {
    const ok = await copyAnswerWithCitations(
      { role: 'ai', content: 'Body.', sources: [{ index: 1, fileName: 'a.pdf', content: '', score: 0.5 }] },
      'Sources'
    );
    expect(ok).toBe(true);
    expect(copyToClipboard).toHaveBeenCalledWith('Body.\n\n## Sources\n[1] a.pdf');
  });
});

describe('downloadAnswerMarkdown', () => {
  let captured: Blob | null;
  let clickSpy: ReturnType<typeof vi.fn>;
  beforeEach(() => {
    captured = null;
    clickSpy = vi.fn();
    vi.spyOn(URL, 'createObjectURL').mockImplementation((obj: Blob | MediaSource) => { captured = obj as Blob; return 'blob:mock'; });
    vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {});
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(clickSpy as unknown as () => void);
  });
  afterEach(() => { vi.restoreAllMocks(); });

  it('downloads a .md blob containing the answer markdown', async () => {
    downloadAnswerMarkdown({ role: 'ai', content: 'Body.' });
    expect(clickSpy).toHaveBeenCalledOnce();
    expect(captured).not.toBeNull();
    const text = await (captured as Blob).text();
    expect(text).toBe('Body.');
    expect((captured as Blob).type).toContain('text/markdown');
  });
});

describe('exportAnswerDocx', () => {
  beforeEach(() => {
    vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:mock');
    vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {});
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});
  });
  afterEach(() => { vi.restoreAllMocks(); vi.clearAllMocks(); });

  it('posts the built markdown and question to the docx endpoint', async () => {
    (authFetch as ReturnType<typeof vi.fn>).mockResolvedValue({
      ok: true,
      blob: async () => new Blob(['docx']),
    });
    await exportAnswerDocx('kb-1', { role: 'ai', content: 'Body.' }, 'What is X?', 'Sources');
    expect(authFetch).toHaveBeenCalledWith(
      `${API_BASE_URL}/api/kb/kb-1/export/docx`,
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({ report: 'Body.', goal: 'What is X?' }),
      })
    );
  });

  it('defaults the goal to "Answer" when the question is empty', async () => {
    (authFetch as ReturnType<typeof vi.fn>).mockResolvedValue({ ok: true, blob: async () => new Blob(['x']) });
    await exportAnswerDocx('kb-1', { role: 'ai', content: 'Body.' }, '');
    const body = JSON.parse((authFetch as ReturnType<typeof vi.fn>).mock.calls[0][1].body);
    expect(body.goal).toBe('Answer');
  });

  it('throws when the response is not ok', async () => {
    (authFetch as ReturnType<typeof vi.fn>).mockResolvedValue({ ok: false });
    await expect(exportAnswerDocx('kb-1', { role: 'ai', content: 'Body.' }, 'q')).rejects.toThrow();
  });
});
