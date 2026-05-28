import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { InlineMessageEditor } from './InlineMessageEditor';

vi.mock('../contexts/ThemeContext', () => ({
  useTheme: () => ({
    t: (key: string) => {
      const translations: Record<string, string> = {
        saveAndSubmit: 'Save & Submit',
        cancel: 'Cancel',
      };
      return translations[key] || key;
    }
  }),
}));

describe('InlineMessageEditor', () => {
  it('renders textarea with initialContent', () => {
    render(<InlineMessageEditor initialContent="hello" onSave={() => {}} onCancel={() => {}} />);
    expect(screen.getByRole('textbox')).toHaveValue('hello');
  });

  it('disables Save button when content unchanged', () => {
    render(<InlineMessageEditor initialContent="hello" onSave={() => {}} onCancel={() => {}} />);
    expect(screen.getByText('Save & Submit').closest('button')).toBeDisabled();
  });

  it('disables Save button when content is empty', async () => {
    const user = userEvent.setup();
    render(<InlineMessageEditor initialContent="hello" onSave={() => {}} onCancel={() => {}} />);

    await user.clear(screen.getByRole('textbox'));
    expect(screen.getByText('Save & Submit').closest('button')).toBeDisabled();
  });

  it('enables Save button when content changes', async () => {
    const user = userEvent.setup();
    render(<InlineMessageEditor initialContent="hello" onSave={() => {}} onCancel={() => {}} />);

    await user.clear(screen.getByRole('textbox'));
    await user.type(screen.getByRole('textbox'), 'new content');
    expect(screen.getByText('Save & Submit').closest('button')).not.toBeDisabled();
  });

  it('Enter (without shift) calls onSave with trimmed content', async () => {
    const user = userEvent.setup();
    const onSave = vi.fn();
    render(<InlineMessageEditor initialContent="old" onSave={onSave} onCancel={() => {}} />);

    const textarea = screen.getByRole('textbox');
    await user.clear(textarea);
    await user.type(textarea, '  new text  {Enter}');
    expect(onSave).toHaveBeenCalledWith('new text');
  });

  it('Escape calls onCancel', async () => {
    const user = userEvent.setup();
    const onCancel = vi.fn();
    render(<InlineMessageEditor initialContent="hello" onSave={() => {}} onCancel={onCancel} />);

    await user.type(screen.getByRole('textbox'), '{Escape}');
    expect(onCancel).toHaveBeenCalledOnce();
  });

  it('Cancel button calls onCancel', async () => {
    const user = userEvent.setup();
    const onCancel = vi.fn();
    render(<InlineMessageEditor initialContent="hello" onSave={() => {}} onCancel={onCancel} />);

    await user.click(screen.getByText('Cancel'));
    expect(onCancel).toHaveBeenCalledOnce();
  });

  it('does not call onSave for whitespace-only content on Enter', async () => {
    const user = userEvent.setup();
    const onSave = vi.fn();
    render(<InlineMessageEditor initialContent="hello" onSave={onSave} onCancel={() => {}} />);

    const textarea = screen.getByRole('textbox');
    await user.clear(textarea);
    await user.type(textarea, '   {Enter}');
    expect(onSave).not.toHaveBeenCalled();
  });
});
