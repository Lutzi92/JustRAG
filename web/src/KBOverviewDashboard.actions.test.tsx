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
            processingFileCount: 0, webTurns: 3, apiTurns: 1, chatCount: 1, createdAt: '2026-01-01T00:00:00Z',
        },
        {
            id: 'kb-2', name: 'Global KB', isGlobal: true, isPublished: true, fileCount: 1, totalSizeBytes: 512,
            failedFileCount: 0, processingFileCount: 0, webTurns: 0, apiTurns: 0, chatCount: 0, createdAt: '2026-01-01T00:00:00Z',
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

// Stub Storage explicitly: local jsdom gives a bare {} while CI gives a working
// localStorage, and this component persists its optional-column visibility —
// that asymmetry is what made v0.4.0 red.
function installMemoryStorage() {
    const map = new Map<string, string>();
    const storage: Storage = {
        get length() { return map.size; },
        clear: () => map.clear(),
        getItem: (k) => (map.has(k) ? map.get(k)! : null),
        key: (i) => Array.from(map.keys())[i] ?? null,
        removeItem: (k) => { map.delete(k); },
        setItem: (k, v) => { map.set(k, v); },
    };
    Object.defineProperty(window, 'localStorage', { value: storage, configurable: true });
}

const activityOverview = {
    rows: [
        {
            id: 'kb-1', name: 'Alpha KB', ownerName: 'Ada Lovelace', ownerId: 'user-1', ownerUsername: 'ada',
            isGlobal: false, isPublished: true, fileCount: 2, totalSizeBytes: 1024, failedFileCount: 0,
            processingFileCount: 0, chatCount: 1, webTurns: 12, apiTurns: 5,
            lastFileUploadAt: '2026-08-01T00:00:00Z', lastTurnAt: '2026-08-17T10:00:00Z',
            createdAt: '2026-01-01T00:00:00Z',
        },
    ],
    queueSummary: {},
    timestamp: '2026-01-01T00:00:00Z',
};

function renderDashboard() {
    mockedAxios.get = vi.fn().mockResolvedValue({ data: activityOverview });
    mockedAxios.delete = vi.fn().mockResolvedValue({});
    mockedAxios.patch = vi.fn().mockResolvedValue({ data: {} });
    mockedAxios.post = vi.fn().mockResolvedValue({ status: 204 });
    render(<KBOverviewDashboard />);
}

// Pins the payoff of the whole usage_events plan: Aktivität sums web + API
// turns (previously only messages.created_at, blind to openaicompat/mcp
// traffic), and Letzte Aktivität follows the newer of upload/turn timestamps.
describe('KBOverviewDashboard Aktivität column', () => {
    beforeEach(() => {
        mockRole = 'superadmin';
    });

    it('renders Aktivität as web + API turns with the split in the tooltip', async () => {
        installMemoryStorage();
        renderDashboard();

        const cell = await screen.findByText('17');
        expect(cell).toBeInTheDocument();
        expect(cell.closest('td')).toHaveAttribute('title', 'Web: 12 · API: 5');
    });

    it('Letzte Aktivität uses the newest of upload and turn timestamps', async () => {
        installMemoryStorage();
        renderDashboard();

        const cells = await screen.findAllByTitle('2026-08-17T10:00:00Z');
        expect(cells.length).toBeGreaterThan(0);
    });
});

// Spec-mandated: "sorting orders by the sum" (webTurns + apiTurns), not by
// either turn count alone.
describe('KBOverviewDashboard Aktivität sort', () => {
    // webTurns-only, apiTurns-only and (webTurns+apiTurns) sort orders are
    // all made to disagree, so this only stays green if the sort is truly
    // keyed on the sum:
    //   webTurns:  Beta(0) < Gamma(1) < Alpha(10)  → Beta, Gamma, Alpha
    //   apiTurns:  Alpha(0) < Gamma(2) < Beta(8)    → Alpha, Gamma, Beta
    //   sum:       Gamma(3) < Beta(8) < Alpha(10)   → Gamma, Beta, Alpha
    const sortOverview = {
        rows: [
            {
                id: 'kb-1', name: 'Alpha KB', ownerName: 'Ada Lovelace', ownerId: 'user-1', ownerUsername: 'ada',
                isGlobal: false, isPublished: true, fileCount: 1, totalSizeBytes: 100, failedFileCount: 0,
                processingFileCount: 0, chatCount: 0, webTurns: 10, apiTurns: 0, createdAt: '2026-01-01T00:00:00Z',
            },
            {
                id: 'kb-2', name: 'Beta KB', ownerName: 'Bob', ownerId: 'user-2', ownerUsername: 'bob',
                isGlobal: false, isPublished: true, fileCount: 1, totalSizeBytes: 100, failedFileCount: 0,
                processingFileCount: 0, chatCount: 0, webTurns: 0, apiTurns: 8, createdAt: '2026-01-01T00:00:00Z',
            },
            {
                id: 'kb-3', name: 'Gamma KB', ownerName: 'Carol', ownerId: 'user-3', ownerUsername: 'carol',
                isGlobal: false, isPublished: true, fileCount: 1, totalSizeBytes: 100, failedFileCount: 0,
                processingFileCount: 0, chatCount: 0, webTurns: 1, apiTurns: 2, createdAt: '2026-01-01T00:00:00Z',
            },
        ],
        queueSummary: {},
        timestamp: '2026-01-01T00:00:00Z',
    };

    beforeEach(() => {
        mockRole = 'superadmin';
    });

    it('clicking the Aktivität header sorts rows by webTurns + apiTurns', async () => {
        installMemoryStorage();
        mockedAxios.get = vi.fn().mockResolvedValue({ data: sortOverview });
        mockedAxios.delete = vi.fn().mockResolvedValue({});
        mockedAxios.patch = vi.fn().mockResolvedValue({ data: {} });
        mockedAxios.post = vi.fn().mockResolvedValue({ status: 204 });
        render(<KBOverviewDashboard />);

        await screen.findByText('Alpha KB');

        const rowNames = () =>
            screen.getAllByRole('row')
                .slice(1) // drop the header row
                .map((row) => {
                    if (row.textContent?.includes('Alpha KB')) return 'Alpha';
                    if (row.textContent?.includes('Beta KB')) return 'Beta';
                    return 'Gamma';
                });

        // Default sort is by name ascending: Alpha, Beta, Gamma.
        expect(rowNames()).toEqual(['Alpha', 'Beta', 'Gamma']);

        await userEvent.click(screen.getByRole('columnheader', { name: 'colActivity' }));

        // Ascending by webTurns + apiTurns: Gamma(3), Beta(8), Alpha(10).
        // The fixture above is chosen so a sort keyed on webTurns alone
        // (Beta, Gamma, Alpha) or apiTurns alone (Alpha, Gamma, Beta) would
        // both produce a different order, so this only passes for the sum.
        expect(rowNames()).toEqual(['Gamma', 'Beta', 'Alpha']);
    });
});
