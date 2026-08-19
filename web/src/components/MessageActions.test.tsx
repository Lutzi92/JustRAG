import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { createRef } from 'react';
import { MessageActions } from './MessageActions';
import type { Message } from '../types';

vi.mock('../contexts/ThemeContext', () => ({
  useTheme: () => ({
    t: (key: string) => {
      const translations: Record<string, string> = {
        editMessage: 'Edit message',
        forkConversation: 'Fork from here',
        forkFromHere: 'Fork from here',
        regenerateResponse: 'Regenerate response',
        compareBranches: 'Compare branches',
        helpfulAnswer: 'Helpful answer',
        unhelpfulAnswer: 'Unhelpful answer',
        feedbackCommentPlaceholder: 'Optional comment (max 2000 chars)',
        feedbackCommentSubmit: 'Send',
        exportAnswer: 'Export answer',
        copyWithCitations: 'Copy with citations',
        exportMarkdown: 'Export as Markdown',
        exportDocx: 'Export as DOCX',
        exportPdf: 'Print / Save PDF',
        answerCopied: 'Copied',
        exportFailed: 'Export failed',
        sourcesHeading: 'Sources',
      };
      return translations[key] || key;
    }
  }),
}));

vi.mock('../contexts/ToastContext', () => ({
  useToast: () => ({ success: vi.fn(), error: vi.fn(), info: vi.fn() }),
}));

vi.mock('../contexts/AuthContext', () => ({
  useAuth: () => ({
    user: null,
    siteConfigs: {},
  }),
}));

const userMsg: Message = { id: 'u1', role: 'user', content: 'hello' };
const aiMsg: Message = { id: 'a1', role: 'ai', content: 'hi' };

describe('MessageActions', () => {
  it('shows Edit button only for user messages when onEdit provided', () => {
    render(<MessageActions message={userMsg} onEdit={() => {}} />);
    expect(screen.getByLabelText('Edit message')).toBeInTheDocument();
  });

  it('does not show Edit button for ai messages', () => {
    render(<MessageActions message={aiMsg} onEdit={() => {}} />);
    expect(screen.queryByLabelText('Edit message')).not.toBeInTheDocument();
  });

  it('shows Fork button only for ai messages when onFork provided', () => {
    render(<MessageActions message={aiMsg} onFork={() => {}} />);
    expect(screen.getByLabelText('Fork from here')).toBeInTheDocument();
  });

  it('does not show Fork button for user messages', () => {
    render(<MessageActions message={userMsg} onFork={() => {}} />);
    expect(screen.queryByLabelText('Fork from here')).not.toBeInTheDocument();
  });

  it('shows Regenerate button only for ai messages when onRegenerate provided', () => {
    render(<MessageActions message={aiMsg} onRegenerate={() => {}} />);
    expect(screen.getByLabelText('Regenerate response')).toBeInTheDocument();
  });

  it('does not show Regenerate for user messages', () => {
    render(<MessageActions message={userMsg} onRegenerate={() => {}} />);
    expect(screen.queryByLabelText('Regenerate response')).not.toBeInTheDocument();
  });

  // A regenerate names the answer it replaces, so the answer has to exist in
  // the database. On an unsaved one — a failed send leaves a `temp-error-*`
  // bubble behind — the button could only ever do nothing.
  it('does not show Regenerate for an unsaved ai message', () => {
    const tempMsg: Message = { id: 'temp-error-1787133382351', role: 'ai', content: 'Fehler' };
    render(<MessageActions message={tempMsg} onRegenerate={() => {}} />);
    expect(screen.queryByLabelText('Regenerate response')).not.toBeInTheDocument();
  });

  it('shows Compare button for both roles when onCompare provided', () => {
    const { unmount } = render(<MessageActions message={userMsg} onCompare={() => {}} />);
    expect(screen.getByLabelText('Compare branches')).toBeInTheDocument();
    unmount();

    render(<MessageActions message={aiMsg} onCompare={() => {}} />);
    expect(screen.getByLabelText('Compare branches')).toBeInTheDocument();
  });

  it('calls correct callback on click', async () => {
    const user = userEvent.setup();
    const onEdit = vi.fn();
    const onCompare = vi.fn();
    render(<MessageActions message={userMsg} onEdit={onEdit} onCompare={onCompare} />);

    await user.click(screen.getByLabelText('Edit message'));
    expect(onEdit).toHaveBeenCalledOnce();

    await user.click(screen.getByLabelText('Compare branches'));
    expect(onCompare).toHaveBeenCalledOnce();
  });

  it('renders no buttons when no callbacks provided', () => {
    const { container } = render(<MessageActions message={userMsg} />);
    expect(container.querySelectorAll('button')).toHaveLength(0);
  });

  it('renders the export menu button for AI messages with a contentRef', () => {
    render(<MessageActions message={aiMsg} contentRef={createRef<HTMLDivElement>()} />);
    expect(screen.getByLabelText('Export answer')).toBeInTheDocument();
  });

  it('does not render the export menu for user messages', () => {
    render(<MessageActions message={userMsg} contentRef={createRef<HTMLDivElement>()} />);
    expect(screen.queryByLabelText('Export answer')).toBeNull();
  });
});

describe('MessageActions feedback flow', () => {
  const aiMessage: Message = {
    id: 'msg-1',
    role: 'ai',
    content: 'hello',
    feedback: null,
  };

  it('first thumb click sends rating and reveals comment box', async () => {
    const user = userEvent.setup();
    const onFeedback = vi.fn();
    render(<MessageActions message={aiMessage} onFeedback={onFeedback} />);
    const upBtn = screen.getByLabelText('Helpful answer');
    await user.click(upBtn);
    expect(onFeedback).toHaveBeenCalledWith('positive');
    expect(screen.getByPlaceholderText('Optional comment (max 2000 chars)')).toBeInTheDocument();
  });

  it('clicking same thumb again clears the rating and hides the box', async () => {
    const user = userEvent.setup();
    const onFeedback = vi.fn();
    render(<MessageActions message={{ ...aiMessage, feedback: 'positive' }} onFeedback={onFeedback} />);
    const upBtn = screen.getByLabelText('Helpful answer');
    await user.click(upBtn);
    expect(onFeedback).toHaveBeenCalledWith(null);
  });

  it('typing and submitting forwards comment', async () => {
    const user = userEvent.setup();
    const onFeedback = vi.fn();
    render(<MessageActions message={aiMessage} onFeedback={onFeedback} />);
    await user.click(screen.getByLabelText('Helpful answer'));
    const textarea = screen.getByPlaceholderText('Optional comment (max 2000 chars)');
    await user.type(textarea, 'great answer');
    await user.click(screen.getByRole('button', { name: 'Send' }));
    expect(onFeedback).toHaveBeenLastCalledWith('positive', 'great answer');
  });
});
