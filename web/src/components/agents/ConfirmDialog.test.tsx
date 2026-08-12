import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ConfirmDialog } from './ConfirmDialog';
import { translations } from '../../translations';

vi.mock('../../contexts/ThemeContext', () => ({
  useTheme: () => ({
    t: (key: string) => {
      const entry = translations[key as keyof typeof translations];
      return entry ? entry.en : key;
    },
  }),
}));

const baseProps = {
  title: 'Delete this agent?',
  body: 'Used by 2 teams.',
  confirmLabel: 'Delete',
  busy: false,
  error: null as string | null,
  onCancel: vi.fn(),
  onConfirm: vi.fn(),
};

describe('ConfirmDialog', () => {
  beforeEach(() => vi.clearAllMocks());

  it('renders title and body inside a modal dialog', () => {
    render(<ConfirmDialog {...baseProps} />);
    const dialog = screen.getByRole('dialog');
    expect(dialog).toHaveAttribute('aria-modal', 'true');
    expect(screen.getByText('Delete this agent?')).toBeInTheDocument();
    expect(screen.getByText('Used by 2 teams.')).toBeInTheDocument();
  });

  it('calls onConfirm when the confirm button is clicked', async () => {
    render(<ConfirmDialog {...baseProps} />);
    await userEvent.click(screen.getByRole('button', { name: 'Delete' }));
    expect(baseProps.onConfirm).toHaveBeenCalledTimes(1);
  });

  it('calls onCancel when the cancel button is clicked', async () => {
    render(<ConfirmDialog {...baseProps} />);
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(baseProps.onCancel).toHaveBeenCalledTimes(1);
  });

  it('calls onCancel on Escape', async () => {
    render(<ConfirmDialog {...baseProps} />);
    await userEvent.keyboard('{Escape}');
    expect(baseProps.onCancel).toHaveBeenCalledTimes(1);
  });

  it('focuses the confirm button on open', () => {
    render(<ConfirmDialog {...baseProps} />);
    expect(screen.getByRole('button', { name: 'Delete' })).toHaveFocus();
  });

  it('renders the error and keeps the dialog open', () => {
    render(<ConfirmDialog {...baseProps} error="Network error" />);
    expect(screen.getByRole('alert')).toHaveTextContent('Network error');
    expect(screen.getByRole('dialog')).toBeInTheDocument();
  });

  it('disables confirm and ignores Escape while busy', async () => {
    render(<ConfirmDialog {...baseProps} busy />);
    expect(screen.getByRole('button', { name: 'Delete' })).toBeDisabled();
    await userEvent.keyboard('{Escape}');
    expect(baseProps.onCancel).not.toHaveBeenCalled();
  });
});
