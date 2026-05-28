import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import axios from 'axios';
import MemoryManager from './MemoryManager';

vi.mock('axios', () => ({
    default: { get: vi.fn(), delete: vi.fn() },
}));
vi.mock('../../contexts/ThemeContext', () => ({
    useTheme: () => ({ t: (k: string) => k, language: 'en' as const }),
}));
vi.mock('../../contexts/ModalContext', () => ({
    useModalContext: () => ({ showConfirm: vi.fn().mockResolvedValue(true) }),
}));

describe('MemoryManager', () => {
    beforeEach(() => vi.clearAllMocks());

    it('renders empty state when no memories', async () => {
        (axios.get as any).mockResolvedValue({ data: [] });
        render(<MemoryManager />);
        await waitFor(() => expect(screen.getByText('memoryNone')).toBeInTheDocument());
    });

    it('lists memories and deletes one', async () => {
        (axios.get as any).mockResolvedValue({ data: [
            { id: 1, kind: 'semantic', content: 'prefers German', kbId: '', salience: 1, createdAt: '2026-05-27T10:00:00Z' },
        ]});
        (axios.delete as any).mockResolvedValue({});
        render(<MemoryManager />);
        await waitFor(() => expect(screen.getByText('prefers German')).toBeInTheDocument());
        await userEvent.click(screen.getByTitle('delete'));
        await waitFor(() => expect(axios.delete).toHaveBeenCalledWith(expect.stringContaining('/api/user/memory/1')));
    });
});
