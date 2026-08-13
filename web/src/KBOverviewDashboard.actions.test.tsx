import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import axios from 'axios';
import KBOverviewDashboard from './KBOverviewDashboard';

vi.mock('axios');
const mockedAxios = axios as unknown as {
    get: ReturnType<typeof vi.fn>;
    delete: ReturnType<typeof vi.fn>;
    patch: ReturnType<typeof vi.fn>;
    post: ReturnType<typeof vi.fn>;
};

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
        mockedAxios.post = vi.fn().mockResolvedValue({ status: 204 });
    });

    it('renders delete for every row and transfer only for personal KBs', async () => {
        render(<KBOverviewDashboard />);
        await waitFor(() => expect(screen.getAllByRole('button', { name: 'kbActionDelete' })).toHaveLength(2));
        expect(screen.getAllByRole('button', { name: 'kbActionTransfer' })).toHaveLength(1);
    });

    // Delete und Transfer bleiben superadmin-only; Veroeffentlichen nicht — der
    // Endpunkt haengt an adminChain. Ein einfacher Systemadmin sieht also die
    // Aktionsspalte, aber nur diese eine Aktion darin.
    it('gives a non-superadmin admin the publish action and nothing else', async () => {
        mockRole = 'admin';
        render(<KBOverviewDashboard />);
        await waitFor(() => expect(screen.getByText('Alpha KB')).toBeTruthy());
        expect(screen.queryByRole('button', { name: 'kbActionDelete' })).toBeNull();
        expect(screen.queryByRole('button', { name: 'kbActionTransfer' })).toBeNull();
        expect(screen.getAllByRole('button', { name: 'kbActionPublish' })).toHaveLength(1);
    });
});

// POST /api/admin/kb/{id}/publish was registered, tested and audited on the
// backend but had no caller anywhere in the frontend — the KB-Übersicht row is
// its intended home, next to the published badge it already renders.
describe('KBOverviewDashboard publish action', () => {
    beforeEach(() => {
        mockRole = 'superadmin';
        mockedAxios.get = vi.fn().mockResolvedValue({ data: overview });
        mockedAxios.delete = vi.fn().mockResolvedValue({});
        mockedAxios.patch = vi.fn().mockResolvedValue({ data: {} });
        mockedAxios.post = vi.fn().mockResolvedValue({ status: 204 });
    });

    // kb-1 ist privat, kb-2 ist bereits oeffentlich: genau eine Schaltflaeche.
    it('offers publishing for a private KB and not for a public one', async () => {
        render(<KBOverviewDashboard />);
        await waitFor(() => expect(screen.getByText('Alpha KB')).toBeTruthy());

        const buttons = screen.getAllByRole('button', { name: 'kbActionPublish' });
        expect(buttons).toHaveLength(1);
        // Die Schaltflaeche steht in der Zeile der privaten KB, nicht der globalen.
        expect(buttons[0].closest('tr')?.textContent).toContain('Alpha KB');
    });

    it('POSTs to the publish endpoint once the dialog is confirmed', async () => {
        render(<KBOverviewDashboard />);
        await waitFor(() => expect(screen.getByText('Alpha KB')).toBeTruthy());

        await userEvent.click(screen.getByRole('button', { name: 'kbActionPublish' }));
        // Der Dialog muss die Folge benennen, bevor irgendetwas rausgeht.
        const dialog = await screen.findByRole('dialog');
        expect(within(dialog).getByText('kbPublishWarning')).toBeTruthy();
        expect(mockedAxios.post).not.toHaveBeenCalled();

        await userEvent.click(within(dialog).getByRole('button', { name: 'kbActionPublish' }));

        await waitFor(() => expect(mockedAxios.post).toHaveBeenCalledWith(
            expect.stringContaining('/api/admin/kb/kb-1/publish'),
        ));
    });

    it('sends nothing when the dialog is cancelled', async () => {
        render(<KBOverviewDashboard />);
        await waitFor(() => expect(screen.getByText('Alpha KB')).toBeTruthy());

        await userEvent.click(screen.getByRole('button', { name: 'kbActionPublish' }));
        const dialog = await screen.findByRole('dialog');
        await userEvent.click(within(dialog).getByRole('button', { name: 'cancel' }));

        expect(mockedAxios.post).not.toHaveBeenCalled();
        expect(screen.queryByRole('dialog')).toBeNull();
    });
});
