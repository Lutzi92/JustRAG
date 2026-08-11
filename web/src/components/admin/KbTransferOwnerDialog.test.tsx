import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import axios from 'axios';
import { KbTransferOwnerDialog } from './KbTransferOwnerDialog';

vi.mock('axios');
const mockedAxios = axios as unknown as { get: ReturnType<typeof vi.fn> };

vi.mock('../../contexts/ThemeContext', () => ({ useTheme: () => ({ t: (k: string) => k }) }));

const baseProps = {
    kbName: 'Alpha KB',
    currentOwnerId: 'user-1',
    currentOwnerName: 'Ada Lovelace',
    busy: false,
    error: null,
    onCancel: () => {},
    onConfirm: () => {},
};

describe('KbTransferOwnerDialog', () => {
    beforeEach(() => {
        mockedAxios.get = vi.fn().mockResolvedValue({
            data: [
                { id: 'user-1', username: 'ada', firstName: 'Ada', lastName: 'Lovelace' },
                { id: 'user-2', username: 'grace', firstName: 'Grace', lastName: 'Hopper' },
            ],
        });
    });

    it('excludes the current owner from the pickable users', async () => {
        render(<KbTransferOwnerDialog {...baseProps} />);
        await waitFor(() => expect(screen.getByText(/grace/)).toBeTruthy());
        expect(screen.queryByRole('button', { name: /ada/ })).toBeNull();
    });

    it('filters users by the search box', async () => {
        render(<KbTransferOwnerDialog {...baseProps} currentOwnerId={null} currentOwnerName={null} />);
        await waitFor(() => expect(screen.getByText(/grace/)).toBeTruthy());

        fireEvent.change(screen.getByLabelText('kbTransferSearchPlaceholder'), { target: { value: 'grace' } });
        expect(screen.getByText(/grace/)).toBeTruthy();
        expect(screen.queryByText(/ada/)).toBeNull();
    });

    it('passes the selected user id to onConfirm', async () => {
        const onConfirm = vi.fn();
        render(<KbTransferOwnerDialog {...baseProps} onConfirm={onConfirm} />);
        await waitFor(() => expect(screen.getByText(/grace/)).toBeTruthy());

        fireEvent.click(screen.getByText(/grace/));
        fireEvent.click(screen.getByRole('button', { name: 'kbTransferSubmit' }));
        expect(onConfirm).toHaveBeenCalledWith('user-2');
    });

    it('keeps the submit button disabled until a user is selected', async () => {
        render(<KbTransferOwnerDialog {...baseProps} />);
        await waitFor(() => expect(screen.getByText(/grace/)).toBeTruthy());
        expect(screen.getByRole('button', { name: 'kbTransferSubmit' })).toBeDisabled();
    });

    it('disables submit and blocks confirm when the selected user is filtered out of view', async () => {
        const onConfirm = vi.fn();
        render(<KbTransferOwnerDialog {...baseProps} currentOwnerId={null} currentOwnerName={null} onConfirm={onConfirm} />);
        await waitFor(() => expect(screen.getByText(/grace/)).toBeTruthy());

        fireEvent.click(screen.getByText(/grace/));
        fireEvent.change(screen.getByLabelText('kbTransferSearchPlaceholder'), { target: { value: 'ada' } });
        expect(screen.queryByText(/grace/)).toBeNull();

        const submitButton = screen.getByRole('button', { name: 'kbTransferSubmit' });
        expect(submitButton).toBeDisabled();

        fireEvent.click(submitButton);
        expect(onConfirm).not.toHaveBeenCalled();
    });
});
