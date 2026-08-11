import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import axios from 'axios';
import KBOverviewDashboard from './KBOverviewDashboard';

vi.mock('axios');
const mockedAxios = axios as unknown as { get: ReturnType<typeof vi.fn>; delete: ReturnType<typeof vi.fn>; patch: ReturnType<typeof vi.fn> };

vi.mock('./contexts/ThemeContext', () => ({ useTheme: () => ({ t: (k: string) => k, language: 'en' }) }));

// Role drives the actions column; each test sets it before rendering.
let mockRole = 'superadmin';
vi.mock('./contexts/AuthContext', () => ({ useAuth: () => ({ user: { id: 'op-1', role: mockRole } }) }));

const overview = {
    rows: [
        {
            id: 'kb-1', name: 'Alpha KB', ownerName: 'Ada Lovelace', ownerId: 'user-1', ownerUsername: 'ada',
            isGlobal: false, isPublished: true, fileCount: 2, totalSizeBytes: 1024, failedFileCount: 0,
            processingFileCount: 0, messageCount: 4, chatCount: 1, createdAt: '2026-01-01T00:00:00Z',
        },
        {
            id: 'kb-2', name: 'Global KB', isGlobal: true, isPublished: true, fileCount: 1, totalSizeBytes: 512,
            failedFileCount: 0, processingFileCount: 0, messageCount: 0, chatCount: 0, createdAt: '2026-01-01T00:00:00Z',
        },
    ],
    queueSummary: {},
    timestamp: '2026-01-01T00:00:00Z',
};

describe('KBOverviewDashboard superadmin actions', () => {
    beforeEach(() => {
        mockRole = 'superadmin';
        mockedAxios.get = vi.fn().mockResolvedValue({ data: overview });
        mockedAxios.delete = vi.fn().mockResolvedValue({});
        mockedAxios.patch = vi.fn().mockResolvedValue({ data: {} });
    });

    it('renders delete for every row and transfer only for personal KBs', async () => {
        render(<KBOverviewDashboard />);
        await waitFor(() => expect(screen.getAllByRole('button', { name: 'kbActionDelete' })).toHaveLength(2));
        expect(screen.getAllByRole('button', { name: 'kbActionTransfer' })).toHaveLength(1);
    });

    it('hides the actions column entirely for a non-superadmin admin', async () => {
        mockRole = 'admin';
        render(<KBOverviewDashboard />);
        await waitFor(() => expect(screen.getByText('Alpha KB')).toBeTruthy());
        expect(screen.queryByRole('button', { name: 'kbActionDelete' })).toBeNull();
        expect(screen.queryByRole('button', { name: 'kbActionTransfer' })).toBeNull();
        expect(screen.queryByText('colActions')).toBeNull();
    });
});
