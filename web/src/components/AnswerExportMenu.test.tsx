// web/src/components/AnswerExportMenu.test.tsx
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { createRef } from 'react';
import { AnswerExportMenu } from './AnswerExportMenu';
import type { Message } from '../types';

vi.mock('../contexts/ToastContext', () => ({
  useToast: () => ({ success: vi.fn(), error: vi.fn(), info: vi.fn() }),
}));

const mocks = vi.hoisted(() => ({
  copy: vi.fn(async () => true),
  md: vi.fn(),
  docx: vi.fn(async () => {}),
  pdf: vi.fn(),
}));
vi.mock('../utils/exportAnswer', () => ({
  copyAnswerWithCitations: mocks.copy,
  downloadAnswerMarkdown: mocks.md,
  exportAnswerDocx: mocks.docx,
  printAnswerPdf: mocks.pdf,
}));

const t = (k: string) => k;
const baseProps = () => ({
  message: { role: 'ai', content: 'Body.' } as Message,
  contentRef: createRef<HTMLDivElement>(),
  buttonStyle: {},
  iconSize: 14,
  t,
});

describe('AnswerExportMenu', () => {
  beforeEach(() => { vi.clearAllMocks(); });

  it('opens the menu and copies with citations', async () => {
    render(<AnswerExportMenu {...baseProps()} />);
    await userEvent.click(screen.getByLabelText('exportAnswer'));
    await userEvent.click(screen.getByText('copyWithCitations'));
    expect(mocks.copy).toHaveBeenCalledOnce();
  });

  it('hides the DOCX item when kbId is absent', async () => {
    render(<AnswerExportMenu {...baseProps()} />);
    await userEvent.click(screen.getByLabelText('exportAnswer'));
    expect(screen.queryByText('exportDocx')).toBeNull();
  });

  it('shows and invokes the DOCX item when kbId is present', async () => {
    render(<AnswerExportMenu {...baseProps()} kbId="kb-1" questionText="Q?" />);
    await userEvent.click(screen.getByLabelText('exportAnswer'));
    await userEvent.click(screen.getByText('exportDocx'));
    expect(mocks.docx).toHaveBeenCalledWith('kb-1', expect.anything(), 'Q?', 'sourcesHeading');
  });

  it('invokes markdown and pdf actions', async () => {
    render(<AnswerExportMenu {...baseProps()} />);
    await userEvent.click(screen.getByLabelText('exportAnswer'));
    await userEvent.click(screen.getByText('exportMarkdown'));
    expect(mocks.md).toHaveBeenCalledOnce();
    await userEvent.click(screen.getByLabelText('exportAnswer'));
    await userEvent.click(screen.getByText('exportPdf'));
    expect(mocks.pdf).toHaveBeenCalledOnce();
  });
});
