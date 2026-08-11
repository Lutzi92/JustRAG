import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import { KbDeleteDialog } from './KbDeleteDialog';

vi.mock('../../contexts/ThemeContext', () => ({ useTheme: () => ({ t: (k: string) => k }) }));

const baseProps = {
    kbName: 'Alpha KB',
    isGlobal: false,
    fileCount: 12,
    sizeLabel: '4.2 MB',
    chatCount: 3,
    busy: false,
    error: null,
    onCancel: () => {},
    onConfirm: () => {},
};

describe('KbDeleteDialog', () => {
    it('keeps the delete button disabled until the typed name matches exactly', () => {
        render(<KbDeleteDialog {...baseProps} />);
        const button = screen.getByRole('button', { name: 'delete' });
        const input = screen.getByLabelText('kbDeleteConfirmLabel');

        expect(button).toBeDisabled();

        fireEvent.change(input, { target: { value: 'Alpha' } });
        expect(button).toBeDisabled();

        fireEvent.change(input, { target: { value: 'alpha kb' } });
        expect(button).toBeDisabled();

        fireEvent.change(input, { target: { value: 'Alpha KB' } });
        expect(button).toBeEnabled();
    });

    it('calls onConfirm only once the name matches', () => {
        const onConfirm = vi.fn();
        render(<KbDeleteDialog {...baseProps} onConfirm={onConfirm} />);

        fireEvent.click(screen.getByRole('button', { name: 'delete' }));
        expect(onConfirm).not.toHaveBeenCalled();

        fireEvent.change(screen.getByLabelText('kbDeleteConfirmLabel'), { target: { value: 'Alpha KB' } });
        fireEvent.click(screen.getByRole('button', { name: 'delete' }));
        expect(onConfirm).toHaveBeenCalledTimes(1);
    });

    it('shows the global-KB note only for a global KB', () => {
        const { rerender } = render(<KbDeleteDialog {...baseProps} />);
        expect(screen.queryByText('kbDeleteGlobalNote')).toBeNull();

        rerender(<KbDeleteDialog {...baseProps} isGlobal />);
        expect(screen.getByText('kbDeleteGlobalNote')).toBeTruthy();
    });
});
